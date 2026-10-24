package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aswinsreeraj/arda/pkg/agentruntime"
	"github.com/aswinsreeraj/arda/pkg/mcp"
	"github.com/aswinsreeraj/arda/pkg/mcp/simulator"
	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/pkg/toolgateway"
	"github.com/aswinsreeraj/arda/services/execution"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	gatewayhttp "github.com/aswinsreeraj/arda/services/gateway/http"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	"github.com/aswinsreeraj/arda/services/registry/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
)

// e2eWorkflowDispatcher wraps Temporal TestSuite to execute workflows synchronously
// and deterministically without requiring an external Temporal cluster.
type e2eWorkflowDispatcher struct {
	suite      *testsuite.WorkflowTestSuite
	activities *temporal.AgentActivities
	lastEnv    *testsuite.TestWorkflowEnvironment
	lastInput  temporal.AgentExecutionInput
	lastWfID   string
	lastRunID  string
	calls      int
	mu         sync.Mutex
}

func newE2EWorkflowDispatcher(activities *temporal.AgentActivities) *e2eWorkflowDispatcher {
	return &e2eWorkflowDispatcher{
		suite:      &testsuite.WorkflowTestSuite{},
		activities: activities,
	}
}

func (d *e2eWorkflowDispatcher) StartAgentExecutionWorkflow(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls++
	d.lastWfID = workflowID
	d.lastInput = input
	d.lastRunID = "run-" + uuid.New().String()

	env := d.suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(temporal.AgentExecutionWorkflow)
	if d.activities != nil {
		env.RegisterActivity(d.activities.AgentExecutionActivity)
	}

	env.ExecuteWorkflow(temporal.AgentExecutionWorkflow, input)
	d.lastEnv = env

	return &mockWorkflowRun{
		workflowID: workflowID,
		runID:      d.lastRunID,
	}, nil
}

// e2eEnvironment bundles all connected ARDA components for end-to-end verification.
type e2eEnvironment struct {
	TenantID       uuid.UUID
	AgentID        uuid.UUID
	VersionID      uuid.UUID
	DevToken       string
	GatewayServer  *httptest.Server
	Simulator      *simulator.TransactionSimulator
	MCPManager     *mcp.ConnectionManager
	ToolStore      *toolgateway.MemoryStore
	ToolRegistry   *toolgateway.DefaultRegistry
	ToolGateway    *toolgateway.Gateway
	ConfigProvider *agentruntime.MockConfigProvider
	MockLLM        *agentruntime.MockLLMClient
	Dispatcher     *e2eWorkflowDispatcher
}

