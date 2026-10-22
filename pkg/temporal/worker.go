package temporal

import (
	"fmt"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// WorkerOption defines functional options for configuring a Worker.
type WorkerOption func(*workerOptions)

type workerOptions struct {
	runtime agentruntime.AgentRuntime
}

// WithAgentRuntime specifies the AgentRuntime instance to inject into the worker's activities.
func WithAgentRuntime(rt agentruntime.AgentRuntime) WorkerOption {
	return func(o *workerOptions) {
		o.runtime = rt
	}
}

// Worker wraps a Temporal worker that registers and executes ARDA workflows and activities.
type Worker struct {
	client  client.Client
	worker  worker.Worker
	config  Config
	runtime agentruntime.AgentRuntime
}

// NewWorker initializes a new Temporal worker listening on the configured task queue.
// If an existing client.Client is provided, it is reused; otherwise a new client connection is established.
// Optional WorkerOptions may be provided. If no runtime is specified, a default deterministic
// mock runtime is initialized.
func NewWorker(cfg Config, c client.Client, opts ...WorkerOption) (*Worker, error) {
	if cfg.Address == "" {
		cfg.Address = DefaultAddress
	}
	if cfg.Namespace == "" {
		cfg.Namespace = DefaultNamespace
	}
	if cfg.TaskQueue == "" {
		cfg.TaskQueue = DefaultTaskQueue
	}

	optsStruct := workerOptions{}
	for _, opt := range opts {
		opt(&optsStruct)
	}

	rt := optsStruct.runtime
	if rt == nil {
		rt = agentruntime.NewDefaultRuntime()
	}

	agentActivities := NewAgentActivities(rt)

	var ownsClient bool
	if c == nil {
		var err error
		c, err = client.Dial(client.Options{
			HostPort:  cfg.Address,
			Namespace: cfg.Namespace,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to connect to temporal server at %s: %w", cfg.Address, err)
		}
		ownsClient = true
	}

	w := worker.New(c, cfg.TaskQueue, worker.Options{})

	// Register workflows and activities
	w.RegisterWorkflow(NoopWorkflow)
	w.RegisterWorkflow(AgentExecutionWorkflow)
	w.RegisterActivity(NoopActivity)
	w.RegisterActivity(agentActivities.AgentExecutionActivity)

	_ = ownsClient // retained for client lifecycle clarity

	return &Worker{
		client:  c,
		worker:  w,
		config:  cfg,
		runtime: rt,
	}, nil
}

// NewWorkerWithRuntime initializes a new Temporal worker with an explicit runtime dependency.
func NewWorkerWithRuntime(cfg Config, c client.Client, rt agentruntime.AgentRuntime) (*Worker, error) {
	return NewWorker(cfg, c, WithAgentRuntime(rt))
}

// Start starts the worker in a non-blocking background goroutine.
func (w *Worker) Start() error {
	if err := w.worker.Start(); err != nil {
		return fmt.Errorf("failed to start temporal worker on task queue %s: %w", w.config.TaskQueue, err)
	}
	return nil
}

// Run starts the worker and blocks until an interrupt channel receives a signal.
func (w *Worker) Run(interruptCh <-chan interface{}) error {
	if err := w.worker.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("temporal worker error on task queue %s: %w", w.config.TaskQueue, err)
	}
	return nil
}

// Stop gracefully stops the worker and closes the underlying client connection.
func (w *Worker) Stop() {
	if w.worker != nil {
		w.worker.Stop()
	}
	if w.client != nil {
		w.client.Close()
	}
}

// Config returns the worker's configuration.
func (w *Worker) Config() Config {
	return w.config
}

// Runtime returns the worker's configured AgentRuntime.
func (w *Worker) Runtime() agentruntime.AgentRuntime {
	return w.runtime
}
