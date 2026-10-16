package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aswinsreeraj/arda/pkg/database"
	"github.com/aswinsreeraj/arda/services/registry"
	registrygrpc "github.com/aswinsreeraj/arda/services/registry/grpc"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/aswinsreeraj/arda/services/registry/store"
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

	// Initialize Registry store, service, and servers
	regStore := store.NewPostgresStore(db.DB)
	regSvc := service.NewRegistryService(regStore)

	// 1. Start gRPC Server
	grpcPort := os.Getenv("GRPC_PORT")
	if grpcPort == "" {
		grpcPort = "50051"
	}
	grpcAddr := fmt.Sprintf("127.0.0.1:%s", grpcPort)
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("Failed to listen on gRPC %s: %v", grpcAddr, err)
	}
	grpcServer := registrygrpc.NewServer(regSvc)

	go func() {
		log.Printf("ARDA Agent Registry gRPC service listening on %s", grpcAddr)
		if err := grpcServer.Serve(grpcLis); err != nil {
			log.Printf("Agent Registry gRPC server ended: %v", err)
		}
	}()

	// 2. Start HTTP Server for local development/compatibility
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
		log.Printf("ARDA Agent Registry v1 HTTP service listening on http://%s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down Agent Registry service...")

	grpcServer.GracefulStop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Println("Agent Registry service stopped cleanly.")
}
