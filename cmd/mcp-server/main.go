package main

import (
	"fmt"
	"log"
	"os"

	"github.com/aswinsreeraj/arda/pkg/mcp/simulator"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	port := os.Getenv("MCP_PORT")
	if port == "" {
		port = "8085"
	}

	// Use default demo tenant ID or parse from environment
	tenantIDStr := os.Getenv("MCP_TENANT_ID")
	tenantID := uuid.Nil
	if tenantIDStr != "" {
		if parsed, err := uuid.Parse(tenantIDStr); err == nil {
			tenantID = parsed
		}
	}
	if tenantID == uuid.Nil {
		tenantID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	}

	sim := simulator.NewTransactionSimulator(tenantID)
	mcpServer := sim.Server()

	// Create SSE transport for MCP
	sseServer := server.NewSSEServer(mcpServer)

	addr := fmt.Sprintf("127.0.0.1:%s", port)
	log.Printf("==============================================================")
	log.Printf("  ARDA Simulated Transaction System MCP Server")
	log.Printf("  Starting on http://%s", addr)
	log.Printf("  SSE Endpoint: http://%s/sse", addr)
	log.Printf("  Available Tools: get_transaction, list_transactions, reverse_transaction")
	log.Printf("==============================================================")

	if err := sseServer.Start(addr); err != nil {
		log.Fatalf("MCP Server error: %v", err)
	}
}
