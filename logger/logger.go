// Package logger
package logger

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const (
	FormatText = "text"
	FormatJSON = "json"
)

func Init(levelValue string, format string) error {
	level, err := parseLevel(levelValue)
	if err != nil {
		return err
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler

	switch format {
	case FormatJSON:
		handler = slog.NewJSONHandler(os.Stdout, opts)
	case FormatText:
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		return fmt.Errorf("unknown log format: %s", format)
	}

	slog.SetDefault(slog.New(handler))

	return nil
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level: %s", value)
	}
}
