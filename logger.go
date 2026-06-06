package gormmetrics

import (
	"log"
	"os"
)

// Logger is the minimal logging contract gormmetrics needs. Implement it to
// hook the library into any structured logger (zap, slog, logr, ...).
//
// Levels:
//   - Infof: lifecycle events (probe failed, scraper disabled) — uncommon.
//   - Debugf: per-scrape diagnostic noise — disabled by default.
type Logger interface {
	Infof(format string, args ...any)
	Debugf(format string, args ...any)
}

type stdLogger struct{ l *log.Logger }

func (s stdLogger) Infof(format string, args ...any)  { s.l.Printf("INFO "+format, args...) }
func (s stdLogger) Debugf(format string, args ...any) { s.l.Printf("DEBUG "+format, args...) }

func defaultLogger() Logger {
	return stdLogger{l: log.New(os.Stderr, "gormmetrics ", log.LstdFlags|log.Lmicroseconds)}
}

// NopLogger discards all output. Useful in tests.
type NopLogger struct{}

func (NopLogger) Infof(string, ...any)  {}
func (NopLogger) Debugf(string, ...any) {}
