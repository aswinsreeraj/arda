package temporal

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
	temporalerr "go.temporal.io/sdk/temporal"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"
)

// NoopActivityInput represents the input payload passed to NoopActivity.
type NoopActivityInput struct {
	Message string `json:"message"`
}

// NoopActivityResult represents the deterministic output returned by NoopActivity.
type NoopActivityResult struct {
	Status     string `json:"status"`
	Message    string `json:"message"`
	ExecutedBy string `json:"executed_by"`
}

// NoopActivity is an activity that returns a deterministic result indicating successful execution.
// If input.Message is "FAIL_SIMULATION", it returns an application error to allow testing failure propagation.
func NoopActivity(ctx context.Context, input NoopActivityInput) (NoopActivityResult, error) {
	if strings.TrimSpace(input.Message) == "FAIL_SIMULATION" {
		return NoopActivityResult{}, temporalerr.NewApplicationError(
			"simulated activity execution failure",
			"SimulatedActivityFailure",
		)
	}

	msg := strings.TrimSpace(input.Message)
	if msg == "" {
		msg = "noop activity executed successfully"
	}

	return NoopActivityResult{
		Status:     "SUCCESS",
		Message:    fmt.Sprintf("processed: %s", msg),
		ExecutedBy: "NoopActivity",
	}, nil
}

// AgentExecutionActivityInput represents parameters passed to AgentExecutionActivity.
type AgentExecutionActivityInput struct {
	TenantID       uuid.UUID      `json:"tenant_id"`
	AgentID        uuid.UUID      `json:"agent_id"`
	AgentVersionID uuid.UUID      `json:"agent_version_id"`
	ExecutionInput map[string]any `json:"execution_input,omitempty"`
	ExecutionID    string         `json:"execution_id,omitempty"`
	Mode           string         `json:"mode,omitempty"`
	ModeType       string         `json:"mode_type,omitempty"`
	ExecutionMode  string         `json:"execution_mode,omitempty"`
}

// AgentExecutionActivityResult represents the activity outcome returned to the workflow.
type AgentExecutionActivityResult struct {
	Status           string                       `json:"status"`
	Output           map[string]any               `json:"output,omitempty"`
	StructuredOutput any                          `json:"structured_output,omitempty"`
	Reason           string                       `json:"reason,omitempty"`
	Escalation       *agentruntime.EscalationInfo `json:"escalation,omitempty"`
}

// AgentActivities encapsulates activities that invoke an injected AgentRuntime.
type AgentActivities struct {
	runtime      agentruntime.AgentRuntime
	reviewClient ardav1.ReviewServiceClient
	logger       log.Logger
}

// NewAgentActivities constructs an AgentActivities instance with the provided AgentRuntime.
// If runtime is nil, NewDefaultRuntime() is used to provide a deterministic mock fallback.
func NewAgentActivities(runtime agentruntime.AgentRuntime) *AgentActivities {
	if runtime == nil {
		runtime = agentruntime.NewDefaultRuntime()
	}
	return &AgentActivities{
		runtime: runtime,
	}
}

// SetReviewClient injects a ReviewServiceClient to automatically create review cases on escalation.
func (a *AgentActivities) SetReviewClient(rc ardav1.ReviewServiceClient) {
	a.reviewClient = rc
}

// SetLogger configures an explicit logger on AgentActivities (useful for testing or custom logging pipelines).
func (a *AgentActivities) SetLogger(l log.Logger) {
	a.logger = l
}

// Runtime returns the injected AgentRuntime.
func (a *AgentActivities) Runtime() agentruntime.AgentRuntime {
	return a.runtime
}

type noopLogger struct{}

func (noopLogger) Debug(string, ...any) {}
func (noopLogger) Info(string, ...any)  {}
func (noopLogger) Warn(string, ...any)  {}
func (noopLogger) Error(string, ...any) {}

func (a *AgentActivities) getLogger(ctx context.Context) (l log.Logger) {
	if a.logger != nil {
		return a.logger
	}
	defer func() {
		if r := recover(); r != nil {
			l = noopLogger{}
		}
	}()
	return activity.GetLogger(ctx)
}

