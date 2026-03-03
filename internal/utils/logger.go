package utils

import (
	"context"
	"io"
	"log/slog"
	"os"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Logger is the package-level default logger set during InitLogger.
var Logger *slog.Logger

type logContextKey struct{}
type traceIDContextKey struct{}

// InitLogger configures the global slog logger based on settings.
func InitLogger(settings *Settings) {
	var level slog.Level
	switch settings.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var writers []io.Writer
	if settings.LogToStderr {
		writers = append(writers, os.Stderr)
	}
	if settings.LogToFile {
		writers = append(writers, &lumberjack.Logger{
			Filename:   settings.LogFilePath,
			MaxSize:    10,
			MaxBackups: 5,
			MaxAge:     28,
			Compress:   true,
		})
	}
	mw := io.MultiWriter(writers...)

	var handler slog.Handler
	if settings.LogFormat == "json" {
		handler = slog.NewJSONHandler(mw, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewTextHandler(mw, &slog.HandlerOptions{Level: level})
	}

	Logger = slog.New(handler)
	slog.SetDefault(Logger)
	Logger.Info("Logger initialized", "level", settings.LogLevel, "format", settings.LogFormat)
}

// InitLoggerWithContext initializes the logger and stores it in the context.
func InitLoggerWithContext(ctx context.Context, settings *Settings) (*slog.Logger, context.Context) {
	InitLogger(settings)
	return Logger, context.WithValue(ctx, logContextKey{}, Logger)
}

// GetLogger retrieves the logger stored in ctx, falling back to the global Logger.
func GetLogger(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(logContextKey{}).(*slog.Logger); ok {
			return l
		}
	}
	return Logger
}

// WithLogger stores logger in ctx.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, logContextKey{}, logger)
}

// SetTraceID stores a trace ID in ctx.
func SetTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDContextKey{}, traceID)
}

// GetTraceID retrieves the trace ID from ctx.
func GetTraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDContextKey{}).(string); ok {
		return v
	}
	return ""
}
