package temporal

import (
	"os"
	"strings"
)

const (
	// DefaultAddress is the standard local development Temporal frontend gRPC address.
	DefaultAddress = "127.0.0.1:7233"

	// DefaultNamespace is the standard development namespace.
	DefaultNamespace = "default"

	// DefaultTaskQueue is the default named task queue for ARDA v1 workflows and workers.
	DefaultTaskQueue = "arda-v1-task-queue"
)

// Config holds connection and routing parameters for Temporal clients and workers.
type Config struct {
	// Address is the host:port of the Temporal frontend gRPC service (e.g. "127.0.0.1:7233").
	Address string

	// Namespace is the registered Temporal namespace (e.g. "default").
	Namespace string

	// TaskQueue is the named task queue that workers listen on and clients route to.
	TaskQueue string
}

// ConfigFromEnv loads Temporal configuration from environment variables with sensible defaults.
func ConfigFromEnv() Config {
	addr := strings.TrimSpace(os.Getenv("TEMPORAL_ADDRESS"))
	if addr == "" {
		addr = strings.TrimSpace(os.Getenv("TEMPORAL_HOST_PORT"))
	}
	if addr == "" {
		addr = DefaultAddress
	}

	ns := strings.TrimSpace(os.Getenv("TEMPORAL_NAMESPACE"))
	if ns == "" {
		ns = DefaultNamespace
	}

	tq := strings.TrimSpace(os.Getenv("TEMPORAL_TASK_QUEUE"))
	if tq == "" {
		tq = DefaultTaskQueue
	}

	return Config{
		Address:   addr,
		Namespace: ns,
		TaskQueue: tq,
	}
}
