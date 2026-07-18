package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

func setupLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(&cleanHandler{level: level}))
}

// cleanHandler is a minimal slog handler that prints "HH:MM:SS message key=val ..."
// without the noisy level prefix.
type cleanHandler struct {
	level slog.Level
}

func (h *cleanHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *cleanHandler) Handle(_ context.Context, r slog.Record) error {
	ts := r.Time.Format("15:04:05")
	var b strings.Builder
	b.WriteString(ts)
	b.WriteByte(' ')
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(fmt.Sprintf("%v", a.Value.Any()))
		return true
	})
	fmt.Fprintln(os.Stderr, b.String())
	return nil
}

func (h *cleanHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *cleanHandler) WithGroup(name string) slog.Handler       { return h }

func setupProxy() {
	api.ConfigureHTTPClient(true)
	proxyVars := []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}
	for _, v := range proxyVars {
		if val := os.Getenv(v); val != "" {
			slog.Info(fmt.Sprintf("proxy: %s=%s", v, val))
		}
	}
}
