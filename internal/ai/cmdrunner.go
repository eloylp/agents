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

	if err := cmd.Run(); err != nil {
		logger.Error().Err(err).Str("stderr", truncateString(stderr.String(), 2000)).Msg(fmt.Sprintf("%s command failed", r.backendName))
		return Response{}, fmt.Errorf("%s command failed: %w", r.backendName, err)
	}

	rawOut := stdout.String()
	logger.Debug().Str("raw_stdout", truncateString(rawOut, 4000)).Msg(fmt.Sprintf("%s raw output", r.backendName))

	var response Response
	if stdout.Len() == 0 {
		logger.Info().Msg(fmt.Sprintf("%s command returned no output", r.backendName))
		return Response{}, nil
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msg(fmt.Sprintf("invalid %s response", r.backendName))
		return Response{}, fmt.Errorf("parse %s response: %w", r.backendName, err)
	}
	logger.Info().Int("artifacts", len(response.Artifacts)).Msg(fmt.Sprintf("%s command completed", r.backendName))
	return response, nil
}

type promptMeta struct {
	Hash   string
	Length int
}

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

func allowCommandEnvKey(key string) bool {
	switch key {
	case "PATH", "HOME", "USER", "SHELL", "TMPDIR", "TMP", "TEMP", "LANG", "TERM", "NO_COLOR", "COLORTERM", "EDITOR", "VISUAL", "PAGER", "SSH_AUTH_SOCK", "CODEX_API_KEY", "ANTHROPIC_API_KEY", "GH_TOKEN", "GH_HOST", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "GITHUB_API_URL":
		return true
	default:
		return strings.HasPrefix(key, "LC_")
	}
}
