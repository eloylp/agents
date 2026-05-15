package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	runtimeexec "github.com/eloylp/agents/internal/runtime"
	"github.com/rs/zerolog"
)

type CommandRunner struct {
	backendName    string
	command        string
	env            map[string]string
	timeout        time.Duration
	maxPromptChars int
	logger         zerolog.Logger
	container      runtimeexec.Runner
	containerImage string
	containerSpec  runtimeexecSpec
}

type runtimeexecSpec = runtimeexec.ContainerSpec

const (
	containerWorkspaceDir   = runtimeexec.RunnerWorkspaceDir
	containerResponseSchema = runtimeexec.RunnerResponseSchema
)

func newCommandRunner(backendName string, command string, env map[string]string, timeoutSeconds int, maxPromptChars int, logger zerolog.Logger) *CommandRunner {
	return &CommandRunner{
		backendName:    backendName,
		command:        command,
		env:            env,
		timeout:        time.Duration(timeoutSeconds) * time.Second,
		maxPromptChars: maxPromptChars,
		logger:         logger,
	}
}

func NewContainerCommandRunner(backendName string, command string, env map[string]string, timeoutSeconds int, maxPromptChars int, runner runtimeexec.Runner, image string, spec runtimeexecSpec, logger zerolog.Logger) *CommandRunner {
	r := newCommandRunner(backendName, command, env, timeoutSeconds, maxPromptChars, logger)
	r.container = runner
	r.containerImage = image
	r.containerSpec = spec
	return r
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

	if r.container == nil {
		return Response{}, fmt.Errorf("%s runner requires container runtime", r.backendName)
	}
	if r.command == "" {
		return Response{}, fmt.Errorf("%s command is required", r.backendName)
	}
	logger.Info().Str("command", r.command).Msgf("executing %s command", r.backendName)
	return r.runCommand(ctx, logger, req)
}

// runCommand executes the backend CLI. For the claude backend the system
// content is delivered via --append-system-prompt so Claude Code's built-in
// tool definitions are preserved; the user content travels on stdin as before.
// For all other backends (codex, openai_compatible, unknown) the two parts are
// concatenated and sent on stdin, the semantics are identical to the previous
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

	env := buildCommandEnv(req, r.env)
	stdoutCap, stderr, cmdErr := r.runContainerCommand(cmdCtx, args, stdin, env, req.OnLine)
	if cmdErr != nil {
		switch {
		case errors.Is(cmdCtx.Err(), context.DeadlineExceeded):
			cmdErr = CommandInterruptedError{
				Backend: r.backendName,
				Kind:    CommandInterruptedTimeout,
				Timeout: r.timeout,
				Err:     cmdErr,
			}
		case errors.Is(cmdCtx.Err(), context.Canceled):
			cmdErr = CommandInterruptedError{
				Backend: r.backendName,
				Kind:    CommandInterruptedCanceled,
				Err:     cmdErr,
			}
		}
	}
	return r.parseCommandResponse(logger, stdoutCap, stderr, cmdErr)
}

func (r *CommandRunner) runContainerCommand(ctx context.Context, args []string, stdin string, env []string, onLine func([]byte)) (lineCapture, string, error) {
	writer := &captureWriter{onLine: onLine}
	var stderr bytes.Buffer

	setup := runtimeexec.BackendSetupOptions{}
	if r.isCodexBackend() {
		var schemaErr error
		args, schemaErr = rewriteContainerResponseSchemaArg(args)
		if schemaErr != nil {
			return lineCapture{}, "", schemaErr
		}
		setup.ResponseSchema = ResponseSchemaString()
	}

	command := slices.Concat([]string{r.command}, args)
	command, env = runtimeexec.WrapBackendCommand(r.backendName, command, env, setup)
	spec := r.containerSpec
	spec.Image = r.containerImage
	spec.Command = command
	spec.WorkingDir = containerWorkspaceDir
	spec.Env = env
	spec.Stdin = bytes.NewBufferString(stdin)
	spec.Stdout = writer
	spec.Stderr = &stderr
	spec.Labels = map[string]string{
		"org.opencontainers.image.title": "agents-runner",
		"agents.backend":                 r.backendName,
	}
	status, err := r.container.Run(ctx, spec)
	writer.flush()
	if err != nil {
		return writer.capture, stderr.String(), err
	}
	if status.Code != 0 {
		return writer.capture, stderr.String(), fmt.Errorf("runner container exited with status %d", status.Code)
	}
	return writer.capture, stderr.String(), nil
}

