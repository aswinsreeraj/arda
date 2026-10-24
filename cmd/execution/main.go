package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/services/execution"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/gin-gonic/gin"
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

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	jwtSecretStr := os.Getenv("JWT_SECRET")
	if jwtSecretStr == "" {
		log.Fatal("FATAL: JWT_SECRET environment variable is missing or empty. Execution Service fails closed.")
	}

	jwtIssuer := os.Getenv("JWT_ISSUER")
	if jwtIssuer == "" {
		jwtIssuer = "arda-auth"
	}

	jwtAudience := os.Getenv("JWT_AUDIENCE")
	if jwtAudience == "" {
		jwtAudience = "arda-api"
	}

	// 1. Initialize Temporal Client
	temporalCfg := temporal.ConfigFromEnv()
	if addr := os.Getenv("TEMPORAL_ADDRESS"); addr != "" {
		temporalCfg.Address = addr
	}
	if ns := os.Getenv("TEMPORAL_NAMESPACE"); ns != "" {
		temporalCfg.Namespace = ns
	}
	if tq := os.Getenv("TEMPORAL_TASK_QUEUE"); tq != "" {
		temporalCfg.TaskQueue = tq
	}

	temporalClient, err := temporal.NewClient(temporalCfg)
	if err != nil {
		log.Fatalf("FATAL: Failed to connect to Temporal at %s: %v", temporalCfg.Address, err)
	}
	defer temporalClient.Close()

	// 2. Initialize Agent Registry gRPC Client
	registryAddr := os.Getenv("REGISTRY_GRPC_ADDR")
	if registryAddr == "" {
		registryAddr = "127.0.0.1:50051"
	}

	regProxyClient, err := proxy.NewRegistryClient(registryAddr)
	if err != nil {
		log.Fatalf("FATAL: Failed to connect to Agent Registry gRPC at %s: %v", registryAddr, err)
	}
	defer regProxyClient.Close()

	// 3. Initialize Authentication Validator
	jwtValidator, err := auth.NewTokenValidator(auth.JWTConfig{
		Secret:   []byte(jwtSecretStr),
		Issuer:   jwtIssuer,
		Audience: jwtAudience,
	})
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize JWT validator: %v", err)
	}

	// 4. Initialize Execution Service and HTTP Handler
	regAdapter := execution.NewGRPCRegistryAdapter(regProxyClient.Client())
	execService, err := execution.NewService(temporalClient, regAdapter)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize execution service: %v", err)
	}

	execHandler := execution.NewHandler(execService, jwtValidator)
	router := gin.New()
	router.Use(gin.Recovery())
	execHandler.RegisterRoutes(router)

	addr := fmt.Sprintf("127.0.0.1:%s", port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// 5. Start Server with Graceful Shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("ARDA Execution Service listening on http://%s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down Execution Service...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Println("Execution Service stopped cleanly.")
}
