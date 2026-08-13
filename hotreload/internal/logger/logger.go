package logger

import (
	"log/slog"
	"os"
)

// NewLogger returns a logger writing to stdout at the given level.
func NewLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
}
