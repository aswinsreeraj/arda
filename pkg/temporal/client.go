package temporal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	namespacepb "go.temporal.io/api/namespace/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Client wraps the Temporal SDK client for dispatching and inspecting ARDA workflows.
type Client struct {
	client client.Client
	config Config
}

// NewClient initializes a new connection to the Temporal frontend.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Address == "" {
		cfg.Address = DefaultAddress
	}
	if cfg.Namespace == "" {
		cfg.Namespace = DefaultNamespace
	}
	if cfg.TaskQueue == "" {
		cfg.TaskQueue = DefaultTaskQueue
	}

	c, err := client.Dial(client.Options{
		HostPort:  cfg.Address,
		Namespace: cfg.Namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to temporal server at %s: %w", cfg.Address, err)
	}

	return &Client{
		client: c,
		config: cfg,
	}, nil
}

// StartNoopWorkflow asynchronously dispatches NoopWorkflow with an explicit workflow ID.
func (c *Client) StartNoopWorkflow(ctx context.Context, workflowID string, input NoopWorkflowInput) (client.WorkflowRun, error) {
	wID := strings.TrimSpace(workflowID)
	if wID == "" {
		return nil, fmt.Errorf("workflow ID cannot be empty")
	}

	opts := client.StartWorkflowOptions{
		ID:        wID,
		TaskQueue: c.config.TaskQueue,
	}

	run, err := c.client.ExecuteWorkflow(ctx, opts, NoopWorkflow, input)
	if err != nil {
		return nil, fmt.Errorf("failed to start workflow %s: %w", wID, err)
	}

	return run, nil
}

// ExecuteNoopWorkflow starts NoopWorkflow, waits synchronously for its completion, and returns the result.
func (c *Client) ExecuteNoopWorkflow(ctx context.Context, workflowID string, input NoopWorkflowInput) (NoopWorkflowResult, error) {
	run, err := c.StartNoopWorkflow(ctx, workflowID, input)
	if err != nil {
		return NoopWorkflowResult{}, err
	}

	var result NoopWorkflowResult
	if err := run.Get(ctx, &result); err != nil {
		return NoopWorkflowResult{}, fmt.Errorf("workflow execution failed for %s: %w", workflowID, err)
	}

	return result, nil
}

// StartAgentExecutionWorkflow asynchronously dispatches AgentExecutionWorkflow with an explicit workflow ID.
func (c *Client) StartAgentExecutionWorkflow(ctx context.Context, workflowID string, input AgentExecutionInput) (client.WorkflowRun, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("temporal client is not initialized")
	}
	wID := strings.TrimSpace(workflowID)
	if wID == "" {
		return nil, fmt.Errorf("workflow ID cannot be empty")
	}

	opts := client.StartWorkflowOptions{
		ID:        wID,
		TaskQueue: c.config.TaskQueue,
		Memo: map[string]interface{}{
			"tenant_id": input.TenantID.String(),
			"agent_id":  input.AgentID.String(),
		},
	}
	if input.TenantID != uuid.Nil {
		opts.SearchAttributes = map[string]interface{}{
			"CustomKeywordField": input.TenantID.String(),
		}
	}

	run, err := c.client.ExecuteWorkflow(ctx, opts, AgentExecutionWorkflow, input)
	if err != nil {
		return nil, fmt.Errorf("failed to start agent execution workflow %s: %w", wID, err)
	}

	return run, nil
}

// ExecuteAgentWorkflow starts AgentExecutionWorkflow, waits synchronously for its completion, and returns the result.
func (c *Client) ExecuteAgentWorkflow(ctx context.Context, workflowID string, input AgentExecutionInput) (AgentExecutionResult, error) {
	run, err := c.StartAgentExecutionWorkflow(ctx, workflowID, input)
	if err != nil {
		return AgentExecutionResult{}, err
	}

	var result AgentExecutionResult
	if err := run.Get(ctx, &result); err != nil {
		return AgentExecutionResult{}, fmt.Errorf("workflow execution failed for %s: %w", workflowID, err)
	}

	return result, nil
}

// UpdateRetention updates the Temporal namespace's workflow execution history retention TTL in days.
func (c *Client) UpdateRetention(ctx context.Context, days int) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("temporal client is not initialized")
	}
	if days < 1 || days > 365 {
		return fmt.Errorf("retention days must be between 1 and 365")
	}
	ns := c.config.Namespace
	if ns == "" {
		ns = DefaultNamespace
	}
	_, err := c.client.WorkflowService().UpdateNamespace(ctx, &workflowservice.UpdateNamespaceRequest{
		Namespace: ns,
		Config: &namespacepb.NamespaceConfig{
			WorkflowExecutionRetentionTtl: durationpb.New(time.Duration(days) * 24 * time.Hour),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to update temporal namespace %q retention to %dd: %w", ns, days, err)
	}
	return nil
}

// Close closes the underlying Temporal SDK client.
func (c *Client) Close() {
	if c.client != nil {
		c.client.Close()
	}
}

// RawClient returns the underlying temporal client.Client for advanced use cases.
func (c *Client) RawClient() client.Client {
	return c.client
}

// Config returns the client's configuration.
func (c *Client) Config() Config {
	return c.config
}