func setupE2EEnvironment(t *testing.T, maxReversalGuardrail float64) *e2eEnvironment {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	jwtCfg := getTestConfig()
	val, err := auth.NewTokenValidator(jwtCfg)
	require.NoError(t, err)

	devToken, err := auth.GenerateTestToken(auth.Identity{
		UserID:   "dev-e2e-user",
		TenantID: tenantID,
		Role:     auth.RoleAgentDeveloper,
	}, jwtCfg, time.Hour)
	require.NoError(t, err)

	// 1. In-process Transaction Simulator MCP server
	sim := simulator.NewTransactionSimulator(tenantID)
	mgr := mcp.NewConnectionManager(mcp.ManagerConfig{AllowLocalhost: true})
	connID := "mcp-tx-conn"
	err = mgr.RegisterInProcessServer(connID, tenantID, sim.Server())
	require.NoError(t, err)

	// 2. Tool Gateway & Registry
	toolStore := toolgateway.NewMemoryStore()
	toolReg := toolgateway.NewRegistry(toolStore)
	toolHandlers := toolgateway.NewHandlerRegistry()

	readHandlerID := mcp.FormatHandlerID(connID, "get_transaction")
	toolHandlers.Register(readHandlerID, mcp.NewToolHandler(mgr, connID, "get_transaction"))

	writeHandlerID := mcp.FormatHandlerID(connID, "reverse_transaction")
	toolHandlers.Register(writeHandlerID, mcp.NewToolHandler(mgr, connID, "reverse_transaction"))

	ctx := context.Background()
	_, err = toolReg.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "get_transaction",
		Description:    "Retrieve details of a synthetic transaction",
		Classification: toolgateway.ClassificationRead,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"transaction_id": map[string]any{"type": "string"},
				"auth_token":     map[string]any{"type": "string"},
			},
			"required": []any{"transaction_id"},
		},
		HandlerID: readHandlerID,
	})
	require.NoError(t, err)

	_, err = toolReg.RegisterTool(ctx, toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "reverse_transaction",
		Description:    "Reverse an existing settled transaction",
		Classification: toolgateway.ClassificationWrite,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"transaction_id": map[string]any{"type": "string"},
				"amount":         map[string]any{"type": "number"},
				"reason":         map[string]any{"type": "string"},
				"api_key":        map[string]any{"type": "string"},
			},
			"required": []any{"transaction_id", "amount", "reason"},
		},
		HandlerID: writeHandlerID,
	})
	require.NoError(t, err)

	guardrail := toolgateway.NewCompositeGuardrail(
		toolgateway.NewDestructiveOperationGuardrail(),
		toolgateway.NewParameterBoundsGuardrail(maxReversalGuardrail),
	)

	configProvider := agentruntime.NewMockConfigProvider()
	configProvider.SetConfig(tenantID, agentID, versionID, domain.Configuration{
		PromptTemplate: "Execute transaction task for {{input.transaction_id}}",
		Tools:          []string{"get_transaction", "reverse_transaction"},
		ExecutionMode:  domain.ExecutionModeAutonomous,
	})

	toolGw := toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry:        toolReg,
		Store:           toolStore,
		Guardrail:       guardrail,
		HandlerRegistry: toolHandlers,
		ConfigProvider:  configProvider,
		AuditLogger:     toolgateway.NewStoreAuditLogger(toolStore),
	})

	// 3. Agent Runtime with LLMExecutor
	mockLLM := agentruntime.NewMockLLMClient()
	executor, err := agentruntime.NewLLMExecutor(mockLLM, configProvider, agentruntime.WithToolGateway(toolGw))
	require.NoError(t, err)

	rt, err := agentruntime.New(executor)
	require.NoError(t, err)

	activities := temporal.NewAgentActivities(rt)
	dispatcher := newE2EWorkflowDispatcher(activities)

	// 4. Execution Service
	regClient := execution.RegistryClientFunc(func(ctx context.Context, tid, aid, vid uuid.UUID) (*domain.AgentVersion, error) {
		if tid != tenantID || aid != agentID || vid != versionID {
			return nil, domain.NewNotFoundError("agent version not found or tenant mismatch")
		}
		return &domain.AgentVersion{
			ID:            versionID,
			AgentID:       agentID,
			VersionNumber: 1,
			Published:     true,
			Configuration: domain.Configuration{
				ExecutionMode: domain.ExecutionModeAutonomous,
			},
		}, nil
	})

	execSvc, err := execution.NewService(dispatcher, regClient)
	require.NoError(t, err)

	execClient := proxy.NewInProcessExecutionClient(execSvc)

	// 5. API Gateway
	gw := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator:    val,
		RateLimiter:     ratelimit.NewLimiter(ratelimit.Config{Enabled: false}),
		ExecutionClient: execClient,
	})

	gwServer := httptest.NewServer(gw)

	return &e2eEnvironment{
		TenantID:       tenantID,
		AgentID:        agentID,
		VersionID:      versionID,
		DevToken:       devToken,
		GatewayServer:  gwServer,
		Simulator:      sim,
		MCPManager:     mgr,
		ToolStore:      toolStore,
		ToolRegistry:   toolReg,
		ToolGateway:    toolGw,
		ConfigProvider: configProvider,
		MockLLM:        mockLLM,
		Dispatcher:     dispatcher,
	}
}

