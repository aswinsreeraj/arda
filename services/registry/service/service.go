package service

import (
	"context"
	"fmt"

	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/aswinsreeraj/arda/services/registry/store"
	"github.com/google/uuid"
)

// CreateAgentRequest holds parameters to create a new agent.
type CreateAgentRequest struct {
	Name          string                `json:"name"`
	Configuration *domain.Configuration `json:"configuration,omitempty"`

	// Flat configuration fields for compatibility with direct creation payloads
	PromptTemplate       string               `json:"prompt_template,omitempty"`
	Tools                []string             `json:"tools,omitempty"`
	ReliabilityThreshold *float64             `json:"reliability_threshold,omitempty"`
	ExecutionMode        domain.ExecutionMode `json:"execution_mode,omitempty"`
	Guardrails           map[string]any       `json:"guardrails,omitempty"`
	Budgets              map[string]any       `json:"budgets,omitempty"`
}

// ExtractConfiguration merges nested or flat configuration fields from the request.
func (r CreateAgentRequest) ExtractConfiguration() *domain.Configuration {
	if r.Configuration != nil {
		return r.Configuration
	}
	// Check if flat fields were provided
	if r.PromptTemplate != "" || len(r.Tools) > 0 || r.ReliabilityThreshold != nil || r.ExecutionMode != "" {
		threshold := 0.85
		if r.ReliabilityThreshold != nil {
			threshold = *r.ReliabilityThreshold
		}
		mode := r.ExecutionMode
		if mode == "" {
			mode = domain.ExecutionModeSupervised
		}
		tools := r.Tools
		if tools == nil {
			tools = []string{}
		}
		return &domain.Configuration{
			PromptTemplate:       r.PromptTemplate,
			Tools:                tools,
			ReliabilityThreshold: threshold,
			ExecutionMode:        mode,
			Guardrails:           r.Guardrails,
			Budgets:              r.Budgets,
		}
	}
	return nil
}

// RegistryService coordinates agent and version management with strict tenant scoping.
type RegistryService struct {
	store store.Store
}

// NewRegistryService creates a new RegistryService instance.
func NewRegistryService(s store.Store) *RegistryService {
	return &RegistryService{store: s}
}

// CreateAgent creates an agent for the authenticated tenant.
func (s *RegistryService) CreateAgent(ctx context.Context, tenantID uuid.UUID, req CreateAgentRequest) (*domain.AgentDetail, error) {
	if tenantID == uuid.Nil {
		return nil, domain.NewUnauthenticatedError("missing or invalid tenant identity")
	}

	if err := domain.ValidateAgentName(req.Name); err != nil {
		return nil, err
	}

	agent := &domain.Agent{
		ID:       uuid.New(),
		TenantID: tenantID,
		Name:     req.Name,
	}

	cfg := req.ExtractConfiguration()
	if cfg != nil {
		if err := domain.ValidateConfiguration(*cfg); err != nil {
			return nil, err
		}
		version := &domain.AgentVersion{
			ID:            uuid.New(),
			AgentID:       agent.ID,
			VersionNumber: 1,
			Configuration: *cfg,
			Published:     false,
		}

		if err := s.store.CreateAgentWithInitialVersion(ctx, agent, version); err != nil {
			return nil, err
		}

		return &domain.AgentDetail{
			Agent:        *agent,
			DraftVersion: version,
		}, nil
	}

	if err := s.store.CreateAgent(ctx, agent); err != nil {
		return nil, err
	}

	return &domain.AgentDetail{
		Agent: *agent,
	}, nil
}

// GetAgent retrieves an agent by ID scoped strictly to the tenant.
func (s *RegistryService) GetAgent(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.AgentDetail, error) {
	if tenantID == uuid.Nil {
		return nil, domain.NewUnauthenticatedError("missing or invalid tenant identity")
	}
	if agentID == uuid.Nil {
		return nil, domain.NewValidationError("invalid agent id")
	}

	agent, err := s.store.GetAgent(ctx, tenantID, agentID)
	if err != nil {
		return nil, err
	}

	latestPub, err := s.store.GetLatestPublishedVersion(ctx, agentID)
	if err != nil {
		var domErr *domain.DomainError
		if !(err != nil && (domErr != nil || true)) {
			// Ignore if not found
		}
	}

	latestDraft, _ := s.store.GetLatestDraftVersion(ctx, agentID)

	return &domain.AgentDetail{
		Agent:                  *agent,
		LatestPublishedVersion: latestPub,
		DraftVersion:           latestDraft,
	}, nil
}

