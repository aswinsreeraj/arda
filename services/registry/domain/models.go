package domain

import (
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

// Configuration represents the versioned configuration of an agent.
type Configuration struct {
	PromptTemplate       string         `json:"prompt_template"`
	Tools                []string       `json:"tools"`
	ReliabilityThreshold float64        `json:"reliability_threshold"`
	ExecutionMode        ExecutionMode  `json:"execution_mode"`
	Guardrails           map[string]any `json:"guardrails,omitempty"`
	Budgets              map[string]any `json:"budgets,omitempty"`
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
