package logging

import (
	"os"
	"strings"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
)

func NewLogger(cfg config.LogConfig) zerolog.Logger {
	level := zerolog.InfoLevel
	if cfg.Level != "" {
		parsed, err := zerolog.ParseLevel(strings.ToLower(cfg.Level))
		if err == nil {
			level = parsed
		}
	}
	zerolog.SetGlobalLevel(level)
	return zerolog.New(os.Stdout).With().Timestamp().Logger()
}
