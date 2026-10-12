package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aswinsreeraj/arda/services/registry"
	_ "github.com/lib/pq"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://arda_user:arda_password@127.0.0.1:5432/arda?sslmode=disable"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Failed to open database connection: %v", err)
	}
	defer db.Close()

	// Verify database connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	// SECURITY NOTE: The standalone Agent Registry defaults to HeaderTenantResolver (when resolver is nil),
	// which trusts the client-supplied X-Tenant-ID header directly without cryptographic verification.
	// This mode is intended strictly for standalone local development and automated store tests.
	// In production, the Registry must run in-process behind the API Gateway using ContextTenantResolver.
	// To prevent unauthorized external access, this standalone listener must remain bound to loopback (127.0.0.1).
	server := registry.NewServer(db, nil)
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
