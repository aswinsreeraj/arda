package agentruntime

import (
	"context"
	"errors"
	"fmt"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// RegistryConfigProviderOption configures optional behavior on a RegistryConfigProvider.
type RegistryConfigProviderOption func(*RegistryConfigProvider)

// WithRequirePublished configures whether only published versions are eligible for execution.
func WithRequirePublished(require bool) RegistryConfigProviderOption {
	return func(p *RegistryConfigProvider) {
		p.requirePublished = require
	}
}

// RegistryConfigProvider adapts an ardav1.AgentRegistryServiceClient to the AgentConfigProvider interface.
// It retrieves immutable agent version configurations from the Agent Registry over gRPC,
// strictly scopes lookups by tenant, agent, and exact version ID, verifies ownership, and
// enforces published-version immutability for execution.
type RegistryConfigProvider struct {
	client           ardav1.AgentRegistryServiceClient
	requirePublished bool
}

// NewRegistryConfigProvider creates a new RegistryConfigProvider instance.
func NewRegistryConfigProvider(client ardav1.AgentRegistryServiceClient, opts ...RegistryConfigProviderOption) (*RegistryConfigProvider, error) {
	if client == nil {
		return nil, errors.New("registry config provider: registry client cannot be nil")
	}

	p := &RegistryConfigProvider{
		client:           client,
		requirePublished: true, // By default, only published immutable versions are eligible for execution
	}

	for _, opt := range opts {
		opt(p)
	}

	return p, nil
}

// GetAgentConfig retrieves the immutable domain.Configuration for an exact pinned agent version.
// It enforces tenant boundary isolation, verifies agent ownership, checks published eligibility,
// and maps missing versions and authorization failures into standardized domain errors.
func (p *RegistryConfigProvider) GetAgentConfig(ctx context.Context, tenantID, agentID, versionID uuid.UUID) (domain.Configuration, error) {
	if err := ctx.Err(); err != nil {
		return domain.Configuration{}, err
	}

	if p == nil || p.client == nil {
		return domain.Configuration{}, ErrNilConfigProvider
	}

	if tenantID == uuid.Nil {
		return domain.Configuration{}, fmt.Errorf("%w: tenant_id is required", ErrInvalidInput)
	}
	if agentID == uuid.Nil {
		return domain.Configuration{}, fmt.Errorf("%w: agent_id is required", ErrInvalidInput)
	}
	if versionID == uuid.Nil {
		return domain.Configuration{}, fmt.Errorf("%w: version_id is required", ErrInvalidInput)
	}

	// Attach trusted internal tenant identity to gRPC outgoing metadata
	md := metadata.New(map[string]string{
		registrygrpc.MetadataTenantID: tenantID.String(),
	})
	outCtx := metadata.NewOutgoingContext(ctx, md)

	req := &ardav1.GetAgentVersionRequest{
		TenantId:  tenantID.String(),
		AgentId:   agentID.String(),
		VersionId: versionID.String(),
	}

	resp, err := p.client.GetAgentVersion(outCtx, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return domain.Configuration{}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return domain.Configuration{}, context.DeadlineExceeded
		}

		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				return domain.Configuration{}, fmt.Errorf("%w: version %s for agent %s in tenant %s", ErrConfigurationNotFound, versionID, agentID, tenantID)
			case codes.Unauthenticated, codes.PermissionDenied:
				return domain.Configuration{}, fmt.Errorf("%w: registry access denied: %s", ErrPermanent, st.Message())
			case codes.InvalidArgument:
				return domain.Configuration{}, fmt.Errorf("%w: invalid registry request: %s", ErrPermanent, st.Message())
			}
		}

		return domain.Configuration{}, fmt.Errorf("%w: registry query failed: %v", ErrTransient, err)
	}

	if resp == nil || resp.GetVersion() == nil {
		return domain.Configuration{}, fmt.Errorf("%w: version %s for agent %s in tenant %s", ErrConfigurationNotFound, versionID, agentID, tenantID)
	}

	protoVer := resp.GetVersion()

	// Verify agent ownership integrity
	if protoVer.GetAgentId() != agentID.String() {
		return domain.Configuration{}, fmt.Errorf("%w: version %s does not belong to agent %s", ErrInvalidConfiguration, versionID, agentID)
	}
	if protoVer.GetId() != versionID.String() {
		return domain.Configuration{}, fmt.Errorf("%w: returned version id %s does not match requested %s", ErrInvalidConfiguration, protoVer.GetId(), versionID)
	}

	// Verify execution eligibility: draft versions are mutable and ineligible for production execution
	if p.requirePublished && !protoVer.GetPublished() {
		return domain.Configuration{}, fmt.Errorf("%w: version %s is not published (draft versions are not eligible for execution)", ErrInvalidConfiguration, versionID)
	}

	// Convert protobuf configuration to domain.Configuration
	cfg := registrygrpc.ProtoToDomainConfig(protoVer.GetConfiguration())

	return cfg, nil
}
