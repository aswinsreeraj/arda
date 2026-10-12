# ARDA — Temporal Workflow Foundation v1 Documentation

This document describes the design, configuration, local infrastructure, and usage of the Temporal workflow foundation package (`pkg/temporal`) and CLI entrypoints for ARDA.

---

## 1. Architecture & Role in ARDA

Temporal serves as the **durable execution orchestrator** for ARDA. It coordinates:
- Stateful agent execution runs across distributed workers.
- Tool invocations and external API integrations with automatic retries and exponential backoff.
- Human-in-the-loop approval workflows and supervisory timeouts.
- Compensation and rollback flows during unexpected execution failures.

### Core Architectural Separation

1. **Workflow Layer (`pkg/temporal/workflows.go`)**:
   - Implements `NoopWorkflow`.
   - **Deterministic Guarantee**: Workflows strictly use Temporal workflow APIs (`workflow.ExecuteActivity`, `workflow.GetInfo`, `workflow.GetLogger`). Workflows **must never** use native Go timers, goroutines, channels, or network calls directly.
2. **Activity Layer (`pkg/temporal/activities.go`)**:
   - Implements `NoopActivity`.
   - Side-effects, network operations, database queries, and external tool calls belong strictly in activities.
3. **Worker Layer (`pkg/temporal/worker.go` & `cmd/worker`)**:
   - Connects to the Temporal frontend and polls the designated named task queue (`arda-v1-task-queue`).
   - Dispatches workflows and activities as work arrives.
4. **Client Layer (`pkg/temporal/client.go` & `cmd/workflow`)**:
   - Dispatches workflows with explicit workflow IDs and awaits execution results.

---

## 2. Configuration Parameters

| Environment Variable | Default Value | Description |
|---|---|---|
| `TEMPORAL_ADDRESS` | `127.0.0.1:7233` | Temporal frontend gRPC service address. |
| `TEMPORAL_NAMESPACE` | `default` | Registered Temporal namespace. |
| `TEMPORAL_TASK_QUEUE` | `arda-v1-task-queue` | Named task queue for workers and workflows. |
| `TEMPORAL_PORT` | `7233` | Host port mapped to Temporal frontend container. |
| `TEMPORAL_UI_PORT` | `8233` | Host port mapped to Temporal Web UI container. |

---

## 3. Local Infrastructure (Docker Compose)

Temporal Server is integrated into `deployments/local/docker-compose.yml`:

- **Container Image**: `temporalio/auto-setup:1.24.2`
- **Persistence Strategy**:
  - Connects to the local PostgreSQL container (`postgres`).
  - Automatically initializes dedicated `temporal` and `temporal_visibility` schemas via `temporal-sql-tool`.
  - **Database Isolation**: Completely separated from ARDA's application database (`arda`).
- **Health Check**:
  - Probes cluster health via `tctl --address 127.0.0.1:7233 cluster health`.
- **Temporal Web UI**:
  - `temporalio/ui:2.26.2` exposed at `http://127.0.0.1:8233` for visual inspection of workflow histories, activity states, and task queue pollers.
- **Security Boundary**:
  - All host ports are bound strictly to `127.0.0.1` (`127.0.0.1:7233` and `127.0.0.1:8233`). Never bound to `0.0.0.0`.

---

## 4. Durability & Error Handling

Workflows configure bounded timeouts and retry policies on activity invocations:

```go
activityOptions := workflow.ActivityOptions{
    StartToCloseTimeout: 10 * time.Second,
    RetryPolicy: &temporal.RetryPolicy{
        InitialInterval:        time.Second,
        BackoffCoefficient:    2.0,
        MaximumInterval:       10 * time.Second,
        MaximumAttempts:       3,
        NonRetryableErrorTypes: []string{"SimulatedActivityFailure"},
    },
}
```

- **Failures Propagated**: If an activity exhausts retries or returns a non-retryable application error, the error propagates back through the workflow execution and is surfaced to the client.
- **Fail-Closed on Unreachable Server**: Client operations fail immediately with descriptive connection errors if the Temporal server is offline. Success is never falsely reported.

---

## 5. Local Commands & Usage Examples

### 1. Start Infrastructure

Start PostgreSQL, Vault, and Temporal in the background:

```bash
docker compose -f deployments/local/docker-compose.yml up -d
```

Verify that all containers are healthy:

```bash
docker compose -f deployments/local/docker-compose.yml ps
```

### 2. Start the Temporal Worker

Start the worker process in a dedicated terminal:

```bash
go run ./cmd/worker/main.go
```

Output:
```
Starting ARDA Temporal Worker...
  Address:    127.0.0.1:7233
  Namespace:  default
  Task Queue: arda-v1-task-queue
ARDA Temporal Worker listening on task queue "arda-v1-task-queue". Press Ctrl+C to stop.
```

### 3. Dispatch a Workflow

In another terminal, trigger the workflow via the client CLI:

```bash
go run ./cmd/workflow/main.go -message "Hello ARDA Temporal"
```

Output:
```
Executing NoopWorkflow...
  Target Server: 127.0.0.1:7233 (namespace: default)
  Task Queue:    arda-v1-task-queue
  Workflow ID:   arda-noop-a1b2c3d4
  Payload:       "Hello ARDA Temporal"

=== Workflow Execution Result ===
Workflow ID:     arda-noop-a1b2c3d4
Run ID:          xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
Status:          SUCCESS
Activity Result: processed: Hello ARDA Temporal
Executed By:     NoopActivity
=================================
```

### 4. Inspect the Execution in Temporal Web UI

Open your browser to:
`http://127.0.0.1:8233`
Inspect the workflow run, task queue, event history, and activity completion details.

---

## 6. Testing

### In-Memory Unit Tests (`testsuite`)

Unit tests execute without any running Docker containers or external network dependencies using Temporal's Go SDK Test Environment (`go.temporal.io/sdk/testsuite`):

```bash
go test -v -count=1 ./pkg/temporal
```

Tests cover:
1. `TestNoopWorkflow_Success`: Full execution of `NoopWorkflow` dispatching `NoopActivity` and asserting deterministic result output.
2. `TestNoopWorkflow_ActivityFailurePropagation`: Simulating activity failure and verifying that workflow returns the error without masking.
3. `TestNoopActivity_DirectExecution`: Standalone activity execution and input validation.
4. `TestConfig_Defaults`: Verifying sensible development defaults.
5. `TestClient_UnavailableServerReportsError`: Verifying that attempting to connect to an offline endpoint surfaces an immediate error.

---

## 7. Remaining Limitations & Future Milestones

1. **Local Setup Scope**: `auto-setup` and single-node containers are strictly intended for local development. Production deployments require a managed Temporal Cloud namespace or clustered self-hosted deployment with mTLS.
2. **Deferred Capabilities**:
   - Dynamic agent step execution.
   - MCP tool activity runners.
   - Human-in-the-loop review signal/query handlers.
   - Reliability score gating activities.