func rewriteContainerResponseSchemaArg(args []string) ([]string, error) {
	if !slices.Contains(args, "--output-schema") {
		return args, nil
	}
	out := slices.Clone(args)
	for i := 0; i < len(out)-1; i++ {
		if out[i] == "--output-schema" {
			out[i+1] = containerResponseSchema
			return out, nil
		}
	}
	return nil, fmt.Errorf("codex output schema flag missing path")
}

func (r *CommandRunner) parseCommandResponse(logger zerolog.Logger, stdoutCap lineCapture, stderr string, cmdErr error) (Response, error) {
	rawOut := stdoutCap.all.String()
	logger.Debug().Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("%s raw output", r.backendName)
	backendDetail := backendFailureDetail(stdoutCap.lines, stderr)

	// If the command exited non-zero but produced no stdout, treat it as a
	// hard failure. If stdout has data we still attempt JSON parsing because
	// some AI CLIs emit non-zero exit codes even on a successful run (e.g.
	// after posting a GitHub comment via MCP tools).
	if cmdErr != nil && stdoutCap.all.Len() == 0 {
		logger.Error().Err(cmdErr).Str("stderr", truncateString(stderr, 2000)).Msgf("%s command failed", r.backendName)
		err := fmt.Errorf("%s command failed: %w", r.backendName, cmdErr)
		return Response{}, runnerFailure(r.backendName, commandFailureKind(cmdErr, backendDetail), backendDetail, err)
	}

	var response Response
	if stdoutCap.all.Len() == 0 {
		logger.Error().Msgf("%s command returned no output", r.backendName)
		err := fmt.Errorf("parse %s response: empty response (no output)", r.backendName)
		return Response{}, runnerFailure(r.backendName, FailureKindParseError, backendDetail, err)
	}

	// Codex with --json wraps the schema-constrained response inside the
	// last agent_message item's `text` field. Peel that envelope first so
	// the generic structured_output / extractJSON path sees just the
	// response JSON. For non-codex (or codex without --json output), the
	// peel is a no-op and the generic flow runs unchanged.
	stdoutForParse := stdoutCap.all.Bytes()
	if r.isCodexBackend() {
		if peeled, ok := extractCodexAgentMessageJSON(stdoutForParse); ok {
			stdoutForParse = peeled
		}
	}

	// When the CLI uses --output-format json (single-object envelope) or
	// --output-format stream-json (JSONL), the structured_output field holds
	// the schema-constrained response. Try parsing the full stdout as a single
	// JSON envelope first (handles --output-format json and any backend that
	// emits a single result object).
	if parsed, ok := extractStructuredOutput(stdoutForParse); ok {
		if err := json.Unmarshal(parsed, &response); err != nil {
			logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("invalid %s structured_output", r.backendName)
			parseErr := fmt.Errorf("parse %s response: %w", r.backendName, err)
			return Response{}, runnerFailure(r.backendName, FailureKindParseError, backendDetail, parseErr)
		}
	} else {
		// Fallback: find the last top-level JSON object in raw stdout. For
		// --output-format stream-json this is the result event, which also
		// has a structured_output wrapper. For legacy CLIs it may be a bare
		// response object.
		jsonBytes, err := extractJSON(stdoutForParse)
		if err != nil {
			logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("invalid %s response", r.backendName)
			parseErr := fmt.Errorf("parse %s response: %w", r.backendName, err)
			return Response{}, runnerFailure(r.backendName, FailureKindParseError, backendDetail, parseErr)
		}
		// Try structured_output on the last JSON (handles stream-json result event).
		if parsed2, ok2 := extractStructuredOutput(jsonBytes); ok2 {
			if err := json.Unmarshal(parsed2, &response); err != nil {
				logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("invalid %s structured_output", r.backendName)
				parseErr := fmt.Errorf("parse %s response: %w", r.backendName, err)
				return Response{}, runnerFailure(r.backendName, FailureKindParseError, backendDetail, parseErr)
			}
		} else {
			// Legacy path: bare JSON response object with no envelope.
			if err := json.Unmarshal(jsonBytes, &response); err != nil {
				logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("invalid %s response", r.backendName)
				parseErr := fmt.Errorf("parse %s response: %w", r.backendName, err)
				return Response{}, runnerFailure(r.backendName, FailureKindParseError, backendDetail, parseErr)
			}
		}
	}

	// Extract the tool-loop transcript from the backend's JSONL stdout.
	// Both parsers return TraceStep values with kind="tool" or "thinking".
	// This is best-effort, errors are logged but do not fail the run.
	switch {
	case strings.HasPrefix(r.backendName, "claude"):
		response.Steps = parseClaudeSteps(stdoutCap.lines)
	case strings.HasPrefix(r.backendName, "codex"):
		response.Steps = parseCodexSteps(stdoutCap.lines)
	}
	// Token usage is best-effort: scan the last JSON envelope for a
	// usage subobject. Anthropic and OpenAI shapes are both accepted.
	response.Usage = extractUsage(stdoutCap.all.Bytes())
	if response.Summary == "" && len(response.Artifacts) == 0 && len(response.Dispatch) == 0 {
		logger.Error().Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("%s response is empty (no summary, artifacts, or dispatch)", r.backendName)
		if cmdErr != nil {
			err := fmt.Errorf("%s command failed: %w", r.backendName, cmdErr)
			return Response{}, runnerFailure(r.backendName, commandFailureKind(cmdErr, backendDetail), backendDetail, err)
		}
		err := fmt.Errorf("parse %s response: empty response (no fields populated)", r.backendName)
		kind := FailureKindParseError
		if backendDetail != "" {
			kind = commandFailureKind(nil, backendDetail)
		}
		return Response{}, runnerFailure(r.backendName, kind, backendDetail, err)
	}
	if cmdErr != nil {
		var interrupted CommandInterruptedError
		if errors.As(cmdErr, &interrupted) {
			logger.Error().Err(cmdErr).Str("stderr", truncateString(stderr, 2000)).Msgf("%s command interrupted after valid partial output", r.backendName)
			return response, runnerFailure(r.backendName, commandFailureKind(cmdErr, backendDetail), backendDetail, cmdErr)
		}
		logger.Warn().Err(cmdErr).Str("stderr", truncateString(stderr, 2000)).Msgf("%s command exited non-zero but produced valid output", r.backendName)
	}
	logger.Info().
		Int("artifacts", len(response.Artifacts)).
		Int("dispatch", len(response.Dispatch)).
		Int("summary_len", len(response.Summary)).
		Msgf("%s command completed", r.backendName)
	return response, nil
}

