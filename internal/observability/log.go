package observability

import (
	"fmt"
	"io"
	"log/slog"
)

func New(out io.Writer, level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("log level: %w", err)
	}
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: l})), nil
}
