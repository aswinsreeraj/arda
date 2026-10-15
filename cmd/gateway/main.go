package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/aswinsreeraj/arda/pkg/database"
	"github.com/aswinsreeraj/arda/services/gateway"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	jwtSecretStr := os.Getenv("JWT_SECRET")
	if jwtSecretStr == "" {
		log.Fatal("FATAL: JWT_SECRET environment variable is missing or empty. API Gateway fails closed.")
	}

	jwtIssuer := os.Getenv("JWT_ISSUER")
	if jwtIssuer == "" {
		jwtIssuer = "arda-auth"
	}

	jwtAudience := os.Getenv("JWT_AUDIENCE")
	if jwtAudience == "" {
		jwtAudience = "arda-api"
	}

	rps := 20.0
	if rpsStr := os.Getenv("RATE_LIMIT_RPS"); rpsStr != "" {
		if parsed, err := strconv.ParseFloat(rpsStr, 64); err == nil && parsed > 0 {
			rps = parsed
		}
	}

	burst := 40
	if burstStr := os.Getenv("RATE_LIMIT_BURST"); burstStr != "" {
		if parsed, err := strconv.Atoi(burstStr); err == nil && parsed > 0 {
			burst = parsed
		}
	}

	db, err := database.Connect(database.LoadConfigFromEnv())
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	cfg := gateway.Config{
		Port:                   port,
		JWTSecret:              []byte(jwtSecretStr),
		JWTIssuer:              jwtIssuer,
		JWTAudience:            jwtAudience,
		RateLimitRPS:           rps,
		RateLimitBurst:         burst,
		RateLimitEnabled:       true,
		ReliabilityServiceAddr: os.Getenv("RELIABILITY_SERVICE_ADDR"),
		TemporalAddr:           os.Getenv("TEMPORAL_ADDRESS"),
		TemporalNamespace:      os.Getenv("TEMPORAL_NAMESPACE"),
		TemporalTaskQueue:      os.Getenv("TEMPORAL_TASK_QUEUE"),
	}

	server, err := gateway.NewServer(cfg, db.DB)
	if err != nil {
		log.Fatalf("Failed to initialize API Gateway: %v", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%s", port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      server.Handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("ARDA API Gateway v1 service listening on http://%s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down API Gateway...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	if server.ReliabilityService != nil {
		_ = server.ReliabilityService.Close()
	}
	if server.TemporalClient != nil {
		server.TemporalClient.Close()
	}
	log.Println("API Gateway stopped cleanly.")
}
