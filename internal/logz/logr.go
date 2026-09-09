package logz

import (
	"github.com/go-logr/logr"
	"github.com/rs/zerolog"
)

// LogrSink is a logr.LogSink implementation that forwards logs to zerolog.
type LogrSink struct {
	zerolog.Logger
}

var _ logr.LogSink = (*LogrSink)(nil)

// Init is a no-op.
func (s *LogrSink) Init(info logr.RuntimeInfo) {
	// ignore
}

// Enabled checks if the given verbosity level is enabled.
func (s *LogrSink) Enabled(level int) bool {
	// 0 is info with logr
	zlEquivalentLevel := s.Logger.GetLevel() - zerolog.InfoLevel
	return level >= int(zlEquivalentLevel)
}

// Info logs an info message with the given key-value pairs.
func (s *LogrSink) Info(level int, msg string, keysAndValues ...interface{}) {
	s.Logger.WithLevel(zerolog.InfoLevel - zerolog.Level(level)).Fields(keysAndValues).Msg(msg)
}

// Error logs an error message with the given key-value pairs.
func (s *LogrSink) Error(err error, msg string, keysAndValues ...interface{}) {
	s.Logger.WithLevel(zerolog.ErrorLevel).Fields(keysAndValues).Err(err).Msg(msg)
}

// WithValues returns a new LogrSink with the given key-value pairs added to the context.
func (s *LogrSink) WithValues(keysAndValues ...interface{}) logr.LogSink {
	return &LogrSink{Logger: s.Logger.With().Fields(keysAndValues).Logger()}
}

// WithName returns a new LogrSink with the given name added to the context.
func (s *LogrSink) WithName(name string) logr.LogSink {
	return &LogrSink{Logger: s.Logger.With().Str(LoggerNameKey, name).Logger()}
}
