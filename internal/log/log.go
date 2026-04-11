// Package log provides a process-wide structured logger built on log/slog.
//
// Writes go to stderr so they cannot collide with the JSON result payload
// that commands print to stdout.
package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Format selects the slog handler used by Init.
type Format int

const (
	// FormatAuto picks JSON when running under GitHub Actions (GITHUB_ACTIONS=true)
	// and human-readable text otherwise.
	FormatAuto Format = iota
	// FormatJSON forces the JSON handler.
	FormatJSON
	// FormatText forces the text handler.
	FormatText
)

var logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

// Init configures the package-level logger. Safe to call once at startup.
func Init(level slog.Level, format Format) {
	initTo(os.Stderr, level, format)
}

func initTo(w io.Writer, level slog.Level, format Format) {
	opts := &slog.HandlerOptions{Level: level}
	useJSON := format == FormatJSON || (format == FormatAuto && os.Getenv("GITHUB_ACTIONS") == "true")
	var h slog.Handler
	if useJSON {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	logger = slog.New(h)
}

// L returns the current package-level logger.
func L() *slog.Logger { return logger }

// ParseLevel maps a case-insensitive string to an slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("invalid log level %q (want debug|info|warn|error)", s)
}

// ParseFormat maps a case-insensitive string to a Format.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return FormatAuto, nil
	case "json":
		return FormatJSON, nil
	case "text":
		return FormatText, nil
	}
	return FormatAuto, fmt.Errorf("invalid log format %q (want auto|json|text)", s)
}
