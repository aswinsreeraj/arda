package agentruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
)

func validConfiguration(promptTemplate string) domain.Configuration {
	return domain.Configuration{
		PromptTemplate:       promptTemplate,
		ExecutionMode:        domain.ExecutionModeAutonomous,
		ReliabilityThreshold: 0.9,
		Tools:                []string{},
	}
}

func setupExecutorTest() (
	*agentruntime.MockLLMClient,
	*agentruntime.MockConfigProvider,
	*agentruntime.LLMExecutor,
	agentruntime.ExecutionInput,
) {
	mockLLM := agentruntime.NewMockLLMClient()
	mockConfig := agentruntime.NewMockConfigProvider()

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	cfg := validConfiguration("System: Process request for {{input.customer_id}} in tier {{input.tier}}.")
	mockConfig.SetConfig(tenantID, agentID, versionID, cfg)

	exec, err := agentruntime.NewLLMExecutor(mockLLM, mockConfig)
	if err != nil {
		panic(err)
	}

	input := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		Payload: map[string]any{
			"customer_id": "cust_12345",
			"tier":        "enterprise",
		},
	}

	return mockLLM, mockConfig, exec, input
}

func TestLLMExecutor_Success(t *testing.T) {
	mockLLM, _, exec, input := setupExecutorTest()

	mockLLM.Response = agentruntime.LLMResponse{
		Content: "Customer processed successfully",
		Model:   "mock-model-v1",
		Usage: &agentruntime.TokenUsage{
			PromptTokens:     25,
			CompletionTokens: 10,
			TotalTokens:      35,
		},
	}

	res, err := exec.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("expected successful execution, got error: %v", err)
	}

	if res.Status != agentruntime.StatusSuccess {
		t.Errorf("expected status %s, got %s", agentruntime.StatusSuccess, res.Status)
	}

	if res.Output == nil {
		t.Fatalf("expected non-nil output")
	}

	if res.Output["content"] != "Customer processed successfully" {
		t.Errorf("unexpected output content: %v", res.Output["content"])
	}
	if res.Output["response"] != "Customer processed successfully" {
		t.Errorf("unexpected output response: %v", res.Output["response"])
	}
	if res.Output["model"] != "mock-model-v1" {
		t.Errorf("unexpected output model: %v", res.Output["model"])
	}

	usage, ok := res.Output["usage"].(map[string]any)
	if !ok || usage["total_tokens"] != 35 {
		t.Errorf("unexpected output usage: %+v", res.Output["usage"])
	}

	if mockLLM.Calls != 1 {
		t.Errorf("expected 1 LLM call, got %d", mockLLM.Calls)
	}
	expectedPrompt := "System: Process request for cust_12345 in tier enterprise."
	if mockLLM.LastRequest.Prompt != expectedPrompt {
		t.Errorf("expected prompt %q, got %q", expectedPrompt, mockLLM.LastRequest.Prompt)
	}
}

func TestLLMExecutor_ModelAndGenerationParameters(t *testing.T) {
	mockLLM := agentruntime.NewMockLLMClient()
	mockConfig := agentruntime.NewMockConfigProvider()

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	cfg := validConfiguration("Hello {{input.name}}")
	cfg.Guardrails = map[string]any{
		"model":       "claude-3-5-sonnet",
		"temperature": 0.7,
		"max_tokens":  json.Number("2048"),
		"top_p":       0.95,
		"stop":        []any{"STOP", "\n\n"},
	}

	mockConfig.SetConfig(tenantID, agentID, versionID, cfg)

	exec, err := agentruntime.NewLLMExecutor(mockLLM, mockConfig)
	if err != nil {
		t.Fatalf("unexpected error creating executor: %v", err)
	}

	input := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		Payload:        map[string]any{"name": "Alice"},
	}

	_, err = exec.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected execution error: %v", err)
	}

	req := mockLLM.LastRequest
	if req.Model != "claude-3-5-sonnet" {
		t.Errorf("expected model 'claude-3-5-sonnet', got %q", req.Model)
	}
	if req.Parameters == nil {
		t.Fatalf("expected non-nil parameters")
	}
	if req.Parameters.Temperature == nil || *req.Parameters.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", req.Parameters.Temperature)
	}
	if req.Parameters.MaxTokens == nil || *req.Parameters.MaxTokens != 2048 {
		t.Errorf("expected max_tokens 2048, got %v", req.Parameters.MaxTokens)
	}
	if req.Parameters.TopP == nil || *req.Parameters.TopP != 0.95 {
		t.Errorf("expected top_p 0.95, got %v", req.Parameters.TopP)
	}
	if len(req.Parameters.Stop) != 2 || req.Parameters.Stop[0] != "STOP" || req.Parameters.Stop[1] != "\n\n" {
		t.Errorf("expected stop sequences ['STOP', '\\n\\n'], got %v", req.Parameters.Stop)
	}
}

