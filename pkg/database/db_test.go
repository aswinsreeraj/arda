package database_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/database"
)

func TestLoadConfigFromEnv_Defaults(t *testing.T) {
	_ = os.Unsetenv("DATABASE_URL")
	_ = os.Unsetenv("DB_MAX_OPEN_CONNS")
	_ = os.Unsetenv("DB_MAX_IDLE_CONNS")
	_ = os.Unsetenv("DB_CONN_MAX_LIFETIME")
	_ = os.Unsetenv("DB_CONN_MAX_IDLE_TIME")

	cfg := database.LoadConfigFromEnv()
	if cfg.URL != "postgres://arda_user:arda_password@127.0.0.1:5432/arda?sslmode=disable" {
		t.Errorf("unexpected default URL: %s", cfg.URL)
	}
	if cfg.MaxOpenConns != 25 {
		t.Errorf("expected MaxOpenConns 25, got %d", cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns != 5 {
		t.Errorf("expected MaxIdleConns 5, got %d", cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime != 15*time.Minute {
		t.Errorf("expected ConnMaxLifetime 15m, got %v", cfg.ConnMaxLifetime)
	}
	if cfg.ConnMaxIdleTime != 5*time.Minute {
		t.Errorf("expected ConnMaxIdleTime 5m, got %v", cfg.ConnMaxIdleTime)
	}
}

func TestLoadConfigFromEnv_Custom(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test:test@127.0.0.1:5432/testdb")
	t.Setenv("DB_MAX_OPEN_CONNS", "50")
	t.Setenv("DB_MAX_IDLE_CONNS", "10")
	t.Setenv("DB_CONN_MAX_LIFETIME", "30m")
	t.Setenv("DB_CONN_MAX_IDLE_TIME", "10m")

	cfg := database.LoadConfigFromEnv()
	if cfg.URL != "postgres://test:test@127.0.0.1:5432/testdb" {
		t.Errorf("unexpected URL: %s", cfg.URL)
	}
	if cfg.MaxOpenConns != 50 {
		t.Errorf("expected 50, got %d", cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns != 10 {
		t.Errorf("expected 10, got %d", cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime != 30*time.Minute {
		t.Errorf("expected 30m, got %v", cfg.ConnMaxLifetime)
	}
	if cfg.ConnMaxIdleTime != 10*time.Minute {
		t.Errorf("expected 10m, got %v", cfg.ConnMaxIdleTime)
	}
}

func TestConnect_Failure(t *testing.T) {
	// Connect with invalid port to verify fail-closed error handling
	cfg := database.Config{
		URL: "postgres://invalid_user:invalid_pass@127.0.0.1:54329/nonexistent?sslmode=disable&connect_timeout=1",
	}

	_, err := database.Connect(cfg)
	if err == nil {
		t.Fatalf("expected connection error for invalid host, got nil")
	}
}

func TestDB_NilMethods(t *testing.T) {
	var db *database.DB
	if err := db.Ping(context.Background()); err == nil {
		t.Errorf("expected error pinging nil DB, got nil")
	}
	if err := db.Close(); err != nil {
		t.Errorf("expected nil closing nil DB, got %v", err)
	}
}
