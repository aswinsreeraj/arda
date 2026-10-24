package temporal

import (
	"fmt"
	"time"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/google/uuid"
	temporalerr "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	// NoopWorkflowName is the canonical registered name of NoopWorkflow.
	NoopWorkflowName = "NoopWorkflow"

	// NoopActivityName is the canonical registered name of NoopActivity.
	NoopActivityName = "NoopActivity"

	// AgentExecutionWorkflowName is the canonical registered name of AgentExecutionWorkflow.
	AgentExecutionWorkflowName = "AgentExecutionWorkflow"

	// AgentExecutionActivityName is the canonical registered name of AgentExecutionActivity.
	AgentExecutionActivityName = "AgentExecutionActivity"
)

// NoopWorkflowInput represents parameters passed when starting NoopWorkflow.
type NoopWorkflowInput struct {
	Message string `json:"message"`
}

// NoopWorkflowResult represents the completed execution output of NoopWorkflow.
type NoopWorkflowResult struct {
	WorkflowID     string             `json:"workflow_id"`
	RunID          string             `json:"run_id"`
	ActivityResult NoopActivityResult `json:"activity_result"`
}

// NoopWorkflow executes NoopActivity using bounded activity options and a retry policy.
// It strictly utilizes deterministic Temporal workflow APIs.
func NoopWorkflow(ctx workflow.Context, input NoopWorkflowInput) (NoopWorkflowResult, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting NoopWorkflow", "input_message", input.Message)

	// Bounded activity start-to-close timeout and exponential backoff retry policy
	activityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy: &temporalerr.RetryPolicy{
			InitialInterval:        time.Second,
			BackoffCoefficient:     2.0,
			MaximumInterval:        10 * time.Second,
			MaximumAttempts:        3,
			NonRetryableErrorTypes: []string{"SimulatedActivityFailure"},
		},
	}
	ctx = workflow.WithActivityOptions(ctx, activityOptions)

	actInput := NoopActivityInput{
		Message: input.Message,
	}

	var actResult NoopActivityResult
	err := workflow.ExecuteActivity(ctx, NoopActivity, actInput).Get(ctx, &actResult)
	if err != nil {
		logger.Error("NoopActivity failed", "error", err)
		return NoopWorkflowResult{}, fmt.Errorf("activity execution failed: %w", err)
	}

	info := workflow.GetInfo(ctx)
	logger.Info("NoopWorkflow finished successfully", "workflow_id", info.WorkflowExecution.ID)

	return NoopWorkflowResult{
		WorkflowID:     info.WorkflowExecution.ID,
		RunID:          info.WorkflowExecution.RunID,
		ActivityResult: actResult,
	}, nil
}

// AgentExecutionInput represents parameters passed when starting AgentExecutionWorkflow.
type AgentExecutionInput struct {
	TenantID       uuid.UUID      `json:"tenant_id"`
	AgentID        uuid.UUID      `json:"agent_id"`
	AgentVersionID uuid.UUID      `json:"agent_version_id"`
	ExecutionInput map[string]any `json:"execution_input,omitempty"`
}

// AgentExecutionResult represents the output of AgentExecutionWorkflow.
type AgentExecutionResult struct {
	WorkflowID       string                       `json:"workflow_id"`
	RunID            string                       `json:"run_id"`
	TenantID         uuid.UUID                    `json:"tenant_id"`
	AgentID          uuid.UUID                    `json:"agent_id"`
	AgentVersionID   uuid.UUID                    `json:"agent_version_id"`
	Status           string                       `json:"status"`
	Output           map[string]any               `json:"output,omitempty"`
	StructuredOutput any                          `json:"structured_output,omitempty"`
	Reason           string                       `json:"reason,omitempty"`
	Escalation       *agentruntime.EscalationInfo `json:"escalation,omitempty"`
}

// AgentExecutionWorkflow orchestrates the execution of an agent via AgentExecutionActivity.
// It is deterministic, using bounded timeouts and retries, while delegating actual runtime
// execution to activities and keeping orchestration separate from LLM/tool reasoning.
func AgentExecutionWorkflow(ctx workflow.Context, input AgentExecutionInput) (AgentExecutionResult, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting AgentExecutionWorkflow",
		"tenant_id", input.TenantID,
		"agent_id", input.AgentID,
		"agent_version_id", input.AgentVersionID,
	)

	// Bounded activity start-to-close timeout and conservative exponential backoff retry policy
	activityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporalerr.RetryPolicy{
			InitialInterval:        time.Second,
			BackoffCoefficient:     2.0,
			MaximumInterval:        10 * time.Second,
			MaximumAttempts:        3,
			NonRetryableErrorTypes: []string{"InvalidInputError", "InvalidResultError", "PermanentExecutionError"},
		},
	}
	ctx = workflow.WithActivityOptions(ctx, activityOptions)

	actInput := AgentExecutionActivityInput{
		TenantID:       input.TenantID,
		AgentID:        input.AgentID,
		AgentVersionID: input.AgentVersionID,
		ExecutionInput: input.ExecutionInput,
	}

	var actResult AgentExecutionActivityResult
	var agentActivities *AgentActivities
	err := workflow.ExecuteActivity(ctx, agentActivities.AgentExecutionActivity, actInput).Get(ctx, &actResult)
	if err != nil {
		logger.Error("AgentExecutionActivity failed", "error", err)
		return AgentExecutionResult{
			WorkflowID:     workflow.GetInfo(ctx).WorkflowExecution.ID,
			RunID:          workflow.GetInfo(ctx).WorkflowExecution.RunID,
			TenantID:       input.TenantID,
			AgentID:        input.AgentID,
			AgentVersionID: input.AgentVersionID,
			Status:         "FAILED",
		}, fmt.Errorf("agent activity execution failed: %w", err)
	}

	info := workflow.GetInfo(ctx)
	logger.Info("AgentExecutionWorkflow completed successfully",
		"workflow_id", info.WorkflowExecution.ID,
		"status", actResult.Status,
	)

	return AgentExecutionResult{
		WorkflowID:       info.WorkflowExecution.ID,
		RunID:            info.WorkflowExecution.RunID,
		TenantID:         input.TenantID,
		AgentID:          input.AgentID,
		AgentVersionID:   input.AgentVersionID,
		Status:           actResult.Status,
		Output:           actResult.Output,
		StructuredOutput: actResult.StructuredOutput,
		Reason:           actResult.Reason,
		Escalation:       actResult.Escalation,
	}, nil
}