// -----------------------------------------------------------------------------
// Objective 1: Full Execution Path -> Schema-Valid Structured Decision
// -----------------------------------------------------------------------------
func TestE2E_FullExecutionPath_StructuredDecision(t *testing.T) {
	env := setupE2EEnvironment(t, 1000.0)
	defer env.GatewayServer.Close()

	// Define strict Draft-07 structured output schema
	decisionSchema := map[string]any{
		"$schema":  "http://json-schema.org/draft-07/schema#",
		"type":     "object",
		"required": []any{"approved", "transaction_id", "risk_score", "assessment"},
		"properties": map[string]any{
			"approved":       map[string]any{"type": "boolean"},
			"transaction_id": map[string]any{"type": "string"},
			"risk_score":     map[string]any{"type": "number"},
			"assessment":     map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}

	env.ConfigProvider.SetConfig(env.TenantID, env.AgentID, env.VersionID, domain.Configuration{
		PromptTemplate: "Analyze transaction {{input.transaction_id}} for query {{input.query}}",
		Tools:          []string{"get_transaction"},
		ExecutionMode:  domain.ExecutionModeAutonomous,
		OutputSchema: &domain.OutputSchema{
			Name:   "TransactionRiskDecision",
			Strict: true,
			Schema: decisionSchema,
		},
	})

	// Configure mock LLM:
	// Turn 1: requests get_transaction tool
	// Turn 2: receives tool output from MCP simulator and outputs schema-valid structured JSON decision
	env.MockLLM.GenerateFunc = func(ctx context.Context, req agentruntime.LLMRequest) (agentruntime.LLMResponse, error) {
		if !json.Valid([]byte(req.Prompt)) && req.Prompt != "" {
			// Check if prompt has tool result from Turn 1
			if !json.Valid([]byte(req.Prompt)) {
				// Continue
			}
		}

		// If prompt contains tool result, return final structured decision
		if req.Prompt != "" && (containsSubstring(req.Prompt, "[Tool Result for get_transaction]:")) {
			return agentruntime.LLMResponse{
				Content: `{"approved": true, "transaction_id": "tx_1001", "risk_score": 0.05, "assessment": "Low risk recurring subscription charge"}`,
				Model:   "mock-decision-model",
				Usage: &agentruntime.TokenUsage{
					PromptTokens:     80,
					CompletionTokens: 25,
					TotalTokens:      105,
				},
			}, nil
		}

		// Initial turn: request get_transaction tool call
		return agentruntime.LLMResponse{
			Content: `{"action": "tool_call", "tool_name": "get_transaction", "arguments": {"transaction_id": "tx_1001"}}`,
			Model:   "mock-decision-model",
			Usage: &agentruntime.TokenUsage{
				PromptTokens:     45,
				CompletionTokens: 20,
				TotalTokens:      65,
			},
		}, nil
	}

	// 1. Submit execution request via API Gateway
	payload := map[string]any{
		"agent_version_id": env.VersionID.String(),
		"execution_id":     "exec-e2e-decision-1",
		"input": map[string]any{
			"transaction_id": "tx_1001",
			"query":          "Review CloudHost charge for anomalies",
		},
	}

	resp, body := doRequest(env.GatewayServer, "POST", fmt.Sprintf("/api/v1/agents/%s/execute", env.AgentID), env.DevToken, nil, payload)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, "expected 202 Accepted: %s", string(body))

	var execResp gatewayhttp.ExecuteAgentResponse
	err := json.Unmarshal(body, &execResp)
	require.NoError(t, err)
	assert.Equal(t, "ACCEPTED", execResp.Status)
	assert.Equal(t, env.VersionID, execResp.AgentVersionID)
	assert.Equal(t, fmt.Sprintf("agent-exec-%s-exec-e2e-decision-1", env.AgentID), execResp.WorkflowID)

	// 2. Verify Temporal workflow completed synchronously in test environment
	require.NotNil(t, env.Dispatcher.lastEnv, "workflow environment must have run")
	require.True(t, env.Dispatcher.lastEnv.IsWorkflowCompleted(), "workflow must have completed")
	require.NoError(t, env.Dispatcher.lastEnv.GetWorkflowError(), "workflow must not return error")

	var wfResult temporal.AgentExecutionResult
	err = env.Dispatcher.lastEnv.GetWorkflowResult(&wfResult)
	require.NoError(t, err)

	// 3. Verify workflow result and structured output
	assert.Equal(t, "SUCCESS", wfResult.Status)
	assert.Equal(t, env.TenantID, wfResult.TenantID)
	assert.Equal(t, env.AgentID, wfResult.AgentID)
	assert.Equal(t, env.VersionID, wfResult.AgentVersionID)
	require.NotNil(t, wfResult.StructuredOutput, "structured output must be populated")

	structuredMap, ok := wfResult.StructuredOutput.(map[string]any)
	require.True(t, ok, "structured output must be a JSON object map")
	assert.Equal(t, true, structuredMap["approved"])
	assert.Equal(t, "tx_1001", structuredMap["transaction_id"])
	assert.Equal(t, 0.05, structuredMap["risk_score"])
	assert.Equal(t, "Low risk recurring subscription charge", structuredMap["assessment"])

	// 4. Verify simulator received exactly one call for get_transaction
	assert.Equal(t, 1, env.Simulator.CallCount("get_transaction"), "MCP simulator must have been called exactly once")

	// 5. Verify audit log completeness
	auditEvents, err := env.ToolStore.ListAuditEvents(context.Background(), env.TenantID, 10)
	require.NoError(t, err)
	require.Len(t, auditEvents, 1, "exactly one tool audit event must be recorded")
	assert.True(t, auditEvents[0].Allowed)
	assert.Equal(t, "get_transaction", auditEvents[0].ToolName)
	assert.Equal(t, "tx_1001", auditEvents[0].SanitizedArguments["transaction_id"])
}