func TestLLMExecutor_FallbackModelAndBudgetMaxTokens(t *testing.T) {
	mockLLM := agentruntime.NewMockLLMClient()
	mockConfig := agentruntime.NewMockConfigProvider()

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	cfg := validConfiguration("Task: {{input.task}}")
	cfg.Budgets = map[string]any{
		"max_tokens": 512,
	}
	mockConfig.SetConfig(tenantID, agentID, versionID, cfg)

	exec, err := agentruntime.NewLLMExecutor(mockLLM, mockConfig, agentruntime.WithDefaultModel("gpt-4o-mini"))
	if err != nil {
		t.Fatalf("unexpected error creating executor: %v", err)
	}

	input := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		Payload:        map[string]any{"task": "summarize"},
	}

	_, err = exec.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected execution error: %v", err)
	}

	if mockLLM.LastRequest.Model != "gpt-4o-mini" {
		t.Errorf("expected default model 'gpt-4o-mini', got %q", mockLLM.LastRequest.Model)
	}
	if mockLLM.LastRequest.Parameters == nil || mockLLM.LastRequest.Parameters.MaxTokens == nil || *mockLLM.LastRequest.Parameters.MaxTokens != 512 {
		t.Errorf("expected max_tokens 512 from budgets, got %v", mockLLM.LastRequest.Parameters)
	}
}

func TestLLMExecutor_ExactVersionSelection(t *testing.T) {
	mockLLM := agentruntime.NewMockLLMClient()
	mockConfig := agentruntime.NewMockConfigProvider()

	tenantID := uuid.New()
	agentID := uuid.New()
	v1ID := uuid.New()
	v2ID := uuid.New()
	vUnconfiguredID := uuid.New()

	mockConfig.SetConfig(tenantID, agentID, v1ID, validConfiguration("v1 prompt for {{input.msg}}"))
	mockConfig.SetConfig(tenantID, agentID, v2ID, validConfiguration("v2 prompt for {{input.msg}}"))

	exec, err := agentruntime.NewLLMExecutor(mockLLM, mockConfig)
	if err != nil {
		t.Fatalf("unexpected error creating executor: %v", err)
	}

	// 1. Execute v1
	inputV1 := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: v1ID,
		Payload:        map[string]any{"msg": "test"},
	}
	_, err = exec.Execute(context.Background(), inputV1)
	if err != nil {
		t.Fatalf("unexpected error on v1: %v", err)
	}
	if mockLLM.LastRequest.Prompt != "v1 prompt for test" {
		t.Errorf("expected v1 prompt, got %q", mockLLM.LastRequest.Prompt)
	}
	qTenant, qAgent, qVer := mockConfig.LastQuery()
	if qTenant != tenantID || qAgent != agentID || qVer != v1ID {
		t.Errorf("expected query for v1 (%s), got %s", v1ID, qVer)
	}

	// 2. Execute v2
	inputV2 := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: v2ID,
		Payload:        map[string]any{"msg": "test"},
	}
	_, err = exec.Execute(context.Background(), inputV2)
	if err != nil {
		t.Fatalf("unexpected error on v2: %v", err)
	}
	if mockLLM.LastRequest.Prompt != "v2 prompt for test" {
		t.Errorf("expected v2 prompt, got %q", mockLLM.LastRequest.Prompt)
	}
	_, _, qVer = mockConfig.LastQuery()
	if qVer != v2ID {
		t.Errorf("expected query for v2 (%s), got %s", v2ID, qVer)
	}

	// 3. Execute unconfigured version
	inputUnconfigured := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: vUnconfiguredID,
		Payload:        map[string]any{"msg": "test"},
	}
	_, err = exec.Execute(context.Background(), inputUnconfigured)
	if err == nil {
		t.Fatalf("expected error for unconfigured version, got nil")
	}
	if !errors.Is(err, agentruntime.ErrConfigurationNotFound) {
		t.Errorf("expected ErrConfigurationNotFound, got %v", err)
	}
	if cat := agentruntime.ClassifyError(err); cat != agentruntime.CategoryPermanent {
		t.Errorf("expected CategoryPermanent for missing configuration, got %s", cat)
	}
}

