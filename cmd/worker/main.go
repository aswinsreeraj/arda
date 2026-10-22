package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aswinsreeraj/arda/pkg/temporal"
)

func loadEnv(filepath string) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			if os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}
	}
}

func main() {
	loadEnv(".env")
	envCfg := temporal.ConfigFromEnv()

	addressFlag := flag.String("address", envCfg.Address, "Temporal server host:port")
	namespaceFlag := flag.String("namespace", envCfg.Namespace, "Temporal namespace")
	taskQueueFlag := flag.String("task-queue", envCfg.TaskQueue, "Temporal task queue name")
	flag.Parse()

	cfg := temporal.Config{
		Address:   *addressFlag,
		Namespace: *namespaceFlag,
		TaskQueue: *taskQueueFlag,
	}

	log.Printf("Starting ARDA Temporal Worker...")
	log.Printf("  Address:    %s", cfg.Address)
	log.Printf("  Namespace:  %s", cfg.Namespace)
	log.Printf("  Task Queue: %s", cfg.TaskQueue)

	w, err := temporal.NewWorker(cfg, nil)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize Temporal worker: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("ARDA Temporal Worker listening on task queue %q. Press Ctrl+C to stop.", cfg.TaskQueue)
		if err := w.Start(); err != nil {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("Worker start error: %v", err)
	case sig := <-sigCh:
		log.Printf("Received shutdown signal (%v). Gracefully stopping worker...", sig)
		w.Stop()
		log.Printf("ARDA Temporal Worker stopped cleanly.")
	}
}