// -----------------------------------------------------------------------------
// Objective 2: Prohibited Write Blocked by Hard Guardrail Before MCP Server
// -----------------------------------------------------------------------------
func TestE2E_ProhibitedWrite_BlockedByHardGuardrail(t *testing.T) {
	// Hard threshold set to $500.0 max reversal
	env := setupE2EEnvironment(t, 500.0)
	defer env.GatewayServer.Close()

	ctx := context.Background()

	// tx_1003 has settled amount of $1200.0
	txBefore, err := env.Simulator.GetTransaction("tx_1003")
	require.NoError(t, err)
	require.Equal(t, 1200.0, txBefore.Amount)
	require.Equal(t, "SETTLED", txBefore.Status)

	env.ConfigProvider.SetConfig(env.TenantID, env.AgentID, env.VersionID, domain.Configuration{
		PromptTemplate: "Execute reversal for {{input.transaction_id}}",
		Tools:          []string{"reverse_transaction"},
		ExecutionMode:  domain.ExecutionModeAutonomous,
	})

	// Direct Tool Gateway boundary test (and runtime propagation)
	// Request reversal of $1200.0 exceeding the $500.0 hard guardrail limit
	req := agentruntime.ToolExecutionRequest{
		TenantID:       env.TenantID,
		AgentID:        env.AgentID,
		AgentVersionID: env.VersionID,
		ExecutionID:    "exec-guardrail-blocked-e2e",
		ToolName:       "reverse_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1003",
			"amount":         1200.0,
			"reason":         "unauthorized_charge",
		},
	}

	resp, err := env.ToolGateway.ExecuteTool(ctx, req)
	require.NoError(t, err)

	// 1. Assert Tool Gateway blocked execution
	assert.False(t, resp.Allowed, "Tool Gateway must deny prohibited write")
	assert.Equal(t, toolgateway.DenialCodeGuardrailDenied, resp.DenialCode)
	assert.Contains(t, resp.Error, "exceeds maximum allowed threshold of 500.00")

	// 2. VERIFIABLE PROOF: Assert zero downstream calls to MCP simulator
	assert.Equal(t, 0, env.Simulator.CallCount("reverse_transaction"),
		"MCP simulator must receive strictly ZERO downstream calls when blocked by guardrail")

	// 3. VERIFIABLE PROOF: Assert unchanged simulator state
	txAfter, err := env.Simulator.GetTransaction("tx_1003")
	require.NoError(t, err)
	assert.Equal(t, "SETTLED", txAfter.Status, "transaction status must remain completely unchanged as SETTLED")
	assert.Equal(t, 1200.0, txAfter.Amount)

	// 4. Assert audit event recorded denial with reason
	events, err := env.ToolStore.ListAuditEvents(ctx, env.TenantID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.False(t, events[0].Allowed)
	assert.Equal(t, toolgateway.DenialCodeGuardrailDenied, events[0].DenialCode)
	assert.Equal(t, "exec-guardrail-blocked-e2e", events[0].ExecutionID)
	assert.Equal(t, "reverse_transaction", events[0].ToolName)
}

