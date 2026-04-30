package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"
)

type CommandRunner struct {
	backendName    string
	mode           string
	command        string
	env            map[string]string
	timeout        time.Duration
	maxPromptChars int
	logger         zerolog.Logger
}

func NewCommandRunner(backendName string, mode string, command string, env map[string]string, timeoutSeconds int, maxPromptChars int, logger zerolog.Logger) *CommandRunner {
	return &CommandRunner{
		backendName:    backendName,
		mode:           mode,
		command:        command,
		env:            env,
		timeout:        time.Duration(timeoutSeconds) * time.Second,
		maxPromptChars: maxPromptChars,
		logger:         logger,
	}
}

func (r *CommandRunner) Run(ctx context.Context, req Request) (Response, error) {
	// The engine records the full composed prompt on the trace span,
	// so per-run logging only needs enough breadcrumbs to correlate
	// with that trace: workflow + repo + number + delivered prompt
	// length.
	combined := truncateString(combineSystemUser(req.System, req.User), r.maxPromptChars)
	logger := r.logger.With().
		Str("workflow", req.Workflow).
		Str("repo", req.Repo).
		Int("number", req.Number).
		Int("prompt_chars", len([]rune(combined))).
		Logger()

	switch r.mode {
	case "noop":
		logger.Info().Msgf("%s runner noop", r.backendName)
		return Response{}, nil
	case "command":
		if r.command == "" {
			return Response{}, fmt.Errorf("%s command is required when mode=command", r.backendName)
		}
		logger.Info().Str("command", r.command).Msgf("executing %s command", r.backendName)
		return r.runCommand(ctx, logger, req)
	default:
		return Response{}, fmt.Errorf("unknown %s mode: %s", r.backendName, r.mode)
	}
}

