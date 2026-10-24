package agentruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/google/uuid"
)

// testAdapter is a test double conforming to ProviderAdapter.
type testAdapter struct {
	providerID      string
	supportedModels []string
	capturedKey     string
	lastReq         agentruntime.LLMRequest
	err             error
	response        agentruntime.LLMResponse
}

func (a *testAdapter) ProviderID() string {
	return a.providerID
}

func (a *testAdapter) SupportedModels() []string {
	return a.supportedModels
}

func (a *testAdapter) IsModelSupported(model string) bool {
	if len(a.supportedModels) == 0 {
		return true
	}
	for _, m := range a.supportedModels {
		if m == model {
			return true
		}
	}
	return false
}

func (a *testAdapter) Generate(ctx context.Context, req agentruntime.LLMRequest) (agentruntime.LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return agentruntime.LLMResponse{}, err
	}
	if a.err != nil {
		return agentruntime.LLMResponse{}, a.err
	}
	key, _ := agentruntime.APIKeyFromContext(ctx)
	a.capturedKey = key
	a.lastReq = req
	if a.response.Content != "" {
		return a.response, nil
	}
	return agentruntime.LLMResponse{
		Content: "test adapter response",
		Model:   req.Model,
		Usage: &agentruntime.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}, nil
}

func TestProviderRegistry_RegistrationAndLookup(t *testing.T) {
	registry := agentruntime.NewProviderRegistry()

	adapter := &testAdapter{
		providerID:      "test-provider",
		supportedModels: []string{"model-a", "model-b"},
	}

	if err := registry.Register(adapter); err != nil {
		t.Fatalf("unexpected error registering adapter: %v", err)
	}

	if !registry.HasProvider("test-provider") {
		t.Fatal("expected registry to have 'test-provider'")
	}
	if !registry.HasProvider("TEST-PROVIDER") { // case insensitive
		t.Fatal("expected registry to be case-insensitive")
	}

	retrieved, err := registry.GetAdapter("test-provider")
	if err != nil {
		t.Fatalf("unexpected error retrieving adapter: %v", err)
	}
	if retrieved.ProviderID() != "test-provider" {
		t.Fatalf("expected provider ID 'test-provider', got %q", retrieved.ProviderID())
	}

	providers := registry.SupportedProviders()
	if len(providers) != 1 || providers[0] != "test-provider" {
		t.Fatalf("expected ['test-provider'], got %v", providers)
	}

	// Unknown provider lookup
	_, err = registry.GetAdapter("unknown-provider")
	if err == nil || !errors.Is(err, agentruntime.ErrUnsupportedProvider) {
		t.Fatalf("expected ErrUnsupportedProvider, got %v", err)
	}
}

func TestResolvingLLMClient_SuccessRoutingAndCredentials(t *testing.T) {
	registry := agentruntime.NewProviderRegistry()
	adapter := &testAdapter{
		providerID:      "openai",
		supportedModels: []string{"gpt-4o", "gpt-4o-mini"},
	}
	_ = registry.Register(adapter)

	credResolver := agentruntime.NewMockCredentialResolver()
	tenantID := uuid.New()
	credResolver.SetDefaultCredential(tenantID, "openai", "test-openai-api-key")

	client, err := agentruntime.NewResolvingLLMClient(registry, credResolver)
	if err != nil {
		t.Fatalf("unexpected client init error: %v", err)
	}

	req := agentruntime.LLMRequest{
		Prompt:   "Hello from ARDA",
		Provider: "openai",
		Model:    "gpt-4o",
		TenantID: tenantID,
	}

	resp, err := client.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected generate error: %v", err)
	}
	if resp.Content != "test adapter response" {
		t.Fatalf("unexpected response content: %q", resp.Content)
	}
	if adapter.capturedKey != "test-openai-api-key" {
		t.Fatalf("expected adapter to receive in-memory API key 'test-openai-api-key', got %q", adapter.capturedKey)
	}
}