// -----------------------------------------------------------------------------
// Objective 3: Immutable Version Pinning and Tenant Anti-Forgery / Isolation
// -----------------------------------------------------------------------------
func TestE2E_VersionPinningAndTenantIsolation(t *testing.T) {
	tenantA := uuid.New()
	tenantB := uuid.New()
	agentID := uuid.New()
	v1ID := uuid.New()
	v2ID := uuid.New()

	jwtCfg := getTestConfig()
	val, err := auth.NewTokenValidator(jwtCfg)
	require.NoError(t, err)

	tokenA, err := auth.GenerateTestToken(auth.Identity{
		UserID:   "dev-tenant-a",
		TenantID: tenantA,
		Role:     auth.RoleAgentDeveloper,
	}, jwtCfg, time.Hour)
	require.NoError(t, err)

	var recordedVersionID uuid.UUID
	var recordedTenantID uuid.UUID

	regClient := execution.RegistryClientFunc(func(ctx context.Context, tid, aid, vid uuid.UUID) (*domain.AgentVersion, error) {
		recordedTenantID = tid
		recordedVersionID = vid
		if tid != tenantA {
			return nil, domain.NewNotFoundError("tenant does not own agent")
		}
		if aid != agentID {
			return nil, domain.NewNotFoundError("agent not found")
		}
		if vid == v1ID {
			return &domain.AgentVersion{
				ID:            v1ID,
				AgentID:       agentID,
				VersionNumber: 1,
				Published:     false,
			}, nil
		}
		if vid == v2ID {
			return &domain.AgentVersion{
				ID:            v2ID,
				AgentID:       agentID,
				VersionNumber: 2,
				Published:     true,
			}, nil
		}
		return nil, domain.NewNotFoundError("version not found")
	})

	var wfInput temporal.AgentExecutionInput
	dispatcher := &mockTemporalWorkflowDispatcher{
		startFn: func(ctx context.Context, workflowID string, input temporal.AgentExecutionInput) (client.WorkflowRun, error) {
			wfInput = input
			return &mockWorkflowRun{workflowID: workflowID, runID: "run-pinning"}, nil
		},
	}

	execSvc, err := execution.NewService(dispatcher, regClient)
	require.NoError(t, err)
	execClient := proxy.NewInProcessExecutionClient(execSvc)

	gw := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator:    val,
		RateLimiter:     ratelimit.NewLimiter(ratelimit.Config{Enabled: false}),
		ExecutionClient: execClient,
	})
	server := httptest.NewServer(gw)
	defer server.Close()

	t.Run("1. execution strictly pinned to explicit immutable version (v1) even when v2 published", func(t *testing.T) {
		payload := map[string]any{
			"agent_version_id": v1ID.String(),
			"execution_id":     "exec-pin-v1",
		}

		resp, body := doRequest(server, "POST", fmt.Sprintf("/api/v1/agents/%s/execute", agentID), tokenA, nil, payload)
		require.Equal(t, http.StatusAccepted, resp.StatusCode, "got: %s", string(body))

		var execResp gatewayhttp.ExecuteAgentResponse
		_ = json.Unmarshal(body, &execResp)
		assert.Equal(t, v1ID, execResp.AgentVersionID, "response must return requested v1")
		assert.Equal(t, v1ID, recordedVersionID, "registry must query requested v1")
		assert.Equal(t, v1ID, wfInput.AgentVersionID, "temporal workflow must be pinned to v1")
	})

	t.Run("2. tenant identity cannot be forged via X-Tenant-ID header", func(t *testing.T) {
		payload := map[string]any{
			"agent_version_id": v1ID.String(),
			"execution_id":     "exec-anti-spoof",
		}

		// Attacker attempts to spoof Tenant B
		headers := map[string]string{
			"X-Tenant-ID": tenantB.String(),
		}

		resp, _ := doRequest(server, "POST", fmt.Sprintf("/api/v1/agents/%s/execute", agentID), tokenA, headers, payload)
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		assert.Equal(t, tenantA, recordedTenantID, "verified JWT tenant identity must override spoofed header")
		assert.Equal(t, tenantA, wfInput.TenantID, "workflow tenant context must be pinned to authentic tenant")
	})

	t.Run("3. cross-tenant execution is rejected by execution service", func(t *testing.T) {
		tokenB, _ := auth.GenerateTestToken(auth.Identity{
			UserID:   "dev-tenant-b",
			TenantID: tenantB,
			Role:     auth.RoleAgentDeveloper,
		}, jwtCfg, time.Hour)

		payload := map[string]any{
			"agent_version_id": v1ID.String(),
		}

		// Tenant B attempts to execute Tenant A's agent
		resp, body := doRequest(server, "POST", fmt.Sprintf("/api/v1/agents/%s/execute", agentID), tokenB, nil, payload)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "cross-tenant access must return 404 Not Found")

		errResp := parseErrorResponse(t, body)
		assert.Equal(t, "NOT_FOUND", errResp.Error.Code)
	})
}

