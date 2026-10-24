package toolgateway

import (
	"context"
	"strings"
	"time"
)

// SensitiveKeyPatterns lists lower-cased substring patterns for keys whose values must be redacted.
var SensitiveKeyPatterns = []string{
	"key",
	"secret",
	"password",
	"token",
	"credential",
	"authorization",
	"private",
	"passphrase",
}

// RedactSensitiveData deep-copies a map and redacts values associated with sensitive keys.
func RedactSensitiveData(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}

	result := make(map[string]any, len(input))
	for k, v := range input {
		lowerKey := strings.ToLower(k)
		isSensitive := false
		for _, pattern := range SensitiveKeyPatterns {
			if strings.Contains(lowerKey, pattern) {
				isSensitive = true
				break
			}
		}

		if isSensitive {
			result[k] = "[REDACTED]"
			continue
		}

		switch val := v.(type) {
		case map[string]any:
			result[k] = RedactSensitiveData(val)
		case []any:
			result[k] = redactSlice(val)
		default:
			result[k] = v
		}
	}
	return result
}

func redactSlice(input []any) []any {
	res := make([]any, len(input))
	for i, item := range input {
		switch v := item.(type) {
		case map[string]any:
			res[i] = RedactSensitiveData(v)
		case []any:
			res[i] = redactSlice(v)
		default:
			res[i] = v
		}
	}
	return res
}

// AuditLogger records governance events for tool invocations.
type AuditLogger interface {
	Log(ctx context.Context, event AuditEvent) error
}

// StoreAuditLogger persists sanitized audit events to a Store.
type StoreAuditLogger struct {
	store Store
}

// NewStoreAuditLogger constructs an audit logger backed by Store.
func NewStoreAuditLogger(store Store) *StoreAuditLogger {
	return &StoreAuditLogger{store: store}
}

func (l *StoreAuditLogger) Log(ctx context.Context, event AuditEvent) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.SanitizedArguments != nil {
		event.SanitizedArguments = RedactSensitiveData(event.SanitizedArguments)
	}
	return l.store.SaveAuditEvent(ctx, &event)
}
