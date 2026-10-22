package agentruntime_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/services/registry/domain"
)

func TestBuildPrompt_SuccessCases(t *testing.T) {
	tests := []struct {
		name     string
		template string
		payload  map[string]any
		expected string
	}{
		{
			name:     "plain text without placeholders",
			template: "You are a customer service assistant. Be polite and helpful.",
			payload:  map[string]any{"customer_id": "cust_123"},
			expected: "You are a customer service assistant. Be polite and helpful.",
		},
		{
			name:     "one placeholder with input prefix",
			template: "Customer identifier: {{input.customer_id}}",
			payload:  map[string]any{"customer_id": "cust_999"},
			expected: "Customer identifier: cust_999",
		},
		{
			name:     "one placeholder without input prefix",
			template: "Customer identifier: {{customer_id}}",
			payload:  map[string]any{"customer_id": "cust_888"},
			expected: "Customer identifier: cust_888",
		},
		{
			name:     "placeholder with whitespace inside braces",
			template: "Hello {{  input.name  }}!",
			payload:  map[string]any{"name": "Alice"},
			expected: "Hello Alice!",
		},
		{
			name:     "multiple placeholders",
			template: "Hello {{input.first_name}} {{input.last_name}}, account: {{input.account_id}}.",
			payload: map[string]any{
				"first_name": "Bob",
				"last_name":  "Smith",
				"account_id": "ACC-404",
			},
			expected: "Hello Bob Smith, account: ACC-404.",
		},
		{
			name:     "nested payload values",
			template: "Ship to {{input.user.name}} in {{input.user.address.city}}, {{input.user.address.zip}}.",
			payload: map[string]any{
				"user": map[string]any{
					"name": "Charlie",
					"address": map[string]any{
						"city": "Seattle",
						"zip":  "98101",
					},
				},
			},
			expected: "Ship to Charlie in Seattle, 98101.",
		},
		{
			name:     "placeholder with hyphens and underscores",
			template: "Job ID: {{input.job_id}}, Task: {{input.task-ref}}",
			payload: map[string]any{
				"job_id":   "job_1",
				"task-ref": "ref-abc",
			},
			expected: "Job ID: job_1, Task: ref-abc",
		},
		{
			name:     "empty template string",
			template: "",
			payload:  map[string]any{"key": "val"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := agentruntime.BuildPrompt(tt.template, tt.payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestBuildPrompt_ScalarFormatting(t *testing.T) {
	tests := []struct {
		name     string
		template string
		payload  map[string]any
		expected string
	}{
		{
			name:     "boolean true and false",
			template: "Active: {{input.is_active}}, Verified: {{input.is_verified}}",
			payload:  map[string]any{"is_active": true, "is_verified": false},
			expected: "Active: true, Verified: false",
		},
		{
			name:     "signed integers",
			template: "Int: {{input.val_int}}, Int64: {{input.val_i64}}, Negative: {{input.val_neg}}",
			payload: map[string]any{
				"val_int": 42,
				"val_i64": int64(9223372036854775807),
				"val_neg": -100,
			},
			expected: "Int: 42, Int64: 9223372036854775807, Negative: -100",
		},
		{
			name:     "unsigned integers",
			template: "Uint: {{input.val_uint}}, Uint64: {{input.val_u64}}",
			payload: map[string]any{
				"val_uint": uint(255),
				"val_u64":  uint64(18446744073709551615),
			},
			expected: "Uint: 255, Uint64: 18446744073709551615",
		},
		{
			name:     "floating point numbers",
			template: "Pi: {{input.pi}}, Rate: {{input.rate}}, Whole: {{input.whole}}",
			payload: map[string]any{
				"pi":    3.14159,
				"rate":  0.05,
				"whole": 50.0,
			},
			expected: "Pi: 3.14159, Rate: 0.05, Whole: 50",
		},
		{
			name:     "json.Number type",
			template: "Amount: {{input.amount}}",
			payload: map[string]any{
				"amount": json.Number("12345.6789"),
			},
			expected: "Amount: 12345.6789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := agentruntime.BuildPrompt(tt.template, tt.payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestBuildPrompt_MissingValues(t *testing.T) {
	tests := []struct {
		name     string
		template string
		payload  map[string]any
	}{
		{
			name:     "missing top-level key",
			template: "Hello {{input.nonexistent}}",
			payload:  map[string]any{"other_key": "val"},
		},
		{
			name:     "missing nested key",
			template: "Hello {{input.user.missing}}",
			payload:  map[string]any{"user": map[string]any{"name": "Alice"}},
		},
		{
			name:     "missing intermediate parent",
			template: "Hello {{input.user.address.street}}",
			payload:  map[string]any{"user": map[string]any{}},
		},
		{
			name:     "attempt to traverse non-map scalar",
			template: "Hello {{input.user.name.first}}",
			payload:  map[string]any{"user": map[string]any{"name": "Alice"}},
		},
		{
			name:     "nil payload",
			template: "Hello {{input.name}}",
			payload:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := agentruntime.BuildPrompt(tt.template, tt.payload)
			if err == nil {
				t.Fatalf("expected ErrMissingPlaceholderValue, got nil")
			}
			if !errors.Is(err, agentruntime.ErrMissingPlaceholderValue) {
				t.Errorf("expected error to wrap ErrMissingPlaceholderValue, got: %v", err)
			}
		})
	}
}

func TestBuildPrompt_MalformedPlaceholders(t *testing.T) {
	tests := []struct {
		name     string
		template string
		payload  map[string]any
	}{
		{
			name:     "unclosed placeholder",
			template: "Hello {{input.customer_id",
			payload:  map[string]any{"customer_id": "123"},
		},
		{
			name:     "stray closing delimiter before open",
			template: "Hello input.customer_id}} foo",
			payload:  map[string]any{"customer_id": "123"},
		},
		{
			name:     "stray closing delimiter after open",
			template: "Hello {{input.customer_id}} extra}}",
			payload:  map[string]any{"customer_id": "123"},
		},
		{
			name:     "nested placeholder",
			template: "Hello {{ {{input.name}} }}",
			payload:  map[string]any{"name": "Alice"},
		},
		{
			name:     "empty placeholder",
			template: "Hello {{}}",
			payload:  map[string]any{"name": "Alice"},
		},
		{
			name:     "whitespace only placeholder",
			template: "Hello {{    }}",
			payload:  map[string]any{"name": "Alice"},
		},
		{
			name:     "empty path after input prefix",
			template: "Hello {{input.}}",
			payload:  map[string]any{"name": "Alice"},
		},
		{
			name:     "consecutive dots in path",
			template: "Hello {{input..name}}",
			payload:  map[string]any{"name": "Alice"},
		},
		{
			name:     "leading dot in path",
			template: "Hello {{.name}}",
			payload:  map[string]any{"name": "Alice"},
		},
		{
			name:     "trailing dot in path",
			template: "Hello {{name.}}",
			payload:  map[string]any{"name": "Alice"},
		},
		{
			name:     "spaces inside key identifier",
			template: "Hello {{input.first name}}",
			payload:  map[string]any{"first name": "Alice"},
		},
		{
			name:     "invalid symbols in key identifier",
			template: "Hello {{input.name@domain}}",
			payload:  map[string]any{"name": "Alice"},
		},
		{
			name:     "bracket syntax rejected",
			template: "Hello {{input[0]}}",
			payload:  map[string]any{"name": "Alice"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := agentruntime.BuildPrompt(tt.template, tt.payload)
			if err == nil {
				t.Fatalf("expected ErrMalformedPlaceholder for %q, got nil", tt.template)
			}
			if !errors.Is(err, agentruntime.ErrMalformedPlaceholder) {
				t.Errorf("expected ErrMalformedPlaceholder, got: %v", err)
			}
		})
	}
}

func TestBuildPrompt_UnsupportedComplexValues(t *testing.T) {
	tests := []struct {
		name     string
		template string
		payload  map[string]any
	}{
		{
			name:     "map value cannot be rendered into scalar placeholder",
			template: "User profile: {{input.user}}",
			payload:  map[string]any{"user": map[string]any{"name": "Alice"}},
		},
		{
			name:     "slice value cannot be rendered into scalar placeholder",
			template: "Tools: {{input.tools}}",
			payload:  map[string]any{"tools": []string{"search", "calculator"}},
		},
		{
			name:     "nil value cannot be rendered",
			template: "Status: {{input.status}}",
			payload:  map[string]any{"status": nil},
		},
		{
			name:     "placeholder named input refers to root map",
			template: "Data: {{input}}",
			payload:  map[string]any{"k": "v"},
		},
		{
			name:     "NaN float value rejected",
			template: "Score: {{input.score}}",
			payload:  map[string]any{"score": math.NaN()},
		},
		{
			name:     "Inf float value rejected",
			template: "Score: {{input.score}}",
			payload:  map[string]any{"score": math.Inf(1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := agentruntime.BuildPrompt(tt.template, tt.payload)
			if err == nil {
				t.Fatalf("expected ErrUnsupportedValueType, got nil")
			}
			if !errors.Is(err, agentruntime.ErrUnsupportedValueType) {
				t.Errorf("expected ErrUnsupportedValueType, got: %v", err)
			}
		})
	}
}

func TestBuildPrompt_DeterministicOutput(t *testing.T) {
	template := "User: {{input.name}}, Age: {{input.age}}, Score: {{input.score}}, Active: {{input.active}}"
	payload := map[string]any{
		"name":   "Dave",
		"age":    30,
		"score":  99.5,
		"active": true,
	}

	firstRun, err := agentruntime.BuildPrompt(template, payload)
	if err != nil {
		t.Fatalf("unexpected error on first run: %v", err)
	}

	// Repeat 100 times to verify determinism
	for i := 0; i < 100; i++ {
		subsequent, err := agentruntime.BuildPrompt(template, payload)
		if err != nil {
			t.Fatalf("unexpected error on run %d: %v", i, err)
		}
		if subsequent != firstRun {
			t.Fatalf("non-deterministic output at iteration %d:\nfirst: %s\nrun:   %s", i, firstRun, subsequent)
		}
	}
}

func TestPromptBuilder_StructAndConfigMethods(t *testing.T) {
	builder := agentruntime.NewPromptBuilder()

	cfg := domain.Configuration{
		PromptTemplate: "System prompt for agent: assist {{input.client_name}} with inquiry.",
	}
	payload := map[string]any{"client_name": "Acme Corp"}

	// 1. Test BuildFromConfig
	out1, err := builder.BuildFromConfig(cfg, payload)
	if err != nil {
		t.Fatalf("unexpected error from BuildFromConfig: %v", err)
	}
	expected := "System prompt for agent: assist Acme Corp with inquiry."
	if out1 != expected {
		t.Errorf("expected %q, got %q", expected, out1)
	}

	// 2. Test Build
	out2, err := builder.Build(cfg.PromptTemplate, payload)
	if err != nil {
		t.Fatalf("unexpected error from Build: %v", err)
	}
	if out2 != expected {
		t.Errorf("expected %q, got %q", expected, out2)
	}

	// 3. Test package-level BuildPromptFromConfig
	out3, err := agentruntime.BuildPromptFromConfig(cfg, payload)
	if err != nil {
		t.Fatalf("unexpected error from BuildPromptFromConfig: %v", err)
	}
	if out3 != expected {
		t.Errorf("expected %q, got %q", expected, out3)
	}
}
