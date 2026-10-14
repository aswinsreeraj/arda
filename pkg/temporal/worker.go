package temporal

import (
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// Worker wraps a Temporal worker that registers and executes ARDA workflows and activities.
type Worker struct {
	client client.Client
	worker worker.Worker
	config Config
}

// NewWorker initializes a new Temporal worker listening on the configured task queue.
// If an existing client.Client is provided, it is reused; otherwise a new client connection is established.
func NewWorker(cfg Config, c client.Client) (*Worker, error) {
	if cfg.Address == "" {
		cfg.Address = DefaultAddress
	}
	if cfg.Namespace == "" {
		cfg.Namespace = DefaultNamespace
	}
	if cfg.TaskQueue == "" {
		cfg.TaskQueue = DefaultTaskQueue
	}

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

	_ = ownsClient // retained for client lifecycle clarity

	return &Worker{
		client: c,
		worker: w,
		config: cfg,
	}, nil
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
