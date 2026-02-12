package claude

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
)

type Runner struct {
	mode           string
	command        string
	args           []string
	timeout        time.Duration
	maxPromptChars int
	redactionSalt  []byte
	logger         zerolog.Logger
}

type Request struct {
	Workflow    string
	Repo        string
	Number      int
	Fingerprint string
	Prompt      string
}

type Artifact struct {
	Type     string  `json:"type"`
	PartKey  string  `json:"part_key"`
	GitHubID string  `json:"github_id"`
	URL      *string `json:"url"`
}

type Response struct {
	Artifacts []Artifact `json:"artifacts"`
	Summary   string     `json:"summary"`
}

func NewRunner(cfg config.ClaudeConfig, logger zerolog.Logger) *Runner {
	salt := []byte{}
	if cfg.RedactionSaltEnv != "" {
		value := os.Getenv(cfg.RedactionSaltEnv)
		if value != "" {
			salt = []byte(value)
		}
	}
	return &Runner{
		mode:           cfg.Mode,
		command:        cfg.Command,
		args:           cfg.Args,
		timeout:        time.Duration(cfg.TimeoutSeconds) * time.Second,
		maxPromptChars: cfg.MaxPromptChars,
		redactionSalt:  salt,
		logger:         logger.With().Str("component", "claude_runner").Logger(),
	}
}

func (r *Runner) Run(ctx context.Context, req Request) (Response, error) {
	prompt := truncatePrompt(req.Prompt, r.maxPromptChars)
	promptMeta := r.promptMeta(prompt)
	logger := r.logger.With().
		Str("workflow", req.Workflow).
		Str("repo", req.Repo).
		Int("number", req.Number).
		Str("fingerprint", req.Fingerprint).
		Str("prompt_hash", promptMeta.Hash).
		Int("prompt_chars", promptMeta.Length).
		Logger()

	switch r.mode {
	case "noop":
		logger.Info().Msg("claude runner noop")
		return Response{}, nil
	case "command":
		if r.command == "" {
			return Response{}, fmt.Errorf("claude command is required when mode=command")
		}
		return r.runCommand(ctx, logger, req, prompt)
	default:
		return Response{}, fmt.Errorf("unknown claude mode: %s", r.mode)
	}
}

func (r *Runner) runCommand(ctx context.Context, logger zerolog.Logger, req Request, prompt string) (Response, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, r.command, r.args...)
	cmd.Env = append(os.Environ(),
		"AI_DAEMON_WORKFLOW="+req.Workflow,
		"AI_DAEMON_REPO="+req.Repo,
		fmt.Sprintf("AI_DAEMON_NUMBER=%d", req.Number),
		"AI_DAEMON_FINGERPRINT="+req.Fingerprint,
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = bytes.NewBufferString(prompt)

	if err := cmd.Run(); err != nil {
		logger.Error().Err(err).Str("stderr", truncateString(stderr.String(), 2000)).Msg("claude command failed")
		return Response{}, fmt.Errorf("claude command failed: %w", err)
	}

	var response Response
	if stdout.Len() == 0 {
		logger.Info().Msg("claude command returned no output")
		return Response{}, nil
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		logger.Error().Err(err).Str("stdout", truncateString(stdout.String(), 2000)).Msg("invalid claude response")
		return Response{}, fmt.Errorf("parse claude response: %w", err)
	}
	return response, nil
}

type promptMeta struct {
	Hash   string
	Length int
}

func (r *Runner) promptMeta(prompt string) promptMeta {
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
	if maxChars <= 0 || len(prompt) <= maxChars {
		return prompt
	}
	return prompt[:maxChars]
}

func truncateString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}
