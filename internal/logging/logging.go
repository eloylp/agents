package logging

import (
	"os"
	"strings"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
)

func NewLogger(cfg config.LogConfig) zerolog.Logger {
	level := zerolog.InfoLevel
	var levelErr error
	if cfg.Level != "" {
		parsed, err := zerolog.ParseLevel(strings.ToLower(cfg.Level))
		if err == nil {
			level = parsed
		} else {
			levelErr = err
		}
	}
	zerolog.SetGlobalLevel(level)
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	if levelErr != nil {
		logger.Warn().Str("configured_level", cfg.Level).Err(levelErr).Msg("invalid log level, defaulting to info")
	}
	return logger
}
