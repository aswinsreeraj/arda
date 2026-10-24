package grpc

import (
	"errors"
	"time"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ProtoToExecutionMode converts ardav1.ExecutionMode to domain.ExecutionMode.
func ProtoToExecutionMode(m ardav1.ExecutionMode) domain.ExecutionMode {
	switch m {
	case ardav1.ExecutionMode_EXECUTION_MODE_AUTONOMOUS:
		return domain.ExecutionModeAutonomous
	case ardav1.ExecutionMode_EXECUTION_MODE_SUPERVISED:
		return domain.ExecutionModeSupervised
	case ardav1.ExecutionMode_EXECUTION_MODE_HUMAN_IN_THE_LOOP:
		return domain.ExecutionModeHumanInTheLoop
	case ardav1.ExecutionMode_EXECUTION_MODE_GATED:
		return domain.ExecutionModeGated
	default:
		return domain.ExecutionModeSupervised
	}
}

// ExecutionModeToProto converts domain.ExecutionMode to ardav1.ExecutionMode.
func ExecutionModeToProto(m domain.ExecutionMode) ardav1.ExecutionMode {
	switch m {
	case domain.ExecutionModeAutonomous:
		return ardav1.ExecutionMode_EXECUTION_MODE_AUTONOMOUS
	case domain.ExecutionModeSupervised:
		return ardav1.ExecutionMode_EXECUTION_MODE_SUPERVISED
	case domain.ExecutionModeHumanInTheLoop:
		return ardav1.ExecutionMode_EXECUTION_MODE_HUMAN_IN_THE_LOOP
	case domain.ExecutionModeGated:
		return ardav1.ExecutionMode_EXECUTION_MODE_GATED
	default:
		return ardav1.ExecutionMode_EXECUTION_MODE_UNSPECIFIED
	}
}

// ProtoToDomainConfig converts ardav1.AgentConfiguration to domain.Configuration.
func ProtoToDomainConfig(cfg *ardav1.AgentConfiguration) domain.Configuration {
	if cfg == nil {
		return domain.Configuration{
			PromptTemplate:       "",
			Tools:                []string{},
			ReliabilityThreshold: 0.85,
			ExecutionMode:        domain.ExecutionModeSupervised,
		}
	}
	var guardrails map[string]any
	if cfg.Guardrails != nil {
		guardrails = cfg.Guardrails.AsMap()
	}
	var budgets map[string]any
	if cfg.Budgets != nil {
		budgets = cfg.Budgets.AsMap()
	}
	threshold := cfg.ReliabilityThreshold
	if threshold == 0 {
		threshold = 0.85
	}
	var llmCfg *domain.LLMConfig
	if cfg.LlmConfig != nil {
		var params map[string]any
		if cfg.LlmConfig.Parameters != nil {
			params = cfg.LlmConfig.Parameters.AsMap()
		}
		llmCfg = &domain.LLMConfig{
			Provider:      cfg.LlmConfig.Provider,
			Model:         cfg.LlmConfig.Model,
			CredentialRef: cfg.LlmConfig.CredentialRef,
			Parameters:    params,
		}
	}
	return domain.Configuration{
		PromptTemplate:       cfg.PromptTemplate,
		Tools:                cfg.Tools,
		ReliabilityThreshold: threshold,
		ExecutionMode:        ProtoToExecutionMode(cfg.ExecutionMode),
		Guardrails:           guardrails,
		Budgets:              budgets,
		LLMConfig:            llmCfg,
	}
}

// DomainConfigToProto converts domain.Configuration to ardav1.AgentConfiguration.
func DomainConfigToProto(cfg domain.Configuration) (*ardav1.AgentConfiguration, error) {
	var guardrails *structpb.Struct
	if cfg.Guardrails != nil {
		var err error
		guardrails, err = structpb.NewStruct(cfg.Guardrails)
		if err != nil {
			return nil, err
		}
	}
	var budgets *structpb.Struct
	if cfg.Budgets != nil {
		var err error
		budgets, err = structpb.NewStruct(cfg.Budgets)
		if err != nil {
			return nil, err
		}
	}
	var protoLLM *ardav1.LLMConfiguration
	if cfg.LLMConfig != nil {
		var params *structpb.Struct
		if cfg.LLMConfig.Parameters != nil {
			var err error
			params, err = structpb.NewStruct(cfg.LLMConfig.Parameters)
			if err != nil {
				return nil, err
			}
		}
		protoLLM = &ardav1.LLMConfiguration{
			Provider:      cfg.LLMConfig.Provider,
			Model:         cfg.LLMConfig.Model,
			CredentialRef: cfg.LLMConfig.CredentialRef,
			Parameters:    params,
		}
	}
	tools := cfg.Tools
	if tools == nil {
		tools = []string{}
	}
	return &ardav1.AgentConfiguration{
		PromptTemplate:       cfg.PromptTemplate,
		Tools:                tools,
		ReliabilityThreshold: cfg.ReliabilityThreshold,
		ExecutionMode:        ExecutionModeToProto(cfg.ExecutionMode),
		Guardrails:           guardrails,
		Budgets:              budgets,
		LlmConfig:            protoLLM,
	}, nil
}

// AgentToProto converts domain.Agent to ardav1.Agent.
func AgentToProto(a domain.Agent) *ardav1.Agent {
	return &ardav1.Agent{
		Id:        a.ID.String(),
		TenantId:  a.TenantID.String(),
		Name:      a.Name,
		CreatedAt: timestamppb.New(a.CreatedAt),
		UpdatedAt: timestamppb.New(a.UpdatedAt),
	}
}

// ProtoToAgent converts ardav1.Agent to domain.Agent.
func ProtoToAgent(p *ardav1.Agent) *domain.Agent {
	if p == nil {
		return nil
	}
	agentID, _ := uuid.Parse(p.Id)
	tenantID, _ := uuid.Parse(p.TenantId)
	var createdAt, updatedAt time.Time
	if p.CreatedAt != nil {
		createdAt = p.CreatedAt.AsTime()
	}
	if p.UpdatedAt != nil {
		updatedAt = p.UpdatedAt.AsTime()
	}
	return &domain.Agent{
		ID:        agentID,
		TenantID:  tenantID,
		Name:      p.Name,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}

// VersionToProto converts domain.AgentVersion to ardav1.AgentVersion.
func VersionToProto(v domain.AgentVersion) (*ardav1.AgentVersion, error) {
	cfgProto, err := DomainConfigToProto(v.Configuration)
	if err != nil {
		return nil, err
	}
	protoVer := &ardav1.AgentVersion{
		Id:            v.ID.String(),
		AgentId:       v.AgentID.String(),
		VersionNumber: int32(v.VersionNumber),
		Configuration: cfgProto,
		Published:     v.Published,
		CreatedAt:     timestamppb.New(v.CreatedAt),
		UpdatedAt:     timestamppb.New(v.UpdatedAt),
	}
	if v.PublishedAt != nil {
		protoVer.PublishedAt = timestamppb.New(*v.PublishedAt)
	}
	return protoVer, nil
}

// ProtoToVersion converts ardav1.AgentVersion to domain.AgentVersion.
func ProtoToVersion(p *ardav1.AgentVersion) *domain.AgentVersion {
	if p == nil {
		return nil
	}
	vID, _ := uuid.Parse(p.Id)
	aID, _ := uuid.Parse(p.AgentId)
	var createdAt, updatedAt time.Time
	var publishedAt *time.Time
	if p.CreatedAt != nil {
		createdAt = p.CreatedAt.AsTime()
	}
	if p.UpdatedAt != nil {
		updatedAt = p.UpdatedAt.AsTime()
	}
	if p.PublishedAt != nil {
		t := p.PublishedAt.AsTime()
		publishedAt = &t
	}
	return &domain.AgentVersion{
		ID:            vID,
		AgentID:       aID,
		VersionNumber: int(p.VersionNumber),
		Configuration: ProtoToDomainConfig(p.Configuration),
		Published:     p.Published,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		PublishedAt:   publishedAt,
	}
}

// DetailToProto converts domain.AgentDetail to ardav1.AgentDetail.
func DetailToProto(d domain.AgentDetail) (*ardav1.AgentDetail, error) {
	protoDetail := &ardav1.AgentDetail{
		Agent: AgentToProto(d.Agent),
	}
	if d.LatestPublishedVersion != nil {
		vProto, err := VersionToProto(*d.LatestPublishedVersion)
		if err != nil {
			return nil, err
		}
		protoDetail.LatestPublishedVersion = vProto
	}
	if d.DraftVersion != nil {
		vProto, err := VersionToProto(*d.DraftVersion)
		if err != nil {
			return nil, err
		}
		protoDetail.DraftVersion = vProto
	}
	return protoDetail, nil
}

// ProtoToDetail converts ardav1.AgentDetail to domain.AgentDetail.
func ProtoToDetail(p *ardav1.AgentDetail) *domain.AgentDetail {
	if p == nil {
		return nil
	}
	d := &domain.AgentDetail{}
	if p.Agent != nil {
		a := ProtoToAgent(p.Agent)
		if a != nil {
			d.Agent = *a
		}
	}
	if p.LatestPublishedVersion != nil {
		d.LatestPublishedVersion = ProtoToVersion(p.LatestPublishedVersion)
	}
	if p.DraftVersion != nil {
		d.DraftVersion = ProtoToVersion(p.DraftVersion)
	}
	return d
}

// MapDomainError maps a domain.DomainError to an appropriate gRPC status error.
func MapDomainError(err error) error {
	if err == nil {
		return nil
	}
	var domErr *domain.DomainError
	if errors.As(err, &domErr) {
		switch domErr.Code {
		case domain.CodeNotFound:
			return status.Error(codes.NotFound, domErr.Message)
		case domain.CodeValidationFailed:
			return status.Error(codes.InvalidArgument, domErr.Message)
		case domain.CodeUnauthenticated:
			return status.Error(codes.Unauthenticated, domErr.Message)
		case domain.CodeVersionConflict:
			return status.Error(codes.AlreadyExists, domErr.Message)
		case domain.CodePublishedImmutable:
			return status.Error(codes.FailedPrecondition, domErr.Message)
		case domain.CodeAlreadyPublished:
			return status.Error(codes.AlreadyExists, domErr.Message)
		default:
			return status.Error(codes.Internal, domErr.Message)
		}
	}
	return status.Error(codes.Internal, err.Error())
}

// MapGRPCErrorToHTTP maps a gRPC error to an HTTP status code, machine-readable code, and message.
func MapGRPCErrorToHTTP(err error) (int, string, string) {
	if err == nil {
		return 200, "", ""
	}
	st, ok := status.FromError(err)
	if !ok {
		return 500, "INTERNAL_ERROR", err.Error()
	}
	switch st.Code() {
	case codes.InvalidArgument:
		return 400, "VALIDATION_FAILED", st.Message()
	case codes.NotFound:
		return 404, "NOT_FOUND", st.Message()
	case codes.AlreadyExists:
		return 409, "VERSION_CONFLICT", st.Message()
	case codes.FailedPrecondition:
		return 409, "PUBLISHED_IMMUTABLE", st.Message()
	case codes.Unauthenticated:
		return 401, "UNAUTHENTICATED", st.Message()
	case codes.PermissionDenied:
		return 403, "PERMISSION_DENIED", st.Message()
	case codes.Unavailable:
		return 503, "UPSTREAM_UNAVAILABLE", "registry service upstream is unavailable"
	default:
		return 500, "INTERNAL_ERROR", st.Message()
	}
}
