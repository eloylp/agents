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

	"github.com/rs/zerolog"
)

type CommandRunner struct {
	backendName    string
	mode           string
	command        string
	args           []string
	timeout        time.Duration
	maxPromptChars int
	redactionSalt  []byte
	logger         zerolog.Logger
}

func NewCommandRunner(backendName string, mode string, command string, args []string, timeoutSeconds int, maxPromptChars int, redactionSaltEnv string, logger zerolog.Logger) *CommandRunner {
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
		timeout:        time.Duration(timeoutSeconds) * time.Second,
		maxPromptChars: maxPromptChars,
		redactionSalt:  salt,
		logger:         logger,
	}
}

func (r *CommandRunner) Run(ctx context.Context, req Request) (Response, error) {
	prompt := truncatePrompt(req.Prompt, r.maxPromptChars)
	promptMeta := r.promptMeta(prompt)
	logger := r.logger.With().
		Str("workflow", req.Workflow).
		Str("repo", req.Repo).
		Int("number", req.Number).
		Str("prompt_hash", promptMeta.Hash).
		Int("prompt_chars", promptMeta.Length).
		Logger()

	switch r.mode {
	case "noop":
		logger.Info().Msg(fmt.Sprintf("%s runner noop", r.backendName))
		return Response{}, nil
	case "command":
		if r.command == "" {
			return Response{}, fmt.Errorf("%s command is required when mode=command", r.backendName)
		}
		logger.Info().Str("command", r.command).Msg(fmt.Sprintf("executing %s command", r.backendName))
		return r.runCommand(ctx, logger, req, prompt)
	default:
		return Response{}, fmt.Errorf("unknown %s mode: %s", r.backendName, r.mode)
	}
}

func (r *CommandRunner) runCommand(ctx context.Context, logger zerolog.Logger, req Request, prompt string) (Response, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, r.command, r.args...)
	cmd.Env = buildCommandEnv(req)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = bytes.NewBufferString(prompt)

	cmdErr := cmd.Run()

	rawOut := stdout.String()
	logger.Debug().Str("raw_stdout", truncateString(rawOut, 4000)).Msg(fmt.Sprintf("%s raw output", r.backendName))

	// If the command exited non-zero but produced no stdout, treat it as a
	// hard failure. If stdout has data we still attempt JSON parsing because
	// some AI CLIs emit non-zero exit codes even on a successful run (e.g.
	// after posting a GitHub comment via MCP tools).
	if cmdErr != nil && stdout.Len() == 0 {
		logger.Error().Err(cmdErr).Str("stderr", truncateString(stderr.String(), 2000)).Msg(fmt.Sprintf("%s command failed", r.backendName))
		return Response{}, fmt.Errorf("%s command failed: %w", r.backendName, cmdErr)
	}

	var response Response
	if stdout.Len() == 0 {
		logger.Info().Msg(fmt.Sprintf("%s command returned no output", r.backendName))
		return Response{}, nil
	}
	jsonBytes, err := extractJSON(stdout.Bytes())
	if err != nil {
		logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msg(fmt.Sprintf("invalid %s response", r.backendName))
		return Response{}, fmt.Errorf("parse %s response: %w", r.backendName, err)
	}
	if err := json.Unmarshal(jsonBytes, &response); err != nil {
		logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msg(fmt.Sprintf("invalid %s response", r.backendName))
		return Response{}, fmt.Errorf("parse %s response: %w", r.backendName, err)
	}
	if cmdErr != nil {
		logger.Warn().Err(cmdErr).Str("stderr", truncateString(stderr.String(), 2000)).Msg(fmt.Sprintf("%s command exited non-zero but produced valid output", r.backendName))
	}
	logger.Info().Int("artifacts", len(response.Artifacts)).Msg(fmt.Sprintf("%s command completed", r.backendName))
	return response, nil
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
		Length: len(prompt),
	}
}

func truncatePrompt(prompt string, maxChars int) string {
	if maxChars <= 0 {
		return prompt
	}
	runes := []rune(prompt)
	if len(runes) <= maxChars {
		return prompt
	}
	return string(runes[:maxChars])
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
// the host environment plus workflow-specific AI_DAEMON_* variables. The
// allowlist prevents leaking unintended host secrets to the AI backend process.
func buildCommandEnv(req Request) []string {
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
		fmt.Sprintf("AI_DAEMON_NUMBER=%d", req.Number),
	)
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
	case "PATH", "HOME", "USER", "SHELL", "TMPDIR", "TMP", "TEMP", "LANG", "TERM", "NO_COLOR", "COLORTERM", "EDITOR", "VISUAL", "PAGER", "SSH_AUTH_SOCK", "CODEX_API_KEY", "ANTHROPIC_API_KEY", "GH_TOKEN", "GH_HOST", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "GITHUB_API_URL":
		return true
	default:
		return strings.HasPrefix(key, "LC_")
	}
}
