package grpc

import (
	"errors"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"github.com/aswinsreeraj/arda/services/review/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DomainFrozenEvidenceToProto converts domain.FrozenEvidence into protobuf message.
func DomainFrozenEvidenceToProto(fe domain.FrozenEvidence) (*ardav1.FrozenEvidence, error) {
	var origInput *structpb.Struct
	if fe.OriginalInput != nil {
		s, err := structpb.NewStruct(fe.OriginalInput)
		if err != nil {
			return nil, err
		}
		origInput = s
	}

	neighbors := make([]*ardav1.ReferenceNeighbor, 0, len(fe.Neighbors))
	for _, n := range fe.Neighbors {
		neighbors = append(neighbors, &ardav1.ReferenceNeighbor{
			ReferenceExampleId: n.ReferenceExampleID,
			Label:              n.Label,
			Similarity:         n.Similarity,
		})
	}

	toolCalls := make([]*ardav1.ToolCallEvidence, 0, len(fe.ToolCalls))
	for _, tc := range fe.ToolCalls {
		var args *structpb.Struct
		if tc.Arguments != nil {
			args, _ = structpb.NewStruct(tc.Arguments)
		}
		var res *structpb.Struct
		if tc.Result != nil {
			res, _ = structpb.NewStruct(tc.Result)
		}
		toolCalls = append(toolCalls, &ardav1.ToolCallEvidence{
			ToolId:     tc.ToolID,
			ToolName:   tc.ToolName,
			Arguments:  args,
			Result:     res,
			Output:     tc.Output,
			Error:      tc.Error,
			Allowed:    tc.Allowed,
			DenialCode: tc.DenialCode,
			DurationMs: tc.DurationMs,
		})
	}

	return &ardav1.FrozenEvidence{
		OriginalInput:        origInput,
		AgentReasoning:       fe.AgentReasoning,
		ProposedDecision:     fe.ProposedDecision,
		Confidence:           fe.Confidence,
		ReliabilityThreshold: fe.ReliabilityThreshold,
		EscalationReasons:    fe.EscalationReasons,
		Neighbors:            neighbors,
		ToolCalls:            toolCalls,
		Metadata:             fe.Metadata,
	}, nil
}

// ProtoToDomainFrozenEvidence converts ardav1.FrozenEvidence into domain type.
func ProtoToDomainFrozenEvidence(p *ardav1.FrozenEvidence) domain.FrozenEvidence {
	if p == nil {
		return domain.FrozenEvidence{}
	}

	var origInput map[string]any
	if p.OriginalInput != nil {
		origInput = p.OriginalInput.AsMap()
	}

	neighbors := make([]domain.ReferenceNeighbor, 0, len(p.Neighbors))
	for _, n := range p.Neighbors {
		neighbors = append(neighbors, domain.ReferenceNeighbor{
			ReferenceExampleID: n.ReferenceExampleId,
			Label:              n.Label,
			Similarity:         n.Similarity,
		})
	}

	toolCalls := make([]domain.ToolCallEvidence, 0, len(p.ToolCalls))
	for _, tc := range p.ToolCalls {
		var args map[string]any
		if tc.Arguments != nil {
			args = tc.Arguments.AsMap()
		}
		var res map[string]any
		if tc.Result != nil {
			res = tc.Result.AsMap()
		}
		toolCalls = append(toolCalls, domain.ToolCallEvidence{
			ToolID:     tc.ToolId,
			ToolName:   tc.ToolName,
			Arguments:  args,
			Result:     res,
			Output:     tc.Output,
			Error:      tc.Error,
			Allowed:    tc.Allowed,
			DenialCode: tc.DenialCode,
			DurationMs: tc.DurationMs,
		})
	}

	return domain.FrozenEvidence{
		OriginalInput:        origInput,
		AgentReasoning:       p.AgentReasoning,
		ProposedDecision:     p.ProposedDecision,
		Confidence:           p.Confidence,
		ReliabilityThreshold: p.ReliabilityThreshold,
		EscalationReasons:    p.EscalationReasons,
		Neighbors:            neighbors,
		ToolCalls:            toolCalls,
		Metadata:             p.Metadata,
	}
}

// DomainCaseToProto converts domain.ReviewCase to ardav1.ReviewCase.
func DomainCaseToProto(rc domain.ReviewCase) (*ardav1.ReviewCase, error) {
	frozenProto, err := DomainFrozenEvidenceToProto(rc.FrozenEvidence)
	if err != nil {
		return nil, err
	}

	var resolvedAtProto *timestamppb.Timestamp
	if rc.ResolvedAt != nil {
		resolvedAtProto = timestamppb.New(*rc.ResolvedAt)
	}

	return &ardav1.ReviewCase{
		Id:                   rc.ID,
		TenantId:             rc.TenantID.String(),
		ExecutionId:          rc.ExecutionID,
		AgentId:              rc.AgentID.String(),
		AgentName:            rc.AgentName,
		AgentVersionId:       rc.AgentVersionID.String(),
		AgentVersionNumber:   int32(rc.AgentVersionNumber),
		ExecutionMode:        rc.ExecutionMode,
		Status:               string(rc.Status),
		ProposedDecision:     rc.ProposedDecision,
		Confidence:           rc.Confidence,
		ReliabilityThreshold: rc.ReliabilityThreshold,
		EscalationReasons:    rc.EscalationReasons,
		FrozenEvidence:       frozenProto,
		Decision:             string(rc.Decision),
		FinalDecision:        rc.FinalDecision,
		ResolutionReason:     rc.ResolutionReason,
		ReviewerId:           rc.ReviewerID,
		AuditRecordId:        rc.AuditRecordID,
		ResolvedAt:           resolvedAtProto,
		CreatedAt:            timestamppb.New(rc.CreatedAt),
		UpdatedAt:            timestamppb.New(rc.UpdatedAt),
	}, nil
}

// DomainSummaryToProto converts domain.ReviewCaseSummary to ardav1.ReviewCaseSummary.
func DomainSummaryToProto(s domain.ReviewCaseSummary) *ardav1.ReviewCaseSummary {
	return &ardav1.ReviewCaseSummary{
		CaseId:            s.CaseID,
		ExecutionId:       s.ExecutionID,
		AgentId:           s.AgentID.String(),
		AgentName:         s.AgentName,
		ProposedDecision:  s.ProposedDecision,
		Confidence:        s.Confidence,
		EscalationReasons: s.EscalationReasons,
		Status:            string(s.Status),
		CreatedAt:         timestamppb.New(s.CreatedAt),
	}
}

// MapReviewDomainError maps domain errors to appropriate gRPC status errors.
func MapReviewDomainError(err error) error {
	if err == nil {
		return nil
	}
	var revErr *domain.ReviewError
	if errors.As(err, &revErr) {
		switch revErr.Code {
		case domain.CodeNotFound:
			return status.Error(codes.NotFound, revErr.Message)
		case domain.CodeValidationFailed:
			return status.Error(codes.InvalidArgument, revErr.Message)
		case domain.CodeConflict:
			return status.Error(codes.AlreadyExists, revErr.Message)
		case domain.CodeForbidden:
			return status.Error(codes.PermissionDenied, revErr.Message)
		case domain.CodeUnauthenticated:
			return status.Error(codes.Unauthenticated, revErr.Message)
		default:
			return status.Error(codes.Internal, revErr.Message)
		}
	}
	return status.Error(codes.Internal, err.Error())
}

// MapReviewGRPCErrorToHTTP maps a gRPC error to HTTP status code, machine-readable code, and message.
func MapReviewGRPCErrorToHTTP(err error) (int, string, string) {
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
		return 409, "CASE_ALREADY_RESOLVED", st.Message()
	case codes.FailedPrecondition:
		return 409, "CONFLICT", st.Message()
	case codes.Unauthenticated:
		return 401, "UNAUTHENTICATED", st.Message()
	case codes.PermissionDenied:
		return 403, "PERMISSION_DENIED", st.Message()
	case codes.Unavailable:
		return 503, "UPSTREAM_UNAVAILABLE", "review service upstream is unavailable"
	default:
		return 500, "INTERNAL_ERROR", st.Message()
	}
}
