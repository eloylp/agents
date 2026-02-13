package openai

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

	"github.com/eloylp/agents/internal/ai"
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

func NewRunner(cfg config.OpenAIConfig, logger zerolog.Logger) *Runner {
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
		logger:         logger.With().Str("component", "openai_runner").Logger(),
	}
}

func (r *Runner) Run(ctx context.Context, req ai.Request) (ai.Response, error) {
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
		logger.Info().Msg("openai runner noop")
		return ai.Response{}, nil
	case "command":
		if r.command == "" {
			return ai.Response{}, fmt.Errorf("openai command is required when mode=command")
		}
		logger.Info().Str("command", r.command).Msg("executing openai command")
		return r.runCommand(ctx, logger, req, prompt)
	default:
		return ai.Response{}, fmt.Errorf("unknown openai mode: %s", r.mode)
	}
}

func (r *Runner) runCommand(ctx context.Context, logger zerolog.Logger, req ai.Request, prompt string) (ai.Response, error) {
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
		logger.Error().Err(err).Str("stderr", truncateString(stderr.String(), 2000)).Msg("openai command failed")
		return ai.Response{}, fmt.Errorf("openai command failed: %w", err)
	}

	rawOut := stdout.String()
	logger.Debug().Str("raw_stdout", truncateString(rawOut, 4000)).Msg("openai raw output")

	var response ai.Response
	if stdout.Len() == 0 {
		logger.Info().Msg("openai command returned no output")
		return ai.Response{}, nil
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		logger.Error().Err(err).Str("raw_stdout", truncateString(rawOut, 4000)).Msg("invalid openai response")
		return ai.Response{}, fmt.Errorf("parse openai response: %w", err)
	}
	logger.Info().Int("artifacts", len(response.Artifacts)).Msg("openai command completed")
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
