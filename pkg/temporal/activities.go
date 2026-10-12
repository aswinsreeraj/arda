package temporal

import (
	"context"
	"fmt"
	"strings"

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