// runCommand executes the backend CLI. For the claude backend the system
// content is delivered via --append-system-prompt so Claude Code's built-in
// tool definitions are preserved; the user content travels on stdin as before.
// For all other backends (codex, openai_compatible, unknown) the two parts are
// concatenated and sent on stdin — the semantics are identical to the previous
// single-prompt behaviour.
func (r *CommandRunner) runCommand(ctx context.Context, logger zerolog.Logger, req Request) (Response, error) {
	var cmdCtx context.Context
	var cancel context.CancelFunc
	if r.timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, r.timeout)
	} else {
		cmdCtx, cancel = ctx, func() {}
	}
	defer cancel()

	// Build the final arg list and stdin content for this request.
	args, stdin := r.buildDelivery(req)

	cmd := exec.CommandContext(cmdCtx, r.command, args...)
	cmd.Env = buildCommandEnv(req, r.env)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdin = bytes.NewBufferString(stdin)

	stdoutPipe, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return Response{}, fmt.Errorf("stdout pipe: %w", pipeErr)
	}
	if err := cmd.Start(); err != nil {
		return Response{}, fmt.Errorf("start %s: %w", r.backendName, err)
	}

	// Read stdout line by line using ReadBytes so there is no per-line size cap
	// (unlike bufio.Scanner which silently truncates at its buffer limit).
	// ReadBytes blocks until a complete newline-delimited line arrives from the
	// pipe, so time.Now() inside addLine reflects when that specific line was
	// received — not when a larger pipe-read chunk happened to land.
	var stdoutCap lineCapture
	reader := bufio.NewReader(stdoutPipe)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			stdoutCap.addLine(bytes.TrimRight(line, "\n"))
		}
		if err != nil {
			if err != io.EOF {
				logger.Warn().Err(err).Msgf("read %s stdout", r.backendName)
			}
			break
		}
	}

	cmdErr := cmd.Wait()

	rawOut := stdoutCap.all.String()
	logger.Debug().Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("%s raw output", r.backendName)

	// If the command exited non-zero but produced no stdout, treat it as a
	// hard failure. If stdout has data we still attempt JSON parsing because
	// some AI CLIs emit non-zero exit codes even on a successful run (e.g.
	// after posting a GitHub comment via MCP tools).
	if cmdErr != nil && stdoutCap.all.Len() == 0 {
		logger.Error().Err(cmdErr).Str("stderr", truncateString(stderr.String(), 2000)).Msgf("%s command failed", r.backendName)
		return Response{}, fmt.Errorf("%s command failed: %w", r.backendName, cmdErr)
	}

	var response Response
	if stdoutCap.all.Len() == 0 {
		logger.Error().Msgf("%s command returned no output", r.backendName)
		return Response{}, fmt.Errorf("parse %s response: empty response (no output)", r.backendName)
	}

	// When the CLI uses --output-format json (single-object envelope) or
	// --output-format stream-json (JSONL), the structured_output field holds
	// the schema-constrained response. Try parsing the full stdout as a single
	// JSON envelope first (handles --output-format json and any backend that
	// emits a single result object).
	if parsed, ok := extractStructuredOutput(stdoutCap.all.Bytes()); ok {
		if err := json.Unmarshal(parsed, &response); err != nil {
			logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("invalid %s structured_output", r.backendName)
			return Response{}, fmt.Errorf("parse %s response: %w", r.backendName, err)
		}
	} else {
		// Fallback: find the last top-level JSON object in raw stdout. For
		// --output-format stream-json this is the result event, which also
		// has a structured_output wrapper. For legacy CLIs it may be a bare
		// response object.
		jsonBytes, err := extractJSON(stdoutCap.all.Bytes())
		if err != nil {
			logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("invalid %s response", r.backendName)
			return Response{}, fmt.Errorf("parse %s response: %w", r.backendName, err)
		}
		// Try structured_output on the last JSON (handles stream-json result event).
		if parsed2, ok2 := extractStructuredOutput(jsonBytes); ok2 {
			if err := json.Unmarshal(parsed2, &response); err != nil {
				logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("invalid %s structured_output", r.backendName)
				return Response{}, fmt.Errorf("parse %s response: %w", r.backendName, err)
			}
		} else {
			// Legacy path: bare JSON response object with no envelope.
			if err := json.Unmarshal(jsonBytes, &response); err != nil {
				logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("invalid %s response", r.backendName)
				return Response{}, fmt.Errorf("parse %s response: %w", r.backendName, err)
			}
		}
	}

	// Extract the tool-loop transcript from stream-json output (claude backends).
	// This is a best-effort parse — errors are logged but do not fail the run.
	if strings.HasPrefix(r.backendName, "claude") {
		response.Steps = parseClaudeSteps(stdoutCap.lines)
	}
	// Token usage is best-effort: scan the last JSON envelope for a
	// usage subobject. Anthropic and OpenAI shapes are both accepted.
	response.Usage = extractUsage(stdoutCap.all.Bytes())
	if response.Summary == "" && len(response.Artifacts) == 0 && len(response.Dispatch) == 0 {
		logger.Error().Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("%s response is empty (no summary, artifacts, or dispatch)", r.backendName)
		return Response{}, fmt.Errorf("parse %s response: empty response (no fields populated)", r.backendName)
	}
	if cmdErr != nil {
		logger.Warn().Err(cmdErr).Str("stderr", truncateString(stderr.String(), 2000)).Msgf("%s command exited non-zero but produced valid output", r.backendName)
	}
	logger.Info().
		Int("artifacts", len(response.Artifacts)).
		Int("dispatch", len(response.Dispatch)).
		Int("summary_len", len(response.Summary)).
		Msgf("%s command completed", r.backendName)
	return response, nil
}

// separatorRunes is the rune-count of the "\n\n" separator inserted between
// System and User when both are non-empty. It is a named constant so the
// budget arithmetic in buildDelivery and its tests stay in sync.
const separatorRunes = 2

