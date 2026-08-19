package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type contextKey string

const (
	TraceIDKey contextKey = "trace_id"
	UserIDKey  contextKey = "user_id"
)

// PIIKeys maps privacy-sensitive metadata to strip out of storage logs automatically
var PIIKeys = []string{
	"password", "cvv", "card", "card_number", "token", "secret", "cvc", "pin", "auth", "authorization",
}

// LevelSecurity is a custom logging weight for critical security/fraud audits (Sub-level 12)
const LevelSecurity = slog.Level(12)

type SecurityLogger struct {
	logger *slog.Logger
}

var DefaultLogger *SecurityLogger

func init() {
	programLevel := new(slog.LevelVar) // programLevel implements slog.Leveler
	opts := &slog.HandlerOptions{
		Level: programLevel,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			keyLower := strings.ToLower(a.Key)
			for _, piiKey := range PIIKeys {
				if strings.Contains(keyLower, piiKey) {
					return slog.String(a.Key, "[REDACTED]")
				}
			}
			return a
		},
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)
	DefaultLogger = &SecurityLogger{
		logger: slog.New(handler),
	}
}

// WithContext extracts TraceID and UserID context variables and attaches them structurally
func (l *SecurityLogger) WithContext(ctx context.Context) *slog.Logger {
	logger := l.logger
	if ctx == nil {
		return logger
	}
	if traceID, ok := ctx.Value(TraceIDKey).(string); ok && traceID != "" {
		logger = logger.With("trace_id", traceID)
	}
	if userID, ok := ctx.Value(UserIDKey).(string); ok && userID != "" {
		logger = logger.With("user_id", userID)
	}
	return logger
}

func Info(ctx context.Context, msg string, args ...any) {
	DefaultLogger.WithContext(ctx).Info(msg, args...)
}

func Warn(ctx context.Context, msg string, args ...any) {
	DefaultLogger.WithContext(ctx).Warn(msg, args...)
}

func Error(ctx context.Context, msg string, args ...any) {
	DefaultLogger.WithContext(ctx).Error(msg, args...)
}

// Security emits an immutable custom logging entry with higher priority weight for fraud detection audits
func Security(ctx context.Context, msg string, args ...any) {
	// FIXED: Direct structural log parsing mapping for custom priority logging blocks safely
	DefaultLogger.WithContext(ctx).Log(ctx, LevelSecurity, msg, args...)
}
