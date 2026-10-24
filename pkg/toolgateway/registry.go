package toolgateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/aswinsreeraj/arda/pkg/jsonschema"
	"github.com/google/uuid"
)

// ToolRegistry defines the management interface for tool definitions in ARDA.
type ToolRegistry interface {
	RegisterTool(ctx context.Context, tool ToolDefinition) (*ToolDefinition, error)
	UpdateTool(ctx context.Context, tenantID uuid.UUID, id string, updates ToolUpdate) (*ToolDefinition, error)
	GetTool(ctx context.Context, tenantID uuid.UUID, id string) (*ToolDefinition, error)
	GetToolByName(ctx context.Context, tenantID uuid.UUID, name string) (*ToolDefinition, error)
	ListTools(ctx context.Context, tenantID uuid.UUID) ([]ToolDefinition, error)
	SetToolEnabled(ctx context.Context, tenantID uuid.UUID, id string, enabled bool) (*ToolDefinition, error)
	DeleteTool(ctx context.Context, tenantID uuid.UUID, id string) error
}

// ToolUpdate defines the fields that can be updated on an existing tool definition.
type ToolUpdate struct {
	Description         *string             `json:"description,omitempty"`
	Classification      *ToolClassification `json:"classification,omitempty"`
	InputSchema         map[string]any      `json:"input_schema,omitempty"`
	Enabled             *bool               `json:"enabled,omitempty"`
	RequiredPermissions *[]string           `json:"required_permissions,omitempty"`
	HandlerID           *string             `json:"handler_id,omitempty"`
}

// DefaultRegistry is the concrete implementation of ToolRegistry.
type DefaultRegistry struct {
	store Store
}

// NewRegistry constructs a new DefaultRegistry backed by the given Store.
func NewRegistry(store Store) *DefaultRegistry {
	return &DefaultRegistry{store: store}
}

// RegisterTool validates and persists a new tool definition.
func (r *DefaultRegistry) RegisterTool(ctx context.Context, tool ToolDefinition) (*ToolDefinition, error) {
	if tool.ID == "" {
		tool.ID = uuid.New().String()
	}
	tool.Name = strings.TrimSpace(tool.Name)
	if err := tool.Validate(); err != nil {
		return nil, err
	}

	// Validate argument schema using ARDA's supported JSON Schema Draft-07 validator
	if _, err := jsonschema.Compile(tool.InputSchema); err != nil {
		return nil, fmt.Errorf("%w: argument JSON schema is invalid: %v", ErrInvalidToolDefinition, err)
	}

	// Validate required permissions
	for _, perm := range tool.RequiredPermissions {
		if strings.TrimSpace(perm) == "" {
			return nil, fmt.Errorf("%w: required permission cannot be empty", ErrInvalidToolDefinition)
		}
	}

	if tool.Version <= 0 {
		tool.Version = 1
	}
	tool.Enabled = true

	if err := r.store.SaveTool(ctx, &tool); err != nil {
		return nil, fmt.Errorf("failed to save tool definition: %w", err)
	}

	return &tool, nil
}

// UpdateTool updates an existing tool definition, incrementing its version to guarantee immutable contracts.
func (r *DefaultRegistry) UpdateTool(ctx context.Context, tenantID uuid.UUID, id string, updates ToolUpdate) (*ToolDefinition, error) {
	existing, err := r.store.GetTool(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}

	updated := *existing

	if updates.Description != nil {
		updated.Description = *updates.Description
	}
	if updates.Classification != nil {
		c := *updates.Classification
		if c != ClassificationRead && c != ClassificationWrite {
			return nil, fmt.Errorf("%w: invalid classification", ErrInvalidToolDefinition)
		}
		updated.Classification = c
	}
	if updates.InputSchema != nil {
		if _, err := jsonschema.Compile(updates.InputSchema); err != nil {
			return nil, fmt.Errorf("%w: argument JSON schema is invalid: %v", ErrInvalidToolDefinition, err)
		}
		updated.InputSchema = updates.InputSchema
	}
	if updates.Enabled != nil {
		updated.Enabled = *updates.Enabled
	}
	if updates.RequiredPermissions != nil {
		for _, perm := range *updates.RequiredPermissions {
			if strings.TrimSpace(perm) == "" {
				return nil, fmt.Errorf("%w: required permission cannot be empty", ErrInvalidToolDefinition)
			}
		}
		updated.RequiredPermissions = *updates.RequiredPermissions
	}
	if updates.HandlerID != nil {
		h := strings.TrimSpace(*updates.HandlerID)
		if h == "" {
			return nil, fmt.Errorf("%w: handler_id cannot be empty", ErrInvalidToolDefinition)
		}
		updated.HandlerID = h
	}

	// Bump version on definition update to preserve execution immutability
	updated.Version = existing.Version + 1

	if err := r.store.SaveTool(ctx, &updated); err != nil {
		return nil, fmt.Errorf("failed to update tool definition: %w", err)
	}

	return &updated, nil
}

// GetTool retrieves a tool definition by ID within the tenant.
func (r *DefaultRegistry) GetTool(ctx context.Context, tenantID uuid.UUID, id string) (*ToolDefinition, error) {
	return r.store.GetTool(ctx, tenantID, id)
}

// GetToolByName retrieves a tool definition by name within the tenant.
func (r *DefaultRegistry) GetToolByName(ctx context.Context, tenantID uuid.UUID, name string) (*ToolDefinition, error) {
	return r.store.GetToolByName(ctx, tenantID, name)
}

// ListTools lists all tool definitions scoped to the tenant.
func (r *DefaultRegistry) ListTools(ctx context.Context, tenantID uuid.UUID) ([]ToolDefinition, error) {
	return r.store.ListTools(ctx, tenantID)
}

// SetToolEnabled toggles the enabled state of a tool without bumping the version.
func (r *DefaultRegistry) SetToolEnabled(ctx context.Context, tenantID uuid.UUID, id string, enabled bool) (*ToolDefinition, error) {
	tool, err := r.store.GetTool(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	tool.Enabled = enabled
	if err := r.store.SaveTool(ctx, tool); err != nil {
		return nil, fmt.Errorf("failed to update tool enabled status: %w", err)
	}
	return tool, nil
}

// DeleteTool removes a tool definition within the tenant.
func (r *DefaultRegistry) DeleteTool(ctx context.Context, tenantID uuid.UUID, id string) error {
	return r.store.DeleteTool(ctx, tenantID, id)
}
