package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"
)

type CommandRunner struct {
	backendName    string
	mode           string
	command        string
	args           []string
	env            map[string]string
	timeout        time.Duration
	maxPromptChars int
	redactionSalt  []byte
	logger         zerolog.Logger
}

func NewCommandRunner(backendName string, mode string, command string, args []string, env map[string]string, timeoutSeconds int, maxPromptChars int, redactionSaltEnv string, logger zerolog.Logger) *CommandRunner {
	salt := []byte{}
	if redactionSaltEnv != "" {
		value := os.Getenv(redactionSaltEnv)
		if value != "" {
			salt = []byte(value)
		}
	}
	return &CommandRunner{
		backendName:    backendName,
		mode:           mode,
		command:        command,
		args:           args,
		env:            env,
		timeout:        time.Duration(timeoutSeconds) * time.Second,
		maxPromptChars: maxPromptChars,
		redactionSalt:  salt,
		logger:         logger,
	}
}

func (r *CommandRunner) Run(ctx context.Context, req Request) (Response, error) {
	// Compute hash and length from the same logical combined prompt that
	// buildDelivery will deliver: join with "\n\n" only when both parts are
	// non-empty, then truncate to the configured budget so the logged values
	// always reflect the actually-delivered content.
	combined := truncateString(combineSystemUser(req.System, req.User), r.maxPromptChars)
	promptMeta := r.promptMeta(combined)
	logger := r.logger.With().
		Str("workflow", req.Workflow).
		Str("repo", req.Repo).
		Int("number", req.Number).
		Str("prompt_hash", promptMeta.Hash).
		Int("prompt_chars", promptMeta.Length).
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

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = bytes.NewBufferString(stdin)

	cmdErr := cmd.Run()

	rawOut := stdout.String()
	logger.Debug().Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("%s raw output", r.backendName)

	// If the command exited non-zero but produced no stdout, treat it as a
	// hard failure. If stdout has data we still attempt JSON parsing because
	// some AI CLIs emit non-zero exit codes even on a successful run (e.g.
	// after posting a GitHub comment via MCP tools).
	if cmdErr != nil && stdout.Len() == 0 {
		logger.Error().Err(cmdErr).Str("stderr", truncateString(stderr.String(), 2000)).Msgf("%s command failed", r.backendName)
		return Response{}, fmt.Errorf("%s command failed: %w", r.backendName, cmdErr)
	}

	var response Response
	if stdout.Len() == 0 {
		logger.Error().Msgf("%s command returned no output", r.backendName)
		return Response{}, fmt.Errorf("parse %s response: empty response (no output)", r.backendName)
	}
	jsonBytes, err := extractJSON(stdout.Bytes())
	if err != nil {
		logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("invalid %s response", r.backendName)
		return Response{}, fmt.Errorf("parse %s response: %w", r.backendName, err)
	}
	if err := json.Unmarshal(jsonBytes, &response); err != nil {
		logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msgf("invalid %s response", r.backendName)
		return Response{}, fmt.Errorf("parse %s response: %w", r.backendName, err)
	}
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
	if strings.HasPrefix(r.backendName, "claude") && req.System != "" {
		// Deliver system content via --append-system-prompt; user via stdin.
		// Build a new slice so concurrent calls do not share the underlying
		// array from r.args.
		args = make([]string, 0, len(r.args)+2)
		args = append(args, r.args...)

		// Enforce the combined prompt budget against the same logical shape as
		// the fallback path: System + "\n\n" + User (separator only when both
		// are non-empty).  Budget the system part first; whatever headroom
		// remains (minus the separator) is available for the user turn.
		systemContent := req.System
		userBudget := r.maxPromptChars
		if userBudget > 0 {
			systemRunes := utf8.RuneCountInString(req.System)
			if systemRunes >= userBudget {
				// System fills or exceeds the budget; truncate it and send no user content.
				systemContent = truncateString(req.System, userBudget)
				args = append(args, "--append-system-prompt", systemContent)
				stdin = ""
				return args, stdin
			}
			// Reserve headroom for system + separator, leaving the rest for user.
			userBudget -= systemRunes + separatorRunes
			if userBudget <= 0 {
				// Separator sits at or past the budget boundary; no room for user.
				args = append(args, "--append-system-prompt", systemContent)
				stdin = ""
				return args, stdin
			}
		}
		args = append(args, "--append-system-prompt", systemContent)
		stdin = truncateString(req.User, userBudget)
		return args, stdin
	}
	// Fallback: concatenate system + user with the same separator rule, send on stdin.
	return r.args, truncateString(combineSystemUser(req.System, req.User), r.maxPromptChars)
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

type promptMeta struct {
	Hash   string
	Length int
}

// promptMeta returns a salted SHA-256 hash of the prompt for log attribution.
// The salt is prepended so that the hash cannot be reversed to recover the
// prompt even if the hash output is observed.
func (r *CommandRunner) promptMeta(prompt string) promptMeta {
	hasher := sha256.New()
	if len(r.redactionSalt) > 0 {
		_, _ = hasher.Write(r.redactionSalt)
	}
	_, _ = hasher.Write([]byte(prompt))
	return promptMeta{
		Hash:   hex.EncodeToString(hasher.Sum(nil)),
		Length: utf8.RuneCountInString(prompt),
	}
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
