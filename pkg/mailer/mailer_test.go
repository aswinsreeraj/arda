package mailer

import (
	"os"
	"testing"
)

func TestLoadConfigFromEnv(t *testing.T) {
	os.Setenv("MAILTRAP_HOST", "sandbox.smtp.mailtrap.io")
	os.Setenv("MAILTRAP_PORT", "2525")
	os.Setenv("MAILTRAP_USER", "test_user")
	os.Setenv("MAILTRAP_PASS", "test_pass")
	defer func() {
		os.Unsetenv("MAILTRAP_HOST")
		os.Unsetenv("MAILTRAP_PORT")
		os.Unsetenv("MAILTRAP_USER")
		os.Unsetenv("MAILTRAP_PASS")
	}()

	cfg := LoadConfigFromEnv()
	if cfg.Host != "sandbox.smtp.mailtrap.io" {
		t.Errorf("expected host sandbox.smtp.mailtrap.io, got %s", cfg.Host)
	}
	if cfg.Port != 2525 {
		t.Errorf("expected port 2525, got %d", cfg.Port)
	}
	if cfg.Username != "test_user" {
		t.Errorf("expected username test_user, got %s", cfg.Username)
	}
	if cfg.Password != "test_pass" {
		t.Errorf("expected password test_pass, got %s", cfg.Password)
	}
}

func TestClient_MockSendWhenUnconfigured(t *testing.T) {
	cfg := Config{}
	client := NewClient(cfg)
	if client.IsConfigured() {
		t.Fatal("expected client to not be configured")
	}

	err := client.SendInvitationEmail("user@example.com", "Alice Admin", "Acme Corp", "http://localhost:5174/invite?token=123")
	if err != nil {
		t.Fatalf("expected nil error on unconfigured dev mock send, got: %v", err)
	}

	err = client.SendVerificationOTPEmail("user@example.com", "849201")
	if err != nil {
		t.Fatalf("expected nil error on unconfigured dev mock send, got: %v", err)
	}
}
