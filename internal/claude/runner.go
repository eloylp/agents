package claude

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

type Runner struct {
	runner *ai.CommandRunner
}

func NewRunner(cfg config.ClaudeConfig, logger zerolog.Logger) *Runner {
	return &Runner{
		runner: ai.NewCommandRunner(
			"claude",
			cfg.Mode,
			cfg.Command,
			cfg.Args,
			cfg.TimeoutSeconds,
			cfg.MaxPromptChars,
			cfg.RedactionSaltEnv,
			logger.With().Str("component", "claude_runner").Logger(),
		),
	}
}

func (r *Runner) Run(ctx context.Context, req ai.Request) (ai.Response, error) {
	return r.runner.Run(ctx, req)
}
