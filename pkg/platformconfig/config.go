package platformconfig

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Canonical platform configuration keys.
const (
	KeyGatewayRateLimitDefault    = "gateway.rate_limit.default"
	KeyCalibrationSamplingInterval = "calibration.sampling_interval"
	KeyReliabilityDefaultThreshold = "reliability.default_threshold"
	KeyRetrievalTopK              = "retrieval.top_k"
	KeyRuntimeCheckpointRetention = "runtime.checkpoint.retention"
)

// Item represents a single persisted platform configuration parameter.
type Item struct {
	Key         string    `json:"key" gorm:"primaryKey;type:text"`
	Label       string    `json:"label" gorm:"type:text;not null"`
	Value       string    `json:"value" gorm:"type:text;not null"`
	Description string    `json:"description" gorm:"type:text;not null"`
	Category    string    `json:"category" gorm:"type:text;not null"`
	UpdatedBy   string    `json:"updated_by,omitempty" gorm:"type:text;not null;default:''"`
	UpdatedAt   time.Time `json:"updated_at" gorm:"not null;autoUpdateTime"`
}

// TableName specifies the PostgreSQL table name for GORM.
func (Item) TableName() string {
	return "platform_configs"
}

// AuditEvent records an operator change made in Platform Operations.
type AuditEvent struct {
	ID        string    `json:"id" gorm:"primaryKey;type:text"`
	Title     string    `json:"title" gorm:"type:text;not null"`
	Detail    string    `json:"detail" gorm:"type:text;not null"`
	Operator  string    `json:"operator" gorm:"type:text;not null"`
	CreatedAt time.Time `json:"created_at" gorm:"not null;autoCreateTime"`
}

// TableName specifies the PostgreSQL table name for GORM.
func (AuditEvent) TableName() string {
	return "platform_audit_logs"
}

// Snapshot contains typed, validated platform settings ready for runtime enforcement.
type Snapshot struct {
	RateLimitPerMinute          int     `json:"rate_limit_per_minute"`
	RateLimitRPS                float64 `json:"rate_limit_rps"`
	RateLimitBurst              int     `json:"rate_limit_burst"`
	CalibrationSamplingInterval int     `json:"calibration_sampling_interval"`
	DefaultReliabilityThreshold float64 `json:"default_reliability_threshold"`
	RetrievalTopK               int     `json:"retrieval_top_k"`
	RetentionDays               int     `json:"retention_days"`
}

// DefaultItems returns the canonical allow-listed platform settings and their defaults.
func DefaultItems() []Item {
	return []Item{
		{
			Key:         KeyGatewayRateLimitDefault,
			Label:       "Tenant Request Rate Limit",
			Value:       "1200",
			Description: "Maximum API requests allowed per minute for each organization (10–100000).",
			Category:    "gateway",
		},
		{
			Key:         KeyCalibrationSamplingInterval,
			Label:       "Autonomous Calibration Sampling",
			Value:       "10",
			Description: "How often autonomous (non-escalated) executions are sent for human calibration review (1 out of every N runs; 0 disables sampling).",
			Category:    "calibration",
		},
		{
			Key:         KeyReliabilityDefaultThreshold,
			Label:       "Reliability Confidence Threshold",
			Value:       "0.85",
			Description: "Default minimum AI confidence score (0.00 to 1.00) required when an agent version does not specify an explicit threshold.",
			Category:    "calibration",
		},
		{
			Key:         KeyRetrievalTopK,
			Label:       "Similar Precedents Retrieved",
			Value:       "5",
			Description: "Number of closest historical execution decisions retrieved from pgvector to evaluate new cases (1–50).",
			Category:    "runtime",
		},
		{
			Key:         KeyRuntimeCheckpointRetention,
			Label:       "Execution History Retention",
			Value:       "30d",
			Description: "Number of days workflow execution state, event histories, and step checkpoints are preserved in Temporal (1d–365d).",
			Category:    "runtime",
		},
	}
}

// DefaultSnapshot returns the typed defaults matching DefaultItems().
func DefaultSnapshot() Snapshot {
	snap, _ := BuildSnapshot(DefaultItems())
	return snap
}

// ValidateValue validates a key/value pair against strict allow-lists and bounds,
// returning the normalized canonical string value.
func ValidateValue(key, rawValue string) (string, error) {
	val := strings.TrimSpace(rawValue)
	switch key {
	case KeyGatewayRateLimitDefault:
		n, err := strconv.Atoi(val)
		if err != nil || n < 10 || n > 100000 {
			return "", fmt.Errorf("%s must be an integer between 10 and 100000 requests/minute", key)
		}
		return strconv.Itoa(n), nil

	case KeyCalibrationSamplingInterval:
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 || n > 10000 {
			return "", fmt.Errorf("%s must be an integer between 0 and 10000 (0 disables periodic sampling)", key)
		}
		return strconv.Itoa(n), nil

	case KeyReliabilityDefaultThreshold:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil || f < 0.0 || f > 1.0 {
			return "", fmt.Errorf("%s must be a number between 0.00 and 1.00", key)
		}
		return fmt.Sprintf("%.2f", f), nil

	case KeyRetrievalTopK:
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 || n > 50 {
			return "", fmt.Errorf("%s must be an integer between 1 and 50", key)
		}
		return strconv.Itoa(n), nil

	case KeyRuntimeCheckpointRetention:
		days, err := ParseRetentionDays(val)
		if err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
		return fmt.Sprintf("%dd", days), nil

	default:
		return "", fmt.Errorf("unsupported platform configuration key: %q", key)
	}
}

// ParseRetentionDays parses strings like "30d" or "30" into an integer day count in [1, 365].
func ParseRetentionDays(raw string) (int, error) {
	cleaned := strings.ToLower(strings.TrimSpace(raw))
	cleaned = strings.TrimSuffix(cleaned, "d")
	days, err := strconv.Atoi(cleaned)
	if err != nil || days < 1 || days > 365 {
		return 0, fmt.Errorf("must be a duration in days between 1d and 365d (e.g. 30d)")
	}
	return days, nil
}

// BuildSnapshot converts a slice of config Items into a typed Snapshot.
func BuildSnapshot(items []Item) (Snapshot, error) {
	snap := Snapshot{
		RateLimitPerMinute:          1200,
		RateLimitRPS:                20.0,
		RateLimitBurst:              40,
		CalibrationSamplingInterval: 10,
		DefaultReliabilityThreshold: 0.85,
		RetrievalTopK:               5,
		RetentionDays:               30,
	}

	for _, item := range items {
		norm, err := ValidateValue(item.Key, item.Value)
		if err != nil {
			return Snapshot{}, err
		}
		switch item.Key {
		case KeyGatewayRateLimitDefault:
			perMin, _ := strconv.Atoi(norm)
			rps := float64(perMin) / 60.0
			burst := int(rps * 2)
			if burst < 5 {
				burst = 5
			}
			snap.RateLimitPerMinute = perMin
			snap.RateLimitRPS = rps
			snap.RateLimitBurst = burst
		case KeyCalibrationSamplingInterval:
			snap.CalibrationSamplingInterval, _ = strconv.Atoi(norm)
		case KeyReliabilityDefaultThreshold:
			snap.DefaultReliabilityThreshold, _ = strconv.ParseFloat(norm, 64)
		case KeyRetrievalTopK:
			snap.RetrievalTopK, _ = strconv.Atoi(norm)
		case KeyRuntimeCheckpointRetention:
			snap.RetentionDays, _ = ParseRetentionDays(norm)
		}
	}

	return snap, nil
}
