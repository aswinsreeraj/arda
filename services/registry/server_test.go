package registry_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/services/registry"
	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestRegistryServer_Lifecycle(t *testing.T) {
	server := registry.NewServer(nil, nil)
	if server.Handler == nil {
		t.Fatalf("expected non-nil server handler")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	httpServer := &http.Server{
		Addr:    ln.Addr().String(),
		Handler: server.Handler,
	}

	go func() {
		_ = httpServer.Serve(ln)
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/healthz", ln.Addr().String()))
	if err != nil {
		t.Fatalf("failed to query /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /healthz, got %d", resp.StatusCode)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("graceful shutdown failed: %v", err)
	}
}
