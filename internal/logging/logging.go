package logging

import (
	"fmt"
	"io"
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

	var formatErr error
	var writer io.Writer = zerolog.ConsoleWriter{Out: os.Stdout}
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "json":
		writer = os.Stdout
	case "text", "":
		// default writer already set
	default:
		formatErr = fmt.Errorf("unknown log format %q", cfg.Format)
	}

	logger := zerolog.New(writer).With().Timestamp().Logger()
	if levelErr != nil {
		logger.Warn().Str("configured_level", cfg.Level).Err(levelErr).Msg("invalid log level, defaulting to info")
	}
	if formatErr != nil {
		logger.Warn().Str("configured_format", cfg.Format).Err(formatErr).Msg("unknown log format, defaulting to text")
	}
	return logger
}