// buildDelivery returns the final args slice and stdin string to use for the
// given request. The delivery strategy depends on the backend:
//
//   - claude (and any backend whose name starts with "claude"): system content
//     is appended to Claude Code's built-in system prompt via
//     --append-system-prompt so tool definitions and permissions are preserved.
//     User content is passed via stdin.
//
//   - all other backends (codex, openai_compatible, …): system and user
//     content are concatenated with a blank-line separator and sent on stdin.
//     This matches the previous single-prompt behaviour and documents the
//     limitation that codex has no native system channel.
func (r *CommandRunner) buildDelivery(req Request) (args []string, stdin string) {
	if r.isClaudeBackend() {
		return r.buildClaudeDelivery(req)
	}
	if r.isCodexBackend() {
		return r.buildCodexDelivery(req)
	}
	return nil, truncateString(combineSystemUser(req.System, req.User), r.maxPromptChars)
}

func (r *CommandRunner) isClaudeBackend() bool { return r.hasBackendPrefix("claude") }
func (r *CommandRunner) isCodexBackend() bool  { return r.hasBackendPrefix("codex") }

// hasBackendPrefix reports whether the backend name or command basename starts
// with prefix (case-insensitive). Centralises the name/command normalisation
// so each new backend type only needs a one-liner predicate.
func (r *CommandRunner) hasBackendPrefix(prefix string) bool {
	name := strings.ToLower(strings.TrimSpace(r.backendName))
	cmd := strings.ToLower(filepath.Base(strings.TrimSpace(r.command)))
	return strings.HasPrefix(name, prefix) || strings.HasPrefix(cmd, prefix)
}

