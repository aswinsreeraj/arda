package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aswinsreeraj/arda/pkg/database"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	reviewevents "github.com/aswinsreeraj/arda/services/review/events"
	reviewgrpc "github.com/aswinsreeraj/arda/services/review/grpc"
	reviewservice "github.com/aswinsreeraj/arda/services/review/service"
	reviewstore "github.com/aswinsreeraj/arda/services/review/store"
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

	// 1. Database Connection
	db, err := database.Connect(database.LoadConfigFromEnv())
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize database: %v", err)
	}
	defer db.Close()

	// 2. Temporal Client (optional for signaling durable workflows)
	var signaler reviewservice.TemporalSignaler
	temporalAddr := os.Getenv("TEMPORAL_ADDRESS")
	if temporalAddr == "" {
		temporalAddr = "127.0.0.1:7233"
	}
	tempClient, err := temporal.NewClient(temporal.Config{
		Address:   temporalAddr,
		Namespace: os.Getenv("TEMPORAL_NAMESPACE"),
		TaskQueue: os.Getenv("TEMPORAL_TASK_QUEUE"),
	})
	if err == nil {
		signaler = tempClient.RawClient()
		defer tempClient.Close()
	} else {
		log.Printf("WARN: Temporal client not available: %v", err)
	}

	// 3. Redpanda / Kafka Producer
	brokerAddr := os.Getenv("REDPANDA_BROKERS")
	if brokerAddr == "" {
		brokerAddr = "127.0.0.1:19092"
	}
	producer, err := reviewevents.NewKafkaProducer(brokerAddr)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize Kafka/Redpanda producer: %v", err)
	}
	defer producer.Close()

	// 4. Store and Outbox Relay
	revStore := reviewstore.NewPostgresStore(db.DB)
	outboxRelay := reviewevents.NewOutboxRelay(revStore, producer)

	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	go outboxRelay.Start(relayCtx)

	// 5. Review Service
	producerSecret := os.Getenv("REDPANDA_PRODUCER_SECRET")
	if producerSecret == "" {
		producerSecret = "arda-producer-secret-default"
	}

	revSvc := reviewservice.NewService(
		revStore,
		reviewservice.WithTemporalSignaler(signaler),
		reviewservice.WithProducerSecret(producerSecret),
		reviewservice.WithOutboxRelay(outboxRelay),
	)

	// 6. gRPC Server
	grpcPort := os.Getenv("REVIEW_GRPC_PORT")
	if grpcPort == "" {
		grpcPort = "50052"
	}
	grpcAddr := fmt.Sprintf("127.0.0.1:%s", grpcPort)
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("FATAL: Failed to listen on gRPC %s: %v", grpcAddr, err)
	}

	grpcServer := reviewgrpc.NewServer(revSvc)

	go func() {
		log.Printf("ARDA Human Review Service listening on gRPC %s", grpcAddr)
		if err := grpcServer.Serve(lis); err != nil {
			log.Printf("Human Review gRPC server ended: %v", err)
		}
	}()

	// 7. Graceful Shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	<-stop
	log.Println("Shutting down Human Review Service...")
	grpcServer.GracefulStop()
	relayCancel()
	log.Println("Human Review Service stopped cleanly.")
}
