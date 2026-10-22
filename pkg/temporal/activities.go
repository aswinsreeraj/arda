package temporal

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
	temporalerr "go.temporal.io/sdk/temporal"
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
}

// AgentExecutionActivityResult represents the activity outcome returned to the workflow.
type AgentExecutionActivityResult struct {
	Status     string                       `json:"status"`
	Output     map[string]any               `json:"output,omitempty"`
	Reason     string                       `json:"reason,omitempty"`
	Escalation *agentruntime.EscalationInfo `json:"escalation,omitempty"`
}

// AgentActivities encapsulates activities that invoke an injected AgentRuntime.
type AgentActivities struct {
	runtime agentruntime.AgentRuntime
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

// Runtime returns the injected AgentRuntime.
func (a *AgentActivities) Runtime() agentruntime.AgentRuntime {
	return a.runtime
}

func tryGetActivityLogger(ctx context.Context) log.Logger {
	defer func() {
		_ = recover()
	}()
	return activity.GetLogger(ctx)
}

// AgentExecutionActivity executes the injected AgentRuntime within a Temporal Activity.
// It maps ARDA execution identifiers and forwards context.Context to the runtime.
// Payloads are not logged to prevent leaking credentials or sensitive user data.
func (a *AgentActivities) AgentExecutionActivity(ctx context.Context, input AgentExecutionActivityInput) (AgentExecutionActivityResult, error) {
	if logger := tryGetActivityLogger(ctx); logger != nil {
		logger.Info("Starting AgentExecutionActivity",
			"tenant_id", input.TenantID,
			"agent_id", input.AgentID,
			"agent_version_id", input.AgentVersionID,
		)
	}

	rtInput := agentruntime.ExecutionInput{
		TenantID:       input.TenantID,
		AgentID:        input.AgentID,
		AgentVersionID: input.AgentVersionID,
		Payload:        input.ExecutionInput,
	}

	res, err := a.runtime.Run(ctx, rtInput)
	if err != nil {
		if logger := tryGetActivityLogger(ctx); logger != nil {
			logger.Error("AgentExecutionActivity runtime execution failed", "error", err)
		}
		if errors.Is(err, agentruntime.ErrInvalidInput) {
			return AgentExecutionActivityResult{}, temporalerr.NewNonRetryableApplicationError(
				err.Error(),
				"InvalidInputError",
				err,
			)
		}
		return AgentExecutionActivityResult{}, temporalerr.NewApplicationError(
			fmt.Sprintf("agent runtime execution failed: %v", err),
			"AgentExecutionFailure",
		)
	}

	if logger := tryGetActivityLogger(ctx); logger != nil {
		logger.Info("AgentExecutionActivity completed successfully",
			"tenant_id", input.TenantID,
			"agent_id", input.AgentID,
			"agent_version_id", input.AgentVersionID,
			"status", res.Status,
		)
	}

	return AgentExecutionActivityResult{
		Status:     string(res.Status),
		Output:     res.Output,
		Reason:     res.Reason,
		Escalation: res.Escalation,
	}, nil
}