type captureWriter struct {
	capture lineCapture
	onLine  func([]byte)
	pending []byte
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		idx := bytes.IndexByte(w.pending, '\n')
		if idx < 0 {
			return len(p), nil
		}
		line := bytes.TrimRight(w.pending[:idx], "\r")
		w.capture.addLine(line)
		if w.onLine != nil {
			w.onLine(line)
		}
		w.pending = w.pending[idx+1:]
	}
}

func (w *captureWriter) flush() {
	if len(w.pending) == 0 {
		return
	}
	line := bytes.TrimRight(w.pending, "\r")
	w.capture.addLine(line)
	if w.onLine != nil {
		w.onLine(line)
	}
	w.pending = nil
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
// --output-schema pointing to the runner-visible response schema path.
// --json makes codex emit one event per JSONL line on stdout so the
// daemon can reconstruct the tool-loop transcript (parseCodexSteps).
func (r *CommandRunner) buildCodexDelivery(req Request) (args []string, stdin string) {
	combinedStdin := truncateString(combineSystemUser(req.System, req.User), r.maxPromptChars)
	args = []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, "--output-schema", containerResponseSchema)
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
//     "cache_creation_input_tokens","cache_read_input_tokens"}
//   - OpenAI / Codex envelope:
//     {"prompt_tokens","completion_tokens","total_tokens"}
//
// Returns the zero Usage when no usable field is found. Best-effort:
// usage is observability, not load-bearing, a missing parse just
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
// iteration, so each line gets its own time.Now(), lines that arrive in
// different pipe reads will have meaningfully different timestamps.
type lineCapture struct {
	lines []timedLine
	all   bytes.Buffer
}

// addLine records a single line (without trailing newline) and its arrival time.
func (c *lineCapture) addLine(data []byte) {
	line := append(bytes.Clone(data), '\n')
	c.lines = append(c.lines, timedLine{data: line, at: time.Now()})
	c.all.Write(line)
}

// parseClaudeSteps scans --output-format stream-json stdout and reconstructs
// the run's transcript as a slice of TraceStep values. Two kinds are emitted
// in stream order:
//
//   - "thinking": one step per text content block in an assistant event.
//     InputSummary carries the full text.
//   - "tool":     one step per tool_use + matching tool_result pair, joined
//     by tool_use_id. ToolName is the tool name; InputSummary
//     is the call args (raw JSON); OutputSummary is the
//     tool's reply; DurationMs is the wall-clock interval
//     between the tool_use and tool_result lines.
//
// Step content is preserved in full up to 64 KB per field; longer values are
// truncated with a marker noting how many bytes were cut. Steps are capped
// at 100 per run. Any event that cannot be parsed is silently skipped, this
// is best-effort.
func parseClaudeSteps(lines []timedLine) []TraceStep {
	parser := newTimedTraceStepParser("claude")
	var steps []TraceStep
	for _, tl := range lines {
		steps = append(steps, parser.process(tl.data, tl.at)...)
		if len(steps) >= 100 {
			return steps[:100]
		}
	}
	return steps
}

// parseCodexSteps scans `codex exec --json` stdout (one JSONL event per line)
// and reconstructs the run transcript as a slice of TraceStep values. Two
// kinds are emitted in stream order:
//
//   - "thinking": one step per `item.completed` event whose `item.type` is
//     `agent_message`. InputSummary carries the message text.
//   - "tool":     one step per `item.completed` event whose `item.type` is
//     a recognised tool (`command_execution`, `mcp_tool_call`,
//     etc.). ToolName, InputSummary, and OutputSummary are
//     populated from the matching item fields. DurationMs is
//     the wall-clock delta between the prior `item.started`
//     and this `item.completed` line, when both are observed.
//
// Unrecognised item.completed kinds are skipped. The 64 KB-per-field cap
// (capStepContent) and 100-step cap match parseClaudeSteps exactly. Any
// event that cannot be parsed is silently skipped, this is best-effort.
func parseCodexSteps(lines []timedLine) []TraceStep {
	parser := newTimedTraceStepParser("codex")
	var steps []TraceStep
	for _, tl := range lines {
		steps = append(steps, parser.process(tl.data, tl.at)...)
		if len(steps) >= 100 {
			return steps[:100]
		}
	}
	return steps
}

// extractCodexAgentMessageJSON walks codex --json JSONL output and returns
// the bytes of the last `agent_message.item.text` field. Codex emits the
// schema-constrained response as that field's content (a JSON envelope
// when --output-schema is set), so peeling this envelope lets the generic
// response parser handle it like any other JSON-emitting backend.
//
// Returns (text-bytes, true) when an agent_message is found; (nil, false)
// otherwise. Best-effort: any parse error per line is silently skipped.
func extractCodexAgentMessageJSON(all []byte) ([]byte, bool) {
	type item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type evt struct {
		Type string `json:"type"`
		Item item   `json:"item"`
	}
	var lastText string
	var found bool
	for line := range bytes.Lines(all) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e evt
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.Type != "item.completed" || e.Item.Type != "agent_message" {
			continue
		}
		lastText = e.Item.Text
		found = true
	}
	if !found {
		return nil, false
	}
	return []byte(lastText), true
}