// -----------------------------------------------------------------------------
// Objective 4: Repeated and Concurrent Tool Invocations & Idempotency Safety
// -----------------------------------------------------------------------------
func TestE2E_RepeatedAndConcurrentToolInvocations(t *testing.T) {
	env := setupE2EEnvironment(t, 1000.0)
	defer env.GatewayServer.Close()

	ctx := context.Background()

	t.Run("1. repeated sequential invocations return cached result without duplicate execution", func(t *testing.T) {
		execID := "exec-repeated-write-1"

		req := agentruntime.ToolExecutionRequest{
			TenantID:       env.TenantID,
			AgentID:        env.AgentID,
			AgentVersionID: env.VersionID,
			ExecutionID:    execID,
			ToolName:       "reverse_transaction",
			Arguments: map[string]any{
				"transaction_id": "tx_1002",
				"amount":         42.50,
				"reason":         "customer_return",
			},
		}

		// First call: executes on MCP server
		resp1, err := env.ToolGateway.ExecuteTool(ctx, req)
		require.NoError(t, err)
		assert.True(t, resp1.Allowed)
		assert.Equal(t, 1, env.Simulator.CallCount("reverse_transaction"))

		// Second call: identical arguments and execution ID
		resp2, err := env.ToolGateway.ExecuteTool(ctx, req)
		require.NoError(t, err)
		assert.True(t, resp2.Allowed)
		assert.Equal(t, resp1.Output, resp2.Output)

		// Assert MCP simulator was NOT called a second time (cached idempotency)
		assert.Equal(t, 1, env.Simulator.CallCount("reverse_transaction"),
			"MCP simulator must NOT be invoked a second time on repeated idempotent call")
	})

	t.Run("2. concurrent invocations are synchronized by process-local lock and executed once", func(t *testing.T) {
		// Reset simulator calls
		env.Simulator.Reset()

		execID := "exec-concurrent-write-1"
		concurrency := 8
		var wg sync.WaitGroup
		wg.Add(concurrency)

		responses := make([]agentruntime.ToolExecutionResponse, concurrency)
		errorsList := make([]error, concurrency)

		for i := 0; i < concurrency; i++ {
			idx := i
			go func() {
				defer wg.Done()
				r, err := env.ToolGateway.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
					TenantID:       env.TenantID,
					AgentID:        env.AgentID,
					AgentVersionID: env.VersionID,
					ExecutionID:    execID,
					ToolName:       "reverse_transaction",
					Arguments: map[string]any{
						"transaction_id": "tx_1001",
						"amount":         150.00,
						"reason":         "concurrent_test",
					},
				})
				responses[idx] = r
				errorsList[idx] = err
			}()
		}

		wg.Wait()

		// Verify every concurrent caller succeeded without race conditions
		for i := 0; i < concurrency; i++ {
			require.NoError(t, errorsList[i], "goroutine %d failed", i)
			assert.True(t, responses[i].Allowed, "goroutine %d was not allowed", i)
		}

		// VERIFIABLE PROOF: MCP server was invoked strictly ONCE!
		assert.Equal(t, 1, env.Simulator.CallCount("reverse_transaction"),
			"MCP simulator must be invoked strictly once across concurrent identical requests")

		// Verify transaction state
		tx, err := env.Simulator.GetTransaction("tx_1001")
		require.NoError(t, err)
		assert.Equal(t, "REVERSED", tx.Status)
	})

	// DOCUMENTATION NOTE ON IDEMPOTENCY GUARANTEES:
	// ARDA's IdempotencyLockManager provides process-local mutex synchronization and store-backed caching
	// per (tenant, execution_id, tool_name, argument_hash).
	// If a worker crashes after the downstream MCP server executes a side effect but before ARDA marks
	// the invocation StatusCompleted, exactly-once semantics depends on downstream support (e.g. downstream
	// idempotency keys). ARDA does not claim unassisted distributed exactly-once side effects without downstream backend support.
}