func TestLLMExecutor_MissingConfiguration(t *testing.T) {
	mockLLM := agentruntime.NewMockLLMClient()
	mockConfig := agentruntime.NewMockConfigProvider()

	exec, err := agentruntime.NewLLMExecutor(mockLLM, mockConfig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := agentruntime.ExecutionInput{
		TenantID:       uuid.New(),
		AgentID:        uuid.New(),
		AgentVersionID: uuid.New(),
		Payload:        map[string]any{"key": "val"},
	}

	_, err = exec.Execute(context.Background(), input)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	if !errors.Is(err, agentruntime.ErrConfigurationNotFound) {
		t.Errorf("expected ErrConfigurationNotFound, got %v", err)
	}
	if cat := agentruntime.ClassifyError(err); cat != agentruntime.CategoryPermanent {
		t.Errorf("expected CategoryPermanent, got %s", cat)
	}
}

func TestLLMExecutor_InvalidConfiguration(t *testing.T) {
	mockLLM := agentruntime.NewMockLLMClient()
	mockConfig := agentruntime.NewMockConfigProvider()

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	// Invalid: reliability_threshold out of range [0.0, 1.0]
	cfg := domain.Configuration{
		PromptTemplate:       "Valid template",
		ExecutionMode:        domain.ExecutionModeAutonomous,
		ReliabilityThreshold: 2.5,
	}
	mockConfig.SetConfig(tenantID, agentID, versionID, cfg)

	exec, err := agentruntime.NewLLMExecutor(mockLLM, mockConfig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		Payload:        map[string]any{"key": "val"},
	}

	_, err = exec.Execute(context.Background(), input)
	if err == nil {
		t.Fatalf("expected error for invalid configuration, got nil")
	}

	if !errors.Is(err, agentruntime.ErrInvalidConfiguration) {
		t.Errorf("expected ErrInvalidConfiguration, got %v", err)
	}
	if cat := agentruntime.ClassifyError(err); cat != agentruntime.CategoryPermanent {
		t.Errorf("expected CategoryPermanent, got %s", cat)
	}
}

func TestLLMExecutor_MissingPromptPlaceholders(t *testing.T) {
	_, _, exec, input := setupExecutorTest()

	// Missing "tier" in payload
	input.Payload = map[string]any{
		"customer_id": "cust_12345",
	}

	_, err := exec.Execute(context.Background(), input)
	if err == nil {
		t.Fatalf("expected error for missing placeholder, got nil")
	}

	if !errors.Is(err, agentruntime.ErrMissingPlaceholderValue) {
		t.Errorf("expected ErrMissingPlaceholderValue, got %v", err)
	}
	if cat := agentruntime.ClassifyError(err); cat != agentruntime.CategoryInvalidInput {
		t.Errorf("expected CategoryInvalidInput, got %s", cat)
	}
}

func TestLLMExecutor_MalformedPromptTemplate(t *testing.T) {
	mockLLM := agentruntime.NewMockLLMClient()
	mockConfig := agentruntime.NewMockConfigProvider()

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	cfg := validConfiguration("Malformed {{input.unclosed")
	mockConfig.SetConfig(tenantID, agentID, versionID, cfg)

	exec, err := agentruntime.NewLLMExecutor(mockLLM, mockConfig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		Payload:        map[string]any{"unclosed": "val"},
	}

	_, err = exec.Execute(context.Background(), input)
	if err == nil {
		t.Fatalf("expected error for malformed template, got nil")
	}

	if !errors.Is(err, agentruntime.ErrMalformedPlaceholder) {
		t.Errorf("expected ErrMalformedPlaceholder, got %v", err)
	}
	if cat := agentruntime.ClassifyError(err); cat != agentruntime.CategoryInvalidInput {
		t.Errorf("expected CategoryInvalidInput, got %s", cat)
	}
}

func TestLLMExecutor_LLMProviderErrors(t *testing.T) {
	// 1. Transient LLM provider error
	transientErr := errors.New("upstream gateway timeout (HTTP 504)")
	mockLLM, _, exec, input := setupExecutorTest()
	mockLLM.Err = transientErr

	_, err := exec.Execute(context.Background(), input)
	if err == nil {
		t.Fatalf("expected provider error, got nil")
	}
	if !errors.Is(err, transientErr) {
		t.Errorf("expected error to wrap transientErr, got %v", err)
	}
	if cat := agentruntime.ClassifyError(err); cat != agentruntime.CategoryTransient {
		t.Errorf("expected transient error to classify as CategoryTransient, got %s", cat)
	}

	// 2. Permanent LLM error (e.g. non-retryable model failure)
	permanentErr := agentruntime.NewPermanentError("model permanently decommissioned", nil)
	mockLLM.Err = permanentErr

	_, err = exec.Execute(context.Background(), input)
	if err == nil {
		t.Fatalf("expected permanent provider error, got nil")
	}
	if !errors.Is(err, permanentErr) {
		t.Errorf("expected error to wrap permanentErr, got %v", err)
	}
	if cat := agentruntime.ClassifyError(err); cat != agentruntime.CategoryPermanent {
		t.Errorf("expected permanent error to classify as CategoryPermanent, got %s", cat)
	}
}

func TestLLMExecutor_ContextCancellationAndDeadlines(t *testing.T) {
	_, _, exec, input := setupExecutorTest()

	// 1. Pre-canceled context
	ctxCancel, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := exec.Execute(ctxCancel, input)
	if err == nil {
		t.Fatalf("expected context canceled error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if cat := agentruntime.ClassifyError(err); cat != agentruntime.CategoryCanceled {
		t.Errorf("expected CategoryCanceled, got %s", cat)
	}

	// 2. Expired deadline
	ctxDeadline, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancelDeadline()

	_, err = exec.Execute(ctxDeadline, input)
	if err == nil {
		t.Fatalf("expected deadline exceeded error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
	if cat := agentruntime.ClassifyError(err); cat != agentruntime.CategoryCanceled {
		t.Errorf("expected CategoryCanceled, got %s", cat)
	}
}

func TestLLMExecutor_NilAndInvalidDependencies(t *testing.T) {
	mockLLM := agentruntime.NewMockLLMClient()
	mockConfig := agentruntime.NewMockConfigProvider()

	// Nil LLM client
	_, err := agentruntime.NewLLMExecutor(nil, mockConfig)
	if !errors.Is(err, agentruntime.ErrNilLLMClient) {
		t.Errorf("expected ErrNilLLMClient, got %v", err)
	}

	// Nil Config provider
	_, err = agentruntime.NewLLMExecutor(mockLLM, nil)
	if !errors.Is(err, agentruntime.ErrNilConfigProvider) {
		t.Errorf("expected ErrNilConfigProvider, got %v", err)
	}

	// Uninitialized struct Execute
	uninit := &agentruntime.LLMExecutor{}
	_, err = uninit.Execute(context.Background(), agentruntime.ExecutionInput{
		TenantID:       uuid.New(),
		AgentID:        uuid.New(),
		AgentVersionID: uuid.New(),
	})
	if !errors.Is(err, agentruntime.ErrNilLLMClient) {
		t.Errorf("expected ErrNilLLMClient for uninitialized executor, got %v", err)
	}
}

func TestLLMExecutor_InputValidation(t *testing.T) {
	_, _, exec, input := setupExecutorTest()

	// Missing TenantID
	badInput := input
	badInput.TenantID = uuid.Nil
	_, err := exec.Execute(context.Background(), badInput)
	if !errors.Is(err, agentruntime.ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for missing tenant_id, got %v", err)
	}
	if cat := agentruntime.ClassifyError(err); cat != agentruntime.CategoryInvalidInput {
		t.Errorf("expected CategoryInvalidInput, got %s", cat)
	}

	// Missing AgentID
	badInput = input
	badInput.AgentID = uuid.Nil
	_, err = exec.Execute(context.Background(), badInput)
	if !errors.Is(err, agentruntime.ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for missing agent_id, got %v", err)
	}

	// Missing AgentVersionID
	badInput = input
	badInput.AgentVersionID = uuid.Nil
	_, err = exec.Execute(context.Background(), badInput)
	if !errors.Is(err, agentruntime.ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for missing agent_version_id, got %v", err)
	}
}

func TestLLMExecutor_AgentRuntimeIntegration(t *testing.T) {
	_, _, exec, input := setupExecutorTest()

	// 1. Using LLMExecutor directly as AgentRuntime via Run()
	resDirect, err := exec.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error running direct: %v", err)
	}
	if resDirect.Status != agentruntime.StatusSuccess {
		t.Errorf("expected status %s, got %s", agentruntime.StatusSuccess, resDirect.Status)
	}

	// 2. Wrapping LLMExecutor inside standard Runtime
	rt, err := agentruntime.New(exec)
	if err != nil {
		t.Fatalf("unexpected error wrapping in Runtime: %v", err)
	}

	resWrapped, err := rt.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error running wrapped: %v", err)
	}
	if resWrapped.Status != agentruntime.StatusSuccess {
		t.Errorf("expected status %s, got %s", agentruntime.StatusSuccess, resWrapped.Status)
	}
}

func TestLLMExecutor_WithResolvingClientAndVaultBYOK(t *testing.T) {
	memStore := vault.NewMemoryStore()
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	// Store BYOK secret in Vault
	_ = memStore.WriteSecret(context.Background(), tenantID, "custom/byok", map[string]string{
		"api_key": "vault-resolved-key-999",
	})

	vaultResolver, err := agentruntime.NewVaultCredentialResolver(memStore)
	if err != nil {
		t.Fatalf("unexpected resolver error: %v", err)
	}

	mockLLM := agentruntime.NewMockLLMClient()
	providerRegistry := agentruntime.NewProviderRegistry()
	_ = providerRegistry.Register(mockLLM)

	resolvingClient, err := agentruntime.NewResolvingLLMClient(providerRegistry, vaultResolver)
	if err != nil {
		t.Fatalf("unexpected client error: %v", err)
	}

	configProvider := agentruntime.NewMockConfigProvider()
	cfg := validConfiguration("Hello {{input.user}}!")
	cfg.LLMConfig = &domain.LLMConfig{
		Provider:      "mock",
		Model:         "mock-model-v2",
		CredentialRef: "secret:custom/byok#api_key",
	}
	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	executor, err := agentruntime.NewLLMExecutor(resolvingClient, configProvider)
	if err != nil {
		t.Fatalf("unexpected executor error: %v", err)
	}

	input := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
		Payload: map[string]any{
			"user": "Alice",
		},
	}

	res, err := executor.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	if res.Status != agentruntime.StatusSuccess {
		t.Errorf("expected success, got %s", res.Status)
	}
	if mockLLM.LastRequest.Prompt != "Hello Alice!" {
		t.Errorf("unexpected prompt: %q", mockLLM.LastRequest.Prompt)
	}
	if mockLLM.LastRequest.Provider != "mock" {
		t.Errorf("unexpected provider: %q", mockLLM.LastRequest.Provider)
	}
	if mockLLM.LastRequest.CredentialRef != "secret:custom/byok#api_key" {
		t.Errorf("unexpected credential ref: %q", mockLLM.LastRequest.CredentialRef)
	}
	if mockLLM.LastRequest.TenantID != tenantID {
		t.Errorf("unexpected tenant ID: %v", mockLLM.LastRequest.TenantID)
	}
}

func TestLLMExecutor_LLMConfigPrecedenceOverGuardrails(t *testing.T) {
	mockLLM := agentruntime.NewMockLLMClient()
	configProvider := agentruntime.NewMockConfigProvider()

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	tempExplicit := 0.1
	maxTokensExplicit := 500

	cfg := validConfiguration("Test prompt")
	// Legacy settings in Guardrails
	cfg.Guardrails = map[string]any{
		"provider":    "legacy-provider",
		"model":       "legacy-model",
		"temperature": 0.9,
		"max_tokens":  2000,
	}
	// Typed settings in LLMConfig
	cfg.LLMConfig = &domain.LLMConfig{
		Provider:      "openai",
		Model:         "gpt-4o",
		CredentialRef: "secret:credentials/llm/openai#api_key",
		Parameters: map[string]any{
			"temperature": tempExplicit,
			"max_tokens":  maxTokensExplicit,
		},
	}

	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	executor, err := agentruntime.NewLLMExecutor(mockLLM, configProvider)
	if err != nil {
		t.Fatalf("unexpected executor error: %v", err)
	}

	input := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
	}

	_, err = executor.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}

	// Verify that LLMConfig values won precedence over Guardrails
	if mockLLM.LastRequest.Provider != "openai" {
		t.Errorf("expected provider 'openai', got %q", mockLLM.LastRequest.Provider)
	}
	if mockLLM.LastRequest.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", mockLLM.LastRequest.Model)
	}
	if mockLLM.LastRequest.Parameters == nil || *mockLLM.LastRequest.Parameters.Temperature != 0.1 {
		t.Errorf("expected temperature 0.1, got %v", mockLLM.LastRequest.Parameters)
	}
	if mockLLM.LastRequest.Parameters == nil || *mockLLM.LastRequest.Parameters.MaxTokens != 500 {
		t.Errorf("expected max_tokens 500, got %v", mockLLM.LastRequest.Parameters)
	}
}