func TestResolvingLLMClient_DefaultProviderAndModelFallback(t *testing.T) {
	registry := agentruntime.NewProviderRegistry()
	adapter := &testAdapter{
		providerID:      "openai",
		supportedModels: []string{"gpt-4o"},
	}
	_ = registry.Register(adapter)

	credResolver := agentruntime.NewMockCredentialResolver()
	tenantID := uuid.New()
	credResolver.SetDefaultCredential(tenantID, "openai", "fallback-key")

	client, err := agentruntime.NewResolvingLLMClient(
		registry,
		credResolver,
		agentruntime.WithDefaultProvider("openai"),
		agentruntime.WithResolvingDefaultModel("gpt-4o"),
	)
	if err != nil {
		t.Fatalf("unexpected client init error: %v", err)
	}

	// Request without Provider or Model should use defaults
	req := agentruntime.LLMRequest{
		Prompt:   "What is 2+2?",
		TenantID: tenantID,
	}

	resp, err := client.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected generate error: %v", err)
	}
	if resp.Model != "gpt-4o" {
		t.Fatalf("expected fallback model 'gpt-4o', got %q", resp.Model)
	}
	if adapter.lastReq.Model != "gpt-4o" {
		t.Fatalf("expected adapter to receive model 'gpt-4o', got %q", adapter.lastReq.Model)
	}
}

func TestResolvingLLMClient_UnsupportedProviderAndModel(t *testing.T) {
	registry := agentruntime.NewProviderRegistry()
	adapter := &testAdapter{
		providerID:      "openai",
		supportedModels: []string{"gpt-4o"},
	}
	_ = registry.Register(adapter)

	credResolver := agentruntime.NewMockCredentialResolver()
	tenantID := uuid.New()
	credResolver.SetDefaultCredential(tenantID, "openai", "key")

	client, err := agentruntime.NewResolvingLLMClient(registry, credResolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1. Unknown provider
	_, err = client.Generate(context.Background(), agentruntime.LLMRequest{
		Prompt:   "hello",
		Provider: "gemini",
		TenantID: tenantID,
	})
	if err == nil || !errors.Is(err, agentruntime.ErrUnsupportedProvider) {
		t.Fatalf("expected ErrUnsupportedProvider, got %v", err)
	}

	// 2. Unsupported model for registered provider
	_, err = client.Generate(context.Background(), agentruntime.LLMRequest{
		Prompt:   "hello",
		Provider: "openai",
		Model:    "unsupported-model-x",
		TenantID: tenantID,
	})
	if err == nil || !errors.Is(err, agentruntime.ErrUnsupportedModel) {
		t.Fatalf("expected ErrUnsupportedModel, got %v", err)
	}
}

func TestResolvingLLMClient_MissingCredentialAndTenant(t *testing.T) {
	registry := agentruntime.NewProviderRegistry()
	adapter := &testAdapter{providerID: "openai"}
	_ = registry.Register(adapter)

	credResolver := agentruntime.NewMockCredentialResolver()
	client, _ := agentruntime.NewResolvingLLMClient(registry, credResolver)

	// 1. Nil TenantID for external provider
	_, err := client.Generate(context.Background(), agentruntime.LLMRequest{
		Prompt:   "hello",
		Provider: "openai",
		TenantID: uuid.Nil,
	})
	if err == nil || !errors.Is(err, agentruntime.ErrInvalidTenant) {
		t.Fatalf("expected ErrInvalidTenant, got %v", err)
	}

	// 2. Tenant without configured credentials
	_, err = client.Generate(context.Background(), agentruntime.LLMRequest{
		Prompt:   "hello",
		Provider: "openai",
		TenantID: uuid.New(),
	})
	if err == nil || !errors.Is(err, agentruntime.ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound, got %v", err)
	}
}

func TestResolvingLLMClient_MockProviderNoCredentialsNeeded(t *testing.T) {
	registry := agentruntime.NewProviderRegistry()
	mockClient := agentruntime.NewMockLLMClient()
	_ = registry.Register(mockClient)

	// No credential resolver configured
	client, err := agentruntime.NewResolvingLLMClient(registry, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp, err := client.Generate(context.Background(), agentruntime.LLMRequest{
		Prompt:   "mock prompt",
		Provider: "mock",
	})
	if err != nil {
		t.Fatalf("unexpected error executing mock provider: %v", err)
	}
	if resp.Content != "deterministic mock llm response" {
		t.Fatalf("unexpected mock response content: %q", resp.Content)
	}
}

func TestResolvingLLMClient_ContextCancellation(t *testing.T) {
	registry := agentruntime.NewProviderRegistry()
	_ = registry.Register(&testAdapter{providerID: "openai"})
	credResolver := agentruntime.NewMockCredentialResolver()
	tenantID := uuid.New()
	credResolver.SetDefaultCredential(tenantID, "openai", "key")

	client, _ := agentruntime.NewResolvingLLMClient(registry, credResolver)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Generate(ctx, agentruntime.LLMRequest{
		Prompt:   "hello",
		Provider: "openai",
		TenantID: tenantID,
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
