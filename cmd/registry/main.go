package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aswinsreeraj/arda/pkg/database"
	"github.com/aswinsreeraj/arda/services/registry"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	db, err := database.Connect(database.LoadConfigFromEnv())
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// SECURITY NOTE: The standalone Agent Registry defaults to HeaderTenantResolver (when resolver is nil),
	// which trusts the client-supplied X-Tenant-ID header directly without cryptographic verification.
	// This mode is intended strictly for standalone local development and automated store tests.
	// In production, the Registry must run in-process behind the API Gateway using ContextTenantResolver.
	// To prevent unauthorized external access, this standalone listener must remain bound to loopback (127.0.0.1).
	server := registry.NewServer(db.DB, nil)
	addr := fmt.Sprintf("127.0.0.1:%s", port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      server.Handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown handling
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("ARDA Agent Registry v1 service listening on http://%s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down Agent Registry service...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Println("Agent Registry service stopped cleanly.")
}
