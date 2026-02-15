package openai

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

type Runner struct {
	runner *ai.CommandRunner
}

func NewRunner(cfg config.OpenAIConfig, logger zerolog.Logger) *Runner {
	return &Runner{
		runner: ai.NewCommandRunner(
			"openai",
			cfg.Mode,
			cfg.Command,
			cfg.Args,
			cfg.TimeoutSeconds,
			cfg.MaxPromptChars,
			cfg.RedactionSaltEnv,
			logger.With().Str("component", "openai_runner").Logger(),
		),
	}
}

func (r *Runner) Run(ctx context.Context, req ai.Request) (ai.Response, error) {
	return r.runner.Run(ctx, req)
}