// buildClaudeDelivery builds hardcoded Claude CLI args and delivers system
// content via --append-system-prompt and user content via stdin.
func (r *CommandRunner) buildClaudeDelivery(req Request) (args []string, stdin string) {
	args = []string{
		"-p",
		"--dangerously-skip-permissions",
		"--verbose",
		"--output-format", "stream-json",
		"--json-schema", ResponseSchemaString(),
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	if req.System == "" {
		return args, truncateString(req.User, r.maxPromptChars)
	}

	// Enforce the budget on the same logical combined prompt as every
	// other backend (System + "\n\n" + User), then re-split the
	// truncated result for transport.  This guarantees both paths
	// truncate at the identical logical boundary even when the cut
	// falls inside System or within the separator.
	combined := combineSystemUser(req.System, req.User)
	truncated := truncateString(combined, r.maxPromptChars)
	truncatedRunes := utf8.RuneCountInString(truncated)
	systemRunes := utf8.RuneCountInString(req.System)

	// userStartInCombined is where user content begins in the combined
	// string (after System + the two-rune "\n\n" separator).
	userStartInCombined := systemRunes + separatorRunes

	if truncatedRunes <= userStartInCombined {
		args = append(args, "--append-system-prompt", truncated)
		return args, ""
	}

	args = append(args, "--append-system-prompt", req.System)
	stdin = string([]rune(truncated)[userStartInCombined:])
	return args, stdin
}

// buildCodexDelivery concatenates system + user on stdin and appends
// --output-schema pointing to the embedded response schema temp file.
func (r *CommandRunner) buildCodexDelivery(req Request) (args []string, stdin string) {
	combinedStdin := truncateString(combineSystemUser(req.System, req.User), r.maxPromptChars)
	args = []string{
		"exec",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	schemaPath, err := ResponseSchemaPath()
	if err != nil {
		r.logger.Error().Err(err).Msg("failed to materialize embedded response schema")
		return args, combinedStdin
	}
	args = append(args, "--output-schema", schemaPath)
	return args, combinedStdin
}

// combineSystemUser joins system and user content with a blank-line separator,
// but only inserts the separator when both parts are non-empty. This ensures
// the logical combined prompt shape is consistent with what every backend
// actually delivers.
func combineSystemUser(system, user string) string {
	if system == "" {
		return user
	}
	if user == "" {
		return system
	}
	return system + "\n\n" + user
}

func truncateString(value string, maxChars int) string {
	if maxChars <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxChars {
		return value
	}
	return string(runes[:maxChars])
}

// buildCommandEnv constructs the subprocess environment from an allowlist of
// the host environment plus workflow-specific AI_DAEMON_* variables, and
// finally merges any per-backend overrides on top (last-write wins). The
// allowlist prevents leaking unintended host secrets; the per-backend map
// lets a single backend (e.g. claude) be routed via different endpoints
// (hosted vs local) without touching the container env.
func buildCommandEnv(req Request, backendEnv map[string]string) []string {
	env := make([]string, 0, 32)
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if allowCommandEnvKey(key) {
			env = append(env, entry)
		}
	}
	env = append(env,
		"AI_DAEMON_WORKFLOW="+req.Workflow,
		"AI_DAEMON_REPO="+req.Repo,
	)
	if req.Number != 0 {
		env = append(env, fmt.Sprintf("AI_DAEMON_NUMBER=%d", req.Number))
	}
	for k, v := range backendEnv {
		env = append(env, k+"="+v)
	}
	return env
}

// extractUsage scans the last top-level JSON object in data for a
// usage subobject and returns it. Handles both shapes seen in the
// wild:
//
//   - Anthropic / Claude Code stream-json `result` event:
//     {"input_tokens","output_tokens",
//      "cache_creation_input_tokens","cache_read_input_tokens"}
//   - OpenAI / Codex envelope:
//     {"prompt_tokens","completion_tokens","total_tokens"}
//
// Returns the zero Usage when no usable field is found. Best-effort:
// usage is observability, not load-bearing — a missing parse just
// means the row shows zero tokens, not a failed run.
func extractUsage(data []byte) Usage {
	last, err := extractJSON(data)
	if err != nil {
		return Usage{}
	}
	var envelope struct {
		Usage *struct {
			// Anthropic
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			// OpenAI
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(last, &envelope); err != nil || envelope.Usage == nil {
		return Usage{}
	}
	u := envelope.Usage
	out := Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
	// Fall back to OpenAI shape when Anthropic fields are absent.
	if out.InputTokens == 0 && u.PromptTokens > 0 {
		out.InputTokens = u.PromptTokens
	}
	if out.OutputTokens == 0 && u.CompletionTokens > 0 {
		out.OutputTokens = u.CompletionTokens
	}
	return out
}

// extractStructuredOutput checks whether data is a CLI envelope
// ({"type":"result",...,"structured_output":{...}}) and returns the
// structured_output value as raw JSON bytes. Returns (nil, false) if the
// envelope is absent or structured_output is missing/null.
func extractStructuredOutput(data []byte) (json.RawMessage, bool) {
	var envelope struct {
		Type             string          `json:"type"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, false
	}
	if envelope.Type != "result" || len(envelope.StructuredOutput) == 0 {
		return nil, false
	}
	// Reject JSON null.
	if string(envelope.StructuredOutput) == "null" {
		return nil, false
	}
	return envelope.StructuredOutput, true
}

// extractJSON finds the last top-level JSON object in data.
// AI CLIs sometimes emit conversational text before the JSON payload.
// This function scans forward through data looking for '{' characters and
// attempts to decode a complete JSON object at each one using encoding/json.
// The last successfully decoded object is returned.  Using the standard
// decoder avoids the string-escape edge cases in a hand-rolled backward
// scanner (e.g. a string ending with '\\' would be misread as an escaped
// quote by a naive backward scan).
func extractJSON(data []byte) ([]byte, error) {
	var last json.RawMessage
	for i := 0; i < len(data); i++ {
		if data[i] != '{' {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(data[i:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == nil {
			last = raw
			i += len(raw) - 1 // advance past the decoded object
		}
	}
	if last == nil {
		return nil, fmt.Errorf("no JSON object found in output")
	}
	return last, nil
}

// timedLine pairs a JSONL line with the wall-clock time it arrived from the
// subprocess. Used by parseClaudeSteps to compute per-tool DurationMs.
type timedLine struct {
	data []byte
	at   time.Time
}

// lineCapture records each stdout line together with the wall-clock time it
// was read from the subprocess pipe. addLine is called once per Scanner.Scan
// iteration, so each line gets its own time.Now() — lines that arrive in
// different pipe reads will have meaningfully different timestamps.
type lineCapture struct {
	lines []timedLine
	all   bytes.Buffer
}

// addLine records a single line (without trailing newline) and its arrival time.
func (c *lineCapture) addLine(data []byte) {
	line := make([]byte, len(data)+1)
	copy(line, data)
	line[len(data)] = '\n'
	c.lines = append(c.lines, timedLine{data: line, at: time.Now()})
	c.all.Write(line)
}

// parseClaudeSteps scans --output-format stream-json stdout and reconstructs
// the tool-loop transcript as a slice of TraceStep values. Each step pairs one
// tool_use block (from an assistant event) with its corresponding tool_result
// block (from the following user event), matched by tool_use_id.
//
// DurationMs is computed as the wall-clock interval between the line that
// carried the tool_use block and the line that carried the matching
// tool_result block, giving a coarse-grained measure of how long the tool ran.
//
// Steps are capped at 100 and input/output are truncated to 200 runes.
// Any event that cannot be parsed is silently skipped — this is best-effort.
func parseClaudeSteps(lines []timedLine) []TraceStep {
	// streamEvent is the minimal shape of a single JSONL line.
	type contentBlock struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"` // string or array
	}
	type streamEvent struct {
		Type    string `json:"type"`
		Message struct {
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}

	type pending struct {
		name   string
		input  string
		order  int
		seenAt time.Time
	}
	pendingTools := make(map[string]pending) // tool_use_id → pending
	var steps []TraceStep
	order := 0

	for _, tl := range lines {
		line := bytes.TrimSpace(tl.data)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "assistant":
			for _, b := range ev.Message.Content {
				if b.Type != "tool_use" || b.ID == "" {
					continue
				}
				pendingTools[b.ID] = pending{
					name:   b.Name,
					input:  truncateString(string(b.Input), 200),
					order:  order,
					seenAt: tl.at,
				}
				order++
			}
		case "user":
			for _, b := range ev.Message.Content {
				if b.Type != "tool_result" || b.ToolUseID == "" {
					continue
				}
				p, ok := pendingTools[b.ToolUseID]
				if !ok {
					continue
				}
				delete(pendingTools, b.ToolUseID)
				output := extractToolResultText(b.Content)
				steps = append(steps, TraceStep{
					ToolName:      p.name,
					InputSummary:  p.input,
					OutputSummary: truncateString(output, 200),
					DurationMs:    tl.at.Sub(p.seenAt).Milliseconds(),
				})
				if len(steps) >= 100 {
					return steps
				}
			}
		}
	}
	return steps
}

// extractToolResultText returns a plain-text summary from a tool_result
// content field, which may be a JSON string or an array of content blocks.
func extractToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try array of content blocks — concatenate all text blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// allowCommandEnvKey reports whether key is safe to forward to the AI backend
// subprocess. Only variables required for tool operation (auth tokens, paths,
// locale) are permitted; everything else is excluded.
func allowCommandEnvKey(key string) bool {
	switch key {
	case "PATH", "HOME", "USER", "SHELL", "TMPDIR", "TMP", "TEMP", "LANG", "TERM", "NO_COLOR", "COLORTERM", "EDITOR", "VISUAL", "PAGER", "SSH_AUTH_SOCK", "CODEX_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL", "GH_TOKEN", "GH_HOST", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "GITHUB_API_URL":
		return true
	default:
		return strings.HasPrefix(key, "LC_")
	}
}