// stepContentMaxBytes bounds the per-field content stored on a TraceStep.
// Set generously so typical tool I/O and thinking blocks pass through
// untouched; pathological output (binary blobs, runaway logs) is cut with
// a clear marker so the user can see something was truncated.
const stepContentMaxBytes = 64 * 1024

// capStepContent returns s if it fits under stepContentMaxBytes, otherwise
// returns the head plus a clear truncation marker. The cut point is rounded
// down to a valid UTF-8 boundary so the result is always valid UTF-8.
func capStepContent(s string) string {
	if len(s) <= stepContentMaxBytes {
		return s
	}
	end := stepContentMaxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + fmt.Sprintf("…[truncated, %d more bytes]", len(s)-end)
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
	// Try array of content blocks, concatenate all text blocks.
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
	case "PATH", "HOME", "USER", "SHELL", "TMPDIR", "TMP", "TEMP", "LANG", "TERM", "NO_COLOR", "COLORTERM", "EDITOR", "VISUAL", "PAGER", "SSH_AUTH_SOCK", "CODEX_AUTH_JSON_BASE64", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL", "GITHUB_TOKEN", "GH_TOKEN", "GH_HOST", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "GITHUB_API_URL":
		return true
	default:
		return strings.HasPrefix(key, "LC_")
	}
}
