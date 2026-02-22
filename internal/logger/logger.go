package logger

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

const (
	maxBufferLines = 100
	flushInterval  = 1 * time.Second
	maxLogAge      = 7 * 24 * time.Hour
	cleanupInterval = 24 * time.Hour
)

// HandlerLogger provides per-handler file-based logging.
type HandlerLogger struct {
	name    string
	mu      sync.Mutex
	buffer  []string
	file    *os.File
	date    string
	ticker  *time.Ticker
	done    chan struct{}
}

var (
	loggers   = make(map[string]*HandlerLogger)
	loggersMu sync.Mutex
	cleanOnce sync.Once
)

var nameRe = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeName converts a handler name to a safe filename component.
func sanitizeName(name string) string {
	return nameRe.ReplaceAllString(strings.ToLower(name), "-")
}

// For returns (or creates) a logger for the given handler name.
func For(name string) *HandlerLogger {
	loggersMu.Lock()
	defer loggersMu.Unlock()

	safeName := sanitizeName(name)
	if l, ok := loggers[safeName]; ok {
		return l
	}

	l := &HandlerLogger{
		name: safeName,
		done: make(chan struct{}),
	}

	l.ticker = time.NewTicker(flushInterval)
	go l.flushLoop()

	loggers[safeName] = l

	// Start cleanup goroutine once
	cleanOnce.Do(func() {
		go cleanupLoop()
	})

	return l
}

// Log writes a log line to the handler's log file.
func (l *HandlerLogger) Log(format string, args ...any) {
	line := fmt.Sprintf("%s %s",
		time.Now().Format("2006-01-02 15:04:05"),
		fmt.Sprintf(format, args...),
	)

	l.mu.Lock()
	l.buffer = append(l.buffer, line)
	if len(l.buffer) >= maxBufferLines {
		l.flushLocked()
	}
	l.mu.Unlock()
}

func (l *HandlerLogger) flushLoop() {
	for {
		select {
		case <-l.ticker.C:
			l.mu.Lock()
			l.flushLocked()
			l.mu.Unlock()
		case <-l.done:
			return
		}
	}
}

func (l *HandlerLogger) flushLocked() {
	if len(l.buffer) == 0 {
		return
	}

	today := time.Now().Format("2006-01-02")

	// Rotate file if date changed
	if l.date != today || l.file == nil {
		if l.file != nil {
			l.file.Close()
		}
		logDir := state.LogDir()
		path := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", l.name, today))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			slog.Error("failed to open log file", "path", path, "error", err)
			l.buffer = nil
			return
		}
		l.file = f
		l.date = today
	}

	for _, line := range l.buffer {
		fmt.Fprintln(l.file, line)
	}
	l.buffer = nil
}

// Close flushes remaining buffer and closes the file.
func (l *HandlerLogger) Close() {
	l.ticker.Stop()
	close(l.done)
	l.mu.Lock()
	l.flushLocked()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	l.mu.Unlock()
}

// CloseAll flushes and closes all loggers. Call on process exit.
func CloseAll() {
	loggersMu.Lock()
	defer loggersMu.Unlock()
	for _, l := range loggers {
		l.Close()
	}
}

// cleanupLoop periodically deletes log files older than maxLogAge.
func cleanupLoop() {
	for {
		cleanOldLogs()
		time.Sleep(cleanupInterval)
	}
}

func cleanOldLogs() {
	logDir := state.LogDir()
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-maxLogAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(logDir, entry.Name())
			os.Remove(path)
			slog.Debug("removed old log file", "path", path)
		}
	}
}
