package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ExecutionMode defines the operating mode for governed agent execution.
type ExecutionMode string

const (
	ExecutionModeAutonomous     ExecutionMode = "autonomous"
	ExecutionModeSupervised     ExecutionMode = "supervised"
	ExecutionModeHumanInTheLoop ExecutionMode = "human_in_the_loop"
	ExecutionModeGated          ExecutionMode = "gated"
)

// SupportedExecutionModes lists the allowed execution modes for validation.
var SupportedExecutionModes = map[ExecutionMode]struct{}{
	ExecutionModeAutonomous:     {},
	ExecutionModeSupervised:     {},
	ExecutionModeHumanInTheLoop: {},
	ExecutionModeGated:          {},
}

// LLMConfig specifies the LLM provider, model, credential reference, and generation parameters for an agent version.
// Plaintext secrets or credentials must never be stored here; use secret references (e.g. "secret:...").
type LLMConfig struct {
	Provider      string         `json:"provider,omitempty"`
	Model         string         `json:"model,omitempty"`
	CredentialRef string         `json:"credential_ref,omitempty"`
	Parameters    map[string]any `json:"parameters,omitempty"`
}

// Configuration represents the versioned configuration of an agent.
type Configuration struct {
	PromptTemplate       string         `json:"prompt_template"`
	Tools                []string       `json:"tools"`
	ReliabilityThreshold float64        `json:"reliability_threshold"`
	ExecutionMode        ExecutionMode  `json:"execution_mode"`
	Guardrails           map[string]any `json:"guardrails,omitempty"`
	Budgets              map[string]any `json:"budgets,omitempty"`
	LLMConfig            *LLMConfig     `json:"llm_config,omitempty"`
}

// Value implements driver.Valuer for database JSONB serialization.
func (c Configuration) Value() (driver.Value, error) {
	if c.Tools == nil {
		c.Tools = []string{}
	}
	return json.Marshal(c)
}

// Scan implements sql.Scanner for database JSONB deserialization.
func (c *Configuration) Scan(value any) error {
	if value == nil {
		*c = Configuration{Tools: []string{}}
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("failed to unmarshal JSONB value: expected []byte or string, got %T", value)
	}
	if err := json.Unmarshal(bytes, c); err != nil {
		return err
	}
	if c.Tools == nil {
		c.Tools = []string{}
	}
	return nil
}

// Agent represents an agent owned by a tenant.
type Agent struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentVersion represents an immutable or draft version of an agent's configuration.
type AgentVersion struct {
	ID            uuid.UUID     `json:"id"`
	AgentID       uuid.UUID     `json:"agent_id"`
	VersionNumber int           `json:"version_number"`
	Configuration Configuration `json:"configuration"`
	Published     bool          `json:"published"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	PublishedAt   *time.Time    `json:"published_at,omitempty"`
}

// AgentDetail provides an agent along with its latest published and current draft versions.
type AgentDetail struct {
	Agent                  Agent         `json:"agent"`
	LatestPublishedVersion *AgentVersion `json:"latest_published_version,omitempty"`
	DraftVersion           *AgentVersion `json:"draft_version,omitempty"`
}