func TestLLMExecutor_LegacyGuardrailsCompatibility(t *testing.T) {
	mockLLM := agentruntime.NewMockLLMClient()
	configProvider := agentruntime.NewMockConfigProvider()

	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	cfg := validConfiguration("Test prompt")
	// Only Guardrails, no LLMConfig
	cfg.Guardrails = map[string]any{
		"provider":       "openai",
		"model":          "gpt-4-turbo",
		"credential_ref": "secret:legacy/key#api_key",
	}

	configProvider.SetConfig(tenantID, agentID, versionID, cfg)

	executor, err := agentruntime.NewLLMExecutor(mockLLM, configProvider)
	if err != nil {
		t.Fatalf("unexpected executor error: %v", err)
	}

	input := agentruntime.ExecutionInput{
		TenantID:       tenantID,
		AgentID:        agentID,
		AgentVersionID: versionID,
	}

	_, err = executor.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}

	if mockLLM.LastRequest.Provider != "openai" {
		t.Errorf("expected provider 'openai', got %q", mockLLM.LastRequest.Provider)
	}
	if mockLLM.LastRequest.Model != "gpt-4-turbo" {
		t.Errorf("expected model 'gpt-4-turbo', got %q", mockLLM.LastRequest.Model)
	}
	if mockLLM.LastRequest.CredentialRef != "secret:legacy/key#api_key" {
		t.Errorf("expected credential ref 'secret:legacy/key#api_key', got %q", mockLLM.LastRequest.CredentialRef)
	}
}
