package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/google/uuid"
)

func main() {
	envCfg := temporal.ConfigFromEnv()

	addressFlag := flag.String("address", envCfg.Address, "Temporal server host:port")
	namespaceFlag := flag.String("namespace", envCfg.Namespace, "Temporal namespace")
	taskQueueFlag := flag.String("task-queue", envCfg.TaskQueue, "Temporal task queue name")
	workflowIDFlag := flag.String("workflow-id", "", "Explicit workflow ID (defaults to generated ID)")
	messageFlag := flag.String("message", "ARDA v1 verification run", "Payload message for NoopWorkflow")
	timeoutFlag := flag.Duration("timeout", 30*time.Second, "Maximum duration to wait for workflow completion")
	flag.Parse()

	workflowID := *workflowIDFlag
	if workflowID == "" {
		workflowID = fmt.Sprintf("arda-noop-%s", uuid.New().String()[:8])
	}

	cfg := temporal.Config{
		Address:   *addressFlag,
		Namespace: *namespaceFlag,
		TaskQueue: *taskQueueFlag,
	}

	client, err := temporal.NewClient(cfg)
	if err != nil {
		log.Fatalf("FATAL: Failed to connect to Temporal: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	log.Printf("Executing NoopWorkflow...")
	log.Printf("  Target Server: %s (namespace: %s)", cfg.Address, cfg.Namespace)
	log.Printf("  Task Queue:    %s", cfg.TaskQueue)
	log.Printf("  Workflow ID:   %s", workflowID)
	log.Printf("  Payload:       %q", *messageFlag)

	result, err := client.ExecuteNoopWorkflow(ctx, workflowID, temporal.NoopWorkflowInput{
		Message: *messageFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nERROR: Workflow execution failed:\n  %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n=== Workflow Execution Result ===\n")
	fmt.Printf("Workflow ID:     %s\n", result.WorkflowID)
	fmt.Printf("Run ID:          %s\n", result.RunID)
	fmt.Printf("Status:          %s\n", result.ActivityResult.Status)
	fmt.Printf("Activity Result: %s\n", result.ActivityResult.Message)
	fmt.Printf("Executed By:     %s\n", result.ActivityResult.ExecutedBy)
	fmt.Printf("=================================\n")
}