// AgentExecutionActivity executes the injected AgentRuntime within a Temporal Activity.
// It maps ARDA execution identifiers and forwards context.Context to the runtime.
// Raw errors and user payloads are not logged or leaked in public error messages to prevent credential leakage.
func (a *AgentActivities) AgentExecutionActivity(ctx context.Context, input AgentExecutionActivityInput) (AgentExecutionActivityResult, error) {
	logger := a.getLogger(ctx)
	logger.Info("Starting AgentExecutionActivity",
		"tenant_id", input.TenantID,
		"agent_id", input.AgentID,
		"agent_version_id", input.AgentVersionID,
	)

	mode := input.Mode
	if mode == "" {
		mode = "async"
	}
	modeType := input.ModeType
	if modeType == "" {
		modeType = "durable"
	}
	execMode := input.ExecutionMode
	if execMode == "" {
		execMode = "autonomous"
	}

	rtInput := agentruntime.ExecutionInput{
		TenantID:       input.TenantID,
		AgentID:        input.AgentID,
		AgentVersionID: input.AgentVersionID,
		ExecutionID:    input.ExecutionID,
		Mode:           mode,
		ModeType:       modeType,
		ExecutionMode:  execMode,
		Payload:        input.ExecutionInput,
	}

	res, err := a.runtime.Run(ctx, rtInput)
	if err != nil {
		// Context cancellation / deadline exceeded: preserve cancellation semantics
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logger.Info("AgentExecutionActivity canceled",
				"tenant_id", input.TenantID,
				"agent_id", input.AgentID,
				"agent_version_id", input.AgentVersionID,
			)
			if ctx.Err() != nil {
				return AgentExecutionActivityResult{}, ctx.Err()
			}
			return AgentExecutionActivityResult{}, err
		}

		category := agentruntime.ClassifyError(err)

		// Log sanitized error category without sensitive provider or payload details
		logger.Error("AgentExecutionActivity runtime execution failed",
			"error_category", string(category),
			"error", err.Error(),
			"tenant_id", input.TenantID,
			"agent_id", input.AgentID,
			"agent_version_id", input.AgentVersionID,
		)

		switch category {
		case agentruntime.CategoryInvalidInput:
			return AgentExecutionActivityResult{}, temporalerr.NewNonRetryableApplicationError(
				"invalid execution input",
				"InvalidInputError",
				nil,
			)
		case agentruntime.CategoryInvalidResult:
			return AgentExecutionActivityResult{}, temporalerr.NewNonRetryableApplicationError(
				"invalid runtime execution result",
				"InvalidResultError",
				nil,
			)
		case agentruntime.CategoryPermanent:
			return AgentExecutionActivityResult{}, temporalerr.NewNonRetryableApplicationError(
				"permanent runtime execution failure",
				"PermanentExecutionError",
				nil,
			)
		case agentruntime.CategoryTransient:
			fallthrough
		default:
			return AgentExecutionActivityResult{}, temporalerr.NewApplicationError(
				"agent runtime execution failed due to a transient error",
				"AgentExecutionTransientFailure",
			)
		}
	}

	// Validate result contract at activity boundary before returning
	if err := res.Validate(); err != nil {
		logger.Error("AgentExecutionActivity received invalid result from runtime",
			"error_category", string(agentruntime.CategoryInvalidResult),
			"tenant_id", input.TenantID,
			"agent_id", input.AgentID,
			"agent_version_id", input.AgentVersionID,
		)
		return AgentExecutionActivityResult{}, temporalerr.NewNonRetryableApplicationError(
			"invalid runtime execution result",
			"InvalidResultError",
			nil,
		)
	}

	if (res.Status == agentruntime.StatusEscalated || res.IsEscalation()) && a.reviewClient != nil {
		proposed := "APPROVE"
		if p, ok := res.Output["proposed_decision"].(string); ok && p != "" {
			proposed = p
		}
		conf := 0.0
		if c, ok := res.Output["confidence"].(float64); ok {
			conf = c
		}
		var reasons []string
		if res.Escalation != nil && res.Escalation.Reason != "" {
			reasons = append(reasons, res.Escalation.Reason)
		}
		var origInput *structpb.Struct
		if input.ExecutionInput != nil {
			origInput, _ = structpb.NewStruct(input.ExecutionInput)
		}

		md := metadata.New(map[string]string{
			registrygrpc.MetadataTenantID: input.TenantID.String(),
		})
		outCtx := metadata.NewOutgoingContext(ctx, md)

		createReq := &ardav1.CreateReviewCaseRequest{
			TenantId:           input.TenantID.String(),
			ExecutionId:        input.ExecutionID,
			AgentId:            input.AgentID.String(),
			AgentName:          fmt.Sprintf("Agent-%s", input.AgentID.String()[:8]),
			AgentVersionId:     input.AgentVersionID.String(),
			ExecutionMode:      "durable",
			ProposedDecision:   proposed,
			Confidence:         conf,
			EscalationReasons:  reasons,
			FrozenEvidence: &ardav1.FrozenEvidence{
				OriginalInput:     origInput,
				AgentReasoning:    res.Reason,
				ProposedDecision:  proposed,
				Confidence:        conf,
				EscalationReasons: reasons,
			},
		}

		if created, err := a.reviewClient.CreateReviewCase(outCtx, createReq); err == nil && created.GetCase() != nil {
			if res.Escalation == nil {
				res.Escalation = &agentruntime.EscalationInfo{}
			}
			res.Escalation.CaseID = created.GetCase().GetId()
			if res.Output == nil {
				res.Output = make(map[string]any)
			}
			res.Output["case_id"] = created.GetCase().GetId()
		}
	}

	logger.Info("AgentExecutionActivity completed successfully",
		"tenant_id", input.TenantID,
		"agent_id", input.AgentID,
		"agent_version_id", input.AgentVersionID,
		"status", string(res.Status),
	)

	return AgentExecutionActivityResult{
		Status:           string(res.Status),
		Output:           res.Output,
		StructuredOutput: res.StructuredOutput,
		Reason:           res.Reason,
		Escalation:       res.Escalation,
	}, nil
}
