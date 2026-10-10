package domain

import (
	"fmt"
	"strings"
)

// ValidateAgentName validates an agent's display name.
func ValidateAgentName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return NewValidationError("agent name is required and cannot be empty")
	}
	if len(trimmed) > 255 {
		return NewValidationError("agent name exceeds maximum length of 255 characters")
	}
	return nil
}

// ValidateConfiguration validates all fields in the agent version configuration.
func ValidateConfiguration(cfg Configuration) error {
	var details []string

	// Validate prompt_template
	if strings.TrimSpace(cfg.PromptTemplate) == "" {
		details = append(details, "prompt_template is required and cannot be empty")
	}

	// Validate reliability_threshold in [0.0, 1.0]
	if cfg.ReliabilityThreshold < 0.0 || cfg.ReliabilityThreshold > 1.0 {
		details = append(details, fmt.Sprintf("reliability_threshold must be between 0.0 and 1.0, got %f", cfg.ReliabilityThreshold))
	}

	// Validate execution_mode
	normalizedMode := ExecutionMode(strings.ToLower(strings.TrimSpace(string(cfg.ExecutionMode))))
	if _, ok := SupportedExecutionModes[normalizedMode]; !ok {
		details = append(details, fmt.Sprintf("execution_mode '%s' is invalid; supported modes are: autonomous, supervised, human_in_the_loop, gated", cfg.ExecutionMode))
	}

	// Validate tools
	for i, tool := range cfg.Tools {
		if strings.TrimSpace(tool) == "" {
			details = append(details, fmt.Sprintf("tools[%d] cannot be empty", i))
		}
	}

	// Security check: ensure no plaintext LLM or MCP credentials are stored
	if err := checkForPlaintextCredentials(cfg); err != nil {
		details = append(details, err.Error())
	}

	if len(details) > 0 {
		return NewValidationError("configuration validation failed", details...)
	}

	return nil
}

// checkForPlaintextCredentials checks that configuration does not contain raw API keys or tokens.
func checkForPlaintextCredentials(cfg Configuration) error {
	// Sensitive key substrings to check in guardrails/budgets maps
	sensitiveKeyPatterns := []string{
		"api_key", "apikey", "secret_key", "secretkey", "auth_token", "access_token", "private_key",
	}

	checkMap := func(m map[string]any, section string) error {
		for k, v := range m {
			lowerK := strings.ToLower(k)
			for _, pattern := range sensitiveKeyPatterns {
				if strings.Contains(lowerK, pattern) {
					// Check if value is a raw credential string instead of a reference (e.g. "env:..." or "secret:...")
					if strVal, ok := v.(string); ok {
						if !strings.HasPrefix(strVal, "env:") &&
							!strings.HasPrefix(strVal, "secret:") &&
							!strings.HasPrefix(strVal, "ref:") {
							return fmt.Errorf("plaintext credential detected in %s['%s']; credentials must not be stored in PostgreSQL (use secret reference instead)", section, k)
						}
					}
				}
			}
			// Check for known secret prefixes
			if strVal, ok := v.(string); ok {
				if strings.HasPrefix(strVal, "sk-") || strings.HasPrefix(strVal, "ghp_") {
					return fmt.Errorf("plaintext API key token detected in %s['%s']; credentials must not be stored in PostgreSQL", section, k)
				}
			}
		}
		return nil
	}

	if err := checkMap(cfg.Guardrails, "guardrails"); err != nil {
		return err
	}
	if err := checkMap(cfg.Budgets, "budgets"); err != nil {
		return err
	}

	return nil
}