// CreateDraftVersion creates a new draft version with JSONB configuration for an agent.
func (s *RegistryService) CreateDraftVersion(ctx context.Context, tenantID, agentID uuid.UUID, cfg domain.Configuration) (*domain.AgentVersion, error) {
	if tenantID == uuid.Nil {
		return nil, domain.NewUnauthenticatedError("missing or invalid tenant identity")
	}
	if agentID == uuid.Nil {
		return nil, domain.NewValidationError("invalid agent id")
	}

	// Verify agent exists and belongs to the authenticated tenant
	if _, err := s.store.GetAgent(ctx, tenantID, agentID); err != nil {
		return nil, err
	}

	// Validate configuration
	if err := domain.ValidateConfiguration(cfg); err != nil {
		return nil, err
	}

	// Get next version number
	nextVersion, err := s.store.GetNextVersionNumber(ctx, agentID)
	if err != nil {
		return nil, err
	}

	version := &domain.AgentVersion{
		ID:            uuid.New(),
		AgentID:       agentID,
		VersionNumber: nextVersion,
		Configuration: cfg,
		Published:     false,
	}

	if err := s.store.CreateVersion(ctx, version); err != nil {
		return nil, err
	}

	return version, nil
}

// PublishVersion transitions a draft agent version to published.
func (s *RegistryService) PublishVersion(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	if tenantID == uuid.Nil {
		return nil, domain.NewUnauthenticatedError("missing or invalid tenant identity")
	}
	if agentID == uuid.Nil || versionID == uuid.Nil {
		return nil, domain.NewValidationError("invalid agent id or version id")
	}

	// Enforce tenant ownership of the agent
	if _, err := s.store.GetAgent(ctx, tenantID, agentID); err != nil {
		return nil, err
	}

	return s.store.PublishVersion(ctx, agentID, versionID)
}

// GetLatestPublishedVersion resolves the latest published version of an agent.
func (s *RegistryService) GetLatestPublishedVersion(ctx context.Context, tenantID, agentID uuid.UUID) (*domain.AgentVersion, error) {
	if tenantID == uuid.Nil {
		return nil, domain.NewUnauthenticatedError("missing or invalid tenant identity")
	}
	if agentID == uuid.Nil {
		return nil, domain.NewValidationError("invalid agent id")
	}

	// Enforce tenant ownership of the agent
	if _, err := s.store.GetAgent(ctx, tenantID, agentID); err != nil {
		return nil, err
	}

	return s.store.GetLatestPublishedVersion(ctx, agentID)
}

// GetVersion retrieves a specific agent version by ID, verifying tenant ownership of the agent.
func (s *RegistryService) GetVersion(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (*domain.AgentVersion, error) {
	if tenantID == uuid.Nil {
		return nil, domain.NewUnauthenticatedError("missing or invalid tenant identity")
	}
	if agentID == uuid.Nil || versionID == uuid.Nil {
		return nil, domain.NewValidationError("invalid agent id or version id")
	}

	// Enforce tenant ownership of the agent
	if _, err := s.store.GetAgent(ctx, tenantID, agentID); err != nil {
		return nil, err
	}

	return s.store.GetVersion(ctx, agentID, versionID)
}

// UpdateDraftVersion updates a draft version's configuration. Rejects modification if version is published.
func (s *RegistryService) UpdateDraftVersion(ctx context.Context, tenantID, agentID, versionID uuid.UUID, cfg domain.Configuration) (*domain.AgentVersion, error) {
	if tenantID == uuid.Nil {
		return nil, domain.NewUnauthenticatedError("missing or invalid tenant identity")
	}

	// Enforce tenant ownership of the agent
	if _, err := s.store.GetAgent(ctx, tenantID, agentID); err != nil {
		return nil, err
	}

	if err := domain.ValidateConfiguration(cfg); err != nil {
		return nil, err
	}

	existing, err := s.store.GetVersion(ctx, agentID, versionID)
	if err != nil {
		return nil, err
	}
	if existing.Published {
		return nil, domain.NewConflictError(domain.CodePublishedImmutable, fmt.Sprintf("cannot modify published agent version %d; published versions are immutable", existing.VersionNumber))
	}

	existing.Configuration = cfg
	if err := s.store.UpdateDraftVersion(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// DeleteDraftVersion deletes a draft version. Rejects deletion if version is published.
func (s *RegistryService) DeleteDraftVersion(ctx context.Context, tenantID, agentID, versionID uuid.UUID) error {
	if tenantID == uuid.Nil {
		return domain.NewUnauthenticatedError("missing or invalid tenant identity")
	}

	// Enforce tenant ownership of the agent
	if _, err := s.store.GetAgent(ctx, tenantID, agentID); err != nil {
		return err
	}

	existing, err := s.store.GetVersion(ctx, agentID, versionID)
	if err != nil {
		return err
	}
	if existing.Published {
		return domain.NewConflictError(domain.CodePublishedImmutable, fmt.Sprintf("cannot delete published agent version %d; published versions are immutable", existing.VersionNumber))
	}

	return s.store.DeleteVersion(ctx, agentID, versionID)
}
