package domain

import (
	"testing"
)

func TestValidateAgentName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid name", input: "Support Bot", wantErr: false},
		{name: "empty name", input: "", wantErr: true},
		{name: "whitespace only name", input: "   ", wantErr: true},
		{name: "max length name", input: string(make([]byte, 255)), wantErr: false},
		{name: "exceeds max length", input: string(make([]byte, 256)), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAgentName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateAgentName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateConfiguration(t *testing.T) {
	validBaseConfig := Configuration{
		PromptTemplate:       "You are a helpful assistant.",
		Tools:                []string{"search", "calculator"},
		ReliabilityThreshold: 0.85,
		ExecutionMode:        ExecutionModeSupervised,
		Guardrails: map[string]any{
			"max_tokens": 1000,
		},
		Budgets: map[string]any{
			"monthly_usd": 50.0,
		},
	}

	t.Run("valid configuration passes", func(t *testing.T) {
		err := ValidateConfiguration(validBaseConfig)
		if err != nil {
			t.Fatalf("expected valid config to pass, got: %v", err)
		}
	})

	t.Run("valid boundary thresholds pass", func(t *testing.T) {
		cfg0 := validBaseConfig
		cfg0.ReliabilityThreshold = 0.0
		if err := ValidateConfiguration(cfg0); err != nil {
			t.Errorf("expected 0.0 threshold to pass, got: %v", err)
		}

		cfg1 := validBaseConfig
		cfg1.ReliabilityThreshold = 1.0
		if err := ValidateConfiguration(cfg1); err != nil {
			t.Errorf("expected 1.0 threshold to pass, got: %v", err)
		}
	})

	t.Run("all valid execution modes pass", func(t *testing.T) {
		modes := []ExecutionMode{
			ExecutionModeAutonomous,
			ExecutionModeSupervised,
			ExecutionModeHumanInTheLoop,
			ExecutionModeGated,
		}
		for _, m := range modes {
			cfg := validBaseConfig
			cfg.ExecutionMode = m
			if err := ValidateConfiguration(cfg); err != nil {
				t.Errorf("expected mode %q to pass, got: %v", m, err)
			}
		}
	})

	t.Run("empty prompt template fails", func(t *testing.T) {
		cfg := validBaseConfig
		cfg.PromptTemplate = "   "
		err := ValidateConfiguration(cfg)
		if err == nil {
			t.Fatalf("expected error for empty prompt template, got nil")
		}
	})

	t.Run("threshold out of bounds fails", func(t *testing.T) {
		cfgNeg := validBaseConfig
		cfgNeg.ReliabilityThreshold = -0.01
		if err := ValidateConfiguration(cfgNeg); err == nil {
			t.Errorf("expected error for negative threshold, got nil")
		}

		cfgHigh := validBaseConfig
		cfgHigh.ReliabilityThreshold = 1.01
		if err := ValidateConfiguration(cfgHigh); err == nil {
			t.Errorf("expected error for threshold > 1.0, got nil")
		}
	})

	t.Run("unsupported execution mode fails", func(t *testing.T) {
		cfg := validBaseConfig
		cfg.ExecutionMode = "uncontrolled"
		err := ValidateConfiguration(cfg)
		if err == nil {
			t.Fatalf("expected error for unsupported execution mode, got nil")
		}
	})

	t.Run("empty tool string fails", func(t *testing.T) {
		cfg := validBaseConfig
		cfg.Tools = []string{"search", " "}
		err := ValidateConfiguration(cfg)
		if err == nil {
			t.Fatalf("expected error for blank tool name, got nil")
		}
	})

	t.Run("plaintext credentials detected and rejected", func(t *testing.T) {
		cfg := validBaseConfig
		cfg.Guardrails = map[string]any{
			"openai_api_key": "sk-12345abcdef67890",
		}
		err := ValidateConfiguration(cfg)
		if err == nil {
			t.Fatalf("expected error for plaintext API key, got nil")
		}

		// Reference should pass
		cfg.Guardrails = map[string]any{
			"openai_api_key": "secret:vault-openai-key",
		}
		if err := ValidateConfiguration(cfg); err != nil {
			t.Fatalf("expected secret reference to pass, got: %v", err)
		}
	})
}