// -----------------------------------------------------------------------------
// Objective 5: Bounded Agent Iterations and Token Budgets
// -----------------------------------------------------------------------------
func TestE2E_BoundedAgentLoop_BudgetsAndTokenAccounting(t *testing.T) {
	tenantID := uuid.New()
	agentID := uuid.New()
	versionID := uuid.New()

	configProvider := agentruntime.NewMockConfigProvider()
	store := toolgateway.NewMemoryStore()
	registry := toolgateway.NewRegistry(store)
	handlers := toolgateway.NewHandlerRegistry()

	// Register dummy read tool
	handlerID := "builtin:echo"

	_, err := registry.RegisterTool(context.Background(), toolgateway.ToolDefinition{
		TenantID:       tenantID,
		Name:           "ping_tool",
		Classification: toolgateway.ClassificationRead,
		InputSchema: map[string]any{
			"type": "object",
		},
		HandlerID: handlerID,
	})
	require.NoError(t, err)

	toolGw := toolgateway.NewGateway(toolgateway.GatewayConfig{
		Registry:        registry,
		Store:           store,
		HandlerRegistry: handlers,
		ConfigProvider:  configProvider,
	})

	t.Run("1. iteration budget strictly terminates infinite tool looping", func(t *testing.T) {
		configProvider.SetConfig(tenantID, agentID, versionID, domain.Configuration{
			PromptTemplate: "Loop: {{input.query}}",
			Tools:          []string{"ping_tool"},
			ExecutionMode:  domain.ExecutionModeAutonomous,
			Budgets: map[string]any{
				"max_iterations":                  3,
				"max_consecutive_identical_calls": 5,
			},
		})

		mockLLM := agentruntime.NewMockLLMClient()
		callCount := 0
		// Return different tool call arguments per iteration so repeated detection is not tripped
		mockLLM.GenerateFunc = func(ctx context.Context, req agentruntime.LLMRequest) (agentruntime.LLMResponse, error) {
			callCount++
			return agentruntime.LLMResponse{
				Content: fmt.Sprintf(`{"action": "tool_call", "name": "ping_tool", "arguments": {"step": %d}}`, callCount),
				Usage:   &agentruntime.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			}, nil
		}

		executor, err := agentruntime.NewLLMExecutor(mockLLM, configProvider, agentruntime.WithToolGateway(toolGw))
		require.NoError(t, err)

		rt, err := agentruntime.New(executor)
		require.NoError(t, err)

		_, err = rt.Run(context.Background(), agentruntime.ExecutionInput{
			TenantID:       tenantID,
			AgentID:        agentID,
			AgentVersionID: versionID,
			Payload:        map[string]any{"query": "bounded-loop-test"},
		})

		require.Error(t, err)
		assert.True(t, errors.Is(err, agentruntime.ErrBudgetExceeded), "expected ErrBudgetExceeded, got: %v", err)
		assert.Contains(t, err.Error(), "iteration budget exceeded")
	})

	t.Run("2. unexpectedly high token usage terminates loop immediately", func(t *testing.T) {
		configProvider.SetConfig(tenantID, agentID, versionID, domain.Configuration{
			PromptTemplate: "Summarize: {{input.query}}",
			ExecutionMode:  domain.ExecutionModeAutonomous,
			Budgets: map[string]any{
				"max_total_tokens": 500,
			},
		})

		mockLLM := agentruntime.NewMockLLMClient()
		mockLLM.Response = agentruntime.LLMResponse{
			Content: `{"result": "partial"}`,
			Usage: &agentruntime.TokenUsage{
				PromptTokens:     200,
				CompletionTokens: 400,
				TotalTokens:      600, // Exceeds 500 limit
			},
		}

		executor, err := agentruntime.NewLLMExecutor(mockLLM, configProvider, agentruntime.WithToolGateway(toolGw))
		require.NoError(t, err)

		rt, err := agentruntime.New(executor)
		require.NoError(t, err)

		_, err = rt.Run(context.Background(), agentruntime.ExecutionInput{
			TenantID:       tenantID,
			AgentID:        agentID,
			AgentVersionID: versionID,
			Payload:        map[string]any{"query": "token-test"},
		})

		require.Error(t, err)
		assert.True(t, errors.Is(err, agentruntime.ErrBudgetExceeded), "expected ErrBudgetExceeded, got: %v", err)
		assert.Contains(t, err.Error(), "token budget exceeded")
	})

	t.Run("3. missing provider usage (nil Usage) handled gracefully without panic", func(t *testing.T) {
		configProvider.SetConfig(tenantID, agentID, versionID, domain.Configuration{
			PromptTemplate: "Analyze: {{input.query}}",
			ExecutionMode:  domain.ExecutionModeAutonomous,
			Budgets: map[string]any{
				"max_iterations": 2,
			},
		})

		mockLLM := agentruntime.NewMockLLMClient()
		mockLLM.GenerateFunc = func(ctx context.Context, req agentruntime.LLMRequest) (agentruntime.LLMResponse, error) {
			return agentruntime.LLMResponse{
				Content: `{"decision": "completed"}`,
				Usage:   nil, // Missing token usage from provider
			}, nil
		}

		executor, err := agentruntime.NewLLMExecutor(mockLLM, configProvider, agentruntime.WithToolGateway(toolGw))
		require.NoError(t, err)

		rt, err := agentruntime.New(executor)
		require.NoError(t, err)

		res, err := rt.Run(context.Background(), agentruntime.ExecutionInput{
			TenantID:       tenantID,
			AgentID:        agentID,
			AgentVersionID: versionID,
			Payload:        map[string]any{"query": "missing-usage-test"},
		})

		require.NoError(t, err, "missing token usage must not cause runtime panic")
		assert.Equal(t, agentruntime.StatusSuccess, res.Status)
	})
}

