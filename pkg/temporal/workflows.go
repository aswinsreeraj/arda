package temporal

import (
	"fmt"
	"time"

	temporalerr "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	// NoopWorkflowName is the canonical registered name of NoopWorkflow.
	NoopWorkflowName = "NoopWorkflow"

	// NoopActivityName is the canonical registered name of NoopActivity.
	NoopActivityName = "NoopActivity"
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
			BackoffCoefficient:    2.0,
			MaximumInterval:       10 * time.Second,
			MaximumAttempts:       3,
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