// -----------------------------------------------------------------------------
// Objective 6: Audit Completeness and Secret Redaction Across All Outcomes
// -----------------------------------------------------------------------------
func TestE2E_AuditCompletenessAndSecretRedaction(t *testing.T) {
	env := setupE2EEnvironment(t, 500.0)
	defer env.GatewayServer.Close()

	ctx := context.Background()

	// 1. Successful read tool invocation with secret token
	_, err := env.ToolGateway.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       env.TenantID,
		AgentID:        env.AgentID,
		AgentVersionID: env.VersionID,
		ExecutionID:    "audit-exec-success",
		ToolName:       "get_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1001",
			"auth_token":     "super-secret-mcp-bearer-token",
		},
	})
	require.NoError(t, err)

	// 2. Denied write tool invocation with secret api_key
	_, err = env.ToolGateway.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       env.TenantID,
		AgentID:        env.AgentID,
		AgentVersionID: env.VersionID,
		ExecutionID:    "audit-exec-denied",
		ToolName:       "reverse_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1003",
			"amount":         1200.0, // Exceeds $500.0 guardrail
			"reason":         "suspected_fraud",
			"api_key":        "sk-live-confidential-secret-key",
		},
	})
	require.NoError(t, err)

	// 3. Failed MCP invocation (reversing an already reversed transaction) with secret password
	// First reverse tx_1002 legitimately
	_, err = env.ToolGateway.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       env.TenantID,
		AgentID:        env.AgentID,
		AgentVersionID: env.VersionID,
		ExecutionID:    "audit-exec-setup",
		ToolName:       "reverse_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1002",
			"amount":         42.50,
			"reason":         "first_reversal",
		},
	})
	require.NoError(t, err)

	// Attempting a second reversal with different execution ID causes MCP simulator to return error
	failResp, err := env.ToolGateway.ExecuteTool(ctx, agentruntime.ToolExecutionRequest{
		TenantID:       env.TenantID,
		AgentID:        env.AgentID,
		AgentVersionID: env.VersionID,
		ExecutionID:    "audit-exec-failed",
		ToolName:       "reverse_transaction",
		Arguments: map[string]any{
			"transaction_id": "tx_1002",
			"amount":         42.50,
			"reason":         "second_reversal_attempt",
			"private_key":    "my-confidential-rsa-private-key",
		},
	})
	require.NoError(t, err)
	assert.False(t, failResp.Allowed)
	assert.Equal(t, toolgateway.DenialCodeExecutionFailed, failResp.DenialCode)

	// Verify all audit events were saved
	events, err := env.ToolStore.ListAuditEvents(ctx, env.TenantID, 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events), 3)

	eventByExec := make(map[string]toolgateway.AuditEvent)
	for _, ev := range events {
		eventByExec[ev.ExecutionID] = ev
	}

	// Verify Successful Call Audit
	successEvent, ok := eventByExec["audit-exec-success"]
	require.True(t, ok)
	assert.True(t, successEvent.Allowed)
	assert.Equal(t, "tx_1001", successEvent.SanitizedArguments["transaction_id"])
	assert.Equal(t, "[REDACTED]", successEvent.SanitizedArguments["auth_token"],
		"auth_token MUST be redacted in successful call audit logs")

	// Verify Denied Call Audit
	deniedEvent, ok := eventByExec["audit-exec-denied"]
	require.True(t, ok)
	assert.False(t, deniedEvent.Allowed)
	assert.Equal(t, toolgateway.DenialCodeGuardrailDenied, deniedEvent.DenialCode)
	assert.Equal(t, "tx_1003", deniedEvent.SanitizedArguments["transaction_id"])
	assert.Equal(t, "[REDACTED]", deniedEvent.SanitizedArguments["api_key"],
		"api_key MUST be redacted in denied call audit logs")

	// Verify Failed Call Audit
	failedEvent, ok := eventByExec["audit-exec-failed"]
	require.True(t, ok)
	assert.False(t, failedEvent.Allowed)
	assert.Equal(t, toolgateway.DenialCodeExecutionFailed, failedEvent.DenialCode)
	assert.Equal(t, "[REDACTED]", failedEvent.SanitizedArguments["private_key"],
		"private_key MUST be redacted in failed call audit logs")
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > 0 && len(sub) > 0 && stringContains(s, sub)))
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
