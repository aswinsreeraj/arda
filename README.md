# ARDA (Auditable, Reliable, Durable Agents)

ARDA is a multi-tenant enterprise platform for governed AI agent execution. It provides centralized API ingress, tenant boundary isolation, cryptographic secret storage in HashiCorp Vault, immutable agent version lifecycles, Model Context Protocol (MCP) tool integration, deterministic guardrails, and durable workflow orchestration via Temporal.

---

## Current Project Status: Week 2 Integration Complete

The platform foundation and execution governance engine are fully implemented and verified:

- [x] **API Gateway Ingress & RBAC**: Centralized HTTP entrypoint with token-bucket rate limiting, JWT authentication, and strict tenant-scoped RBAC across 4 roles (`super_admin`, `company_admin`, `agent_developer`, `reviewer`).
- [x] **Agent Registry & Version Pinning**: Versioned agent definitions over gRPC and REST with immutable published snapshots (`domain.Configuration`) preventing version drift.
- [x] **BYOK Multi-Provider LLM Integration**: Tenant-isolated credentials stored in HashiCorp Vault KV v2. Supports 6 major providers:
  - **Anthropic** (`claude-3-5-sonnet`, `claude-3-haiku`)
  - **Google Gemini** (`gemini-1.5-pro`, `gemini-1.5-flash`)
  - **OpenAI** (`gpt-4o`, `gpt-4o-mini`, `o1`)
  - **Groq Cloud** (`llama-3.3-70b-versatile`, `mixtral-8x7b-32768`)
  - **xAI Grok** (`grok-beta`, `grok-2`)
  - **OpenRouter** (Multi-model routing gateway)
- [x] **Tool Registry & Policy Gateway**: Mandatory governance chokepoint. Enforces read/write classification, Draft-07 JSON Schema validation, tenant authorization, invocation budgets, and argument hashing for write idempotency.
- [x] **Deterministic Hard Guardrails**: Security and business rule policies evaluated by the Tool Gateway. Blocks unauthorized or destructive operations regardless of model confidence.
- [x] **Model Context Protocol (MCP) Integration**: Tenant-scoped MCP connections, tool discovery, and integration with an external transaction system simulator.
- [x] **Auditable Secret Redaction**: Real-time audit trails with automated masking (`[REDACTED]`) for sensitive keys, tokens, and authorization headers.
- [x] **Durable Temporal Orchestration**: Resilient agent execution workflows with bounded execution loops, token budgets, and live progress tracking.
- [x] **Interactive Dev UI Console**: Standalone web console (`dev-ui/`) for tenant self-service, BYOK credential management, tool registration, agent versioning, and live workflow execution.

---

## System Architecture

```
                                      +---------------------------------------------+
                                      |    Dev UI Console / External HTTP Client    |
                                      +---------------------------------------------+
                                                             | (HTTP / JSON)
                                                             v
                                      +---------------------------------------------+
                                      |                 API Gateway                 |
                                      |    - Token Bucket Rate Limiter              |
                                      |    - JWT Authentication (HMAC-SHA256)       |
                                      |    - Centralized RBAC Enforcement           |
                                      |    - Tenant Header Injection (X-Tenant-ID)  |
                                      +---------------------------------------------+
                                                             |
            +------------------------------------------------+------------------------------------------------+
            |                                                |                                                |
            v (Internal gRPC)                                v (Vault KV v2 API)                              v (HTTP / REST)
+------------------------------------+          +------------------------------------+          +------------------------------------+
|       Agent Registry Service       |          |      Vault Credential Service      |          |         Execution Service          |
|    - Version Lifecycle & Drafts    |          |    - Tenant Secret Isolation       |          |    - Version Pinning Verification  |
|    - Immutable Published Snapshots |          |    - Zero Plaintext in Database    |          |    - Temporal Workflow Dispatch    |
+------------------------------------+          +------------------------------------+          +------------------------------------+
            |                                                |                                                |
            v (GORM Persistence)                             v (Path: secret/tenants/{id}/...)                v (Temporal SDK)
+------------------------------------+          +------------------------------------+          +------------------------------------+
|        PostgreSQL Database         |          |       HashiCorp Vault KV v2        |          |          Temporal Server           |
|  - Tenants, Users, Memberships     |          +------------------------------------+          |  - Resilient Orchestration Engine  |
|  - Agents, Versions, Metadata      |                                                          +------------------------------------+
|  - Tool Definitions & Audit Logs   |                                                                        |
+------------------------------------+                                                                        v
                                                                                                +------------------------------------+
                                                                                                |          Temporal Worker           |
                                                                                                |    (Task Queue: arda-execution)    |
                                                                                                +------------------------------------+
                                                                                                              |
                                                                                                              v
                                                                                                +------------------------------------+
                                                                                                |           Agent Runtime            |
                                                                                                |  - Prompt Template Rendering       |
                                                                                                |  - Budget Tracker (Tokens, Iter)   |
                                                                                                |  - Output Schema Validation        |
                                                                                                +------------------------------------+
                                                                                                       |                      |
                                                          +--------------------------------------------+                      |
                                                          | (Provider Dispatch)                                               | (Authorized Tool Calls)
                                                          v                                                                   v
                                       +------------------------------------+                           +------------------------------------+
                                       |       Multi-Provider LLM Client    |                           |            Tool Gateway            |
                                       | - Anthropic / Google Gemini        |                           |  - Mandatory Governance Chokepoint |
                                       | - OpenAI / Groq / Grok / OpenRouter|                           |  - Hard Guardrail Policy Gate      |
                                       +------------------------------------+                           |  - JSON Schema Draft-07 Validation |
                                                                                                        |  - Write Idempotency & Concurrency |
                                                                                                        |  - Audit Log & Secret Redaction    |
                                                                                                        +------------------------------------+
                                                                                                                          |
                                                                                                                          v
                                                                                                        +------------------------------------+
                                                                                                        |       MCP Connection Manager       |
                                                                                                        |   - SSE / stdio Client Transport   |
                                                                                                        +------------------------------------+
                                                                                                                          |
                                                                                                                          v
                                                                                                        +------------------------------------+
                                                                                                        | Simulated MCP Transaction Server   |
                                                                                                        |  - get_transaction (Read)          |
                                                                                                        |  - reverse_transaction (Write)     |
                                                                                                        +------------------------------------+
```

---

## Core Services & Network Ports

| Service / Binary | Description | Default Port |
|---|---|---|
| [`cmd/gateway`](file:///home/aswin/second-project/arda/cmd/gateway) | Public API Gateway, Auth, RBAC, and Tool Management | `8000` |
| [`cmd/execution`](file:///home/aswin/second-project/arda/cmd/execution) | Execution Service and Workflow Dispatcher | `8081` |
| [`cmd/worker`](file:///home/aswin/second-project/arda/cmd/worker) | Temporal Background Worker running Agent Runtime workflows | Background |
| [`cmd/mcp-server`](file:///home/aswin/second-project/arda/cmd/mcp-server) | External Financial Transaction Simulator via MCP (SSE) | `8085` |
| [`cmd/registry`](file:///home/aswin/second-project/arda/cmd/registry) | Standalone Agent Registry gRPC Service | `50051` |
| [`dev-ui/`](file:///home/aswin/second-project/arda/dev-ui) | Interactive Web Development & Demonstration Console | `8080` |
| **PostgreSQL** | Primary Relational Persistence (GORM) | `5432` |
| **HashiCorp Vault** | Secure KV v2 Secret Storage | `8200` |
| **Temporal Server** | Durable Workflow Orchestration Engine | `7233` |
| **Temporal Web UI** | Real-time Workflow Visibility & Debugging UI | `8233` |

---

## Role-Based Access Control (RBAC) Matrix

ARDA enforces strict tenant boundary isolation and least-privilege role boundaries:

| Resource Group | `super_admin` | `company_admin` | `agent_developer` | `reviewer` |
|---|---|---|---|---|
| **Tenants** (`/api/v1/tenants`) | Full access | - | - | - |
| **Users & Members** (`/api/v1/users`, `/members`) | Full access | Full (own tenant) | Self profile (`/me`) | Self profile (`/me`) |
| **Agents & Versions** (`/api/v1/agents`) | Full access | Full (own tenant) | Full (own tenant) | Read-only |
| **Vault Credentials** (`/api/v1/credentials`) | Full access | Full (Create / Delete) | Read metadata only | - |
| **Tool Registry** (`/api/v1/tools`) | Full access | Full (Create / Update / Delete) | Read & Register | Read-only |
| **Audit Logs** (`/api/v1/tools/audit`) | Full access | Full (own tenant) | Read-only | Read-only |
| **Execution Dispatch** (`/api/v1/agents/:id/execute`) | Full access | Full | Read & Execute | Read-only |

---

## Getting Started

### 1. Prerequisites
- **Go**: 1.23 or newer
- **Docker & Docker Compose**: v2+
- **Git**

### 2. Start Local Infrastructure
Launch PostgreSQL, HashiCorp Vault, Temporal, and Temporal Web UI:
```bash
docker compose -f deployments/local/docker-compose.yml up -d
```

Verify service availability:
- **PostgreSQL**: `127.0.0.1:5432` (User: `arda_user`, Pass: `arda_password`, DB: `arda`)
- **HashiCorp Vault**: `http://127.0.0.1:8200` (Dev Token: `root`)
- **Temporal Server**: `127.0.0.1:7233`
- **Temporal Web UI**: [http://127.0.0.1:8233](http://127.0.0.1:8233)

### 3. Run Database Migrations
Apply versioned migrations (000001 through 000005):
```bash
./scripts/migrate.sh up
```

### 4. Start Core Services

**Terminal 1 — API Gateway:**
```bash
go run cmd/gateway/main.go
```
*Listens on `http://127.0.0.1:8000`.*

**Terminal 2 — Temporal Background Worker:**
```bash
go run cmd/worker/main.go
```
*Processes execution workflows on task queue `arda-execution-queue`.*

**Terminal 3 — Simulated MCP Transaction Server (Optional for MCP demos):**
```bash
go run cmd/mcp-server/main.go
```
*Listens on `http://127.0.0.1:8085`.*

**Terminal 4 — Interactive Dev Console UI:**
```bash
python3 -m http.server 8080 --directory dev-ui
```
*Open [http://localhost:8080](http://localhost:8080) in your browser.*

---

## Review Demonstration Walkthrough

You can perform the complete end-to-end product demonstration using either the **Dev UI Console** ([http://localhost:8080](http://localhost:8080)) or `curl`.

### Primary Demo Flow: Full Autonomous Execution
```
API Request -> Agent Registry -> Execution Service -> Temporal Worker -> Agent Runtime -> Vault LLM Key -> Tool Gateway -> Simulated MCP Server -> Structured Output
```

#### Step 1: Provision Tenant & Admin
```bash
curl -s -X POST http://localhost:8000/api/v1/tenants/register \
  -H "Content-Type: application/json" \
  -d '{
    "tenant_name": "Acme Fintech",
    "admin_email": "admin@acme.com",
    "admin_password": "AdminPassword123!",
    "admin_display_name": "Alice Admin"
  }'
```
*Stores the tenant and user atomically, returning an access JWT.*

Save the token:
```bash
export TOKEN="<jwt_token_from_response>"
```

#### Step 2: Store Tenant BYOK LLM Credential in Vault
```bash
curl -s -X POST http://localhost:8000/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "category": "llm",
    "provider": "groq",
    "secret_value": "gsk_your_groq_api_key"
  }'
```
*The raw key is written exclusively to Vault at `secret/tenants/{tenant_id}/credentials/llm/groq`. PostgreSQL stores only masked metadata (`gsk_...`).*

#### Step 3: Register an MCP Tool in the Tool Registry
```bash
curl -s -X POST http://localhost:8000/api/v1/tools \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "get_transaction",
    "description": "Retrieve financial transaction record via MCP",
    "classification": "read",
    "handler_id": "mcp:mcp-tx-conn:get_transaction",
    "input_schema": {
      "type": "object",
      "properties": {
        "transaction_id": { "type": "string" }
      },
      "required": ["transaction_id"],
      "additionalProperties": false
    }
  }'
```

#### Step 4: Create & Publish an Agent Version
```bash
# Create Agent
curl -s -X POST http://localhost:8000/api/v1/agents \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Transaction Dispute Investigator",
    "prompt_template": "You are a financial dispute investigator. Inspect transaction {{.transaction_id}} and make a determination.",
    "tools": ["get_transaction"],
    "llm_provider": "groq",
    "llm_model": "llama-3.3-70b-versatile",
    "execution_mode": "autonomous",
    "output_schema": {
      "type": "object",
      "required": ["decision", "confidence", "reasoning"],
      "properties": {
        "decision": { "type": "string", "enum": ["APPROVE", "REJECT", "ESCALATE"] },
        "confidence": { "type": "number", "minimum": 0, "maximum": 1 },
        "reasoning": { "type": "string" }
      }
    }
  }'

# Publish Version 1 (Immutable)
curl -s -X POST http://localhost:8000/api/v1/agents/<agent_id>/versions/<version_id>/publish \
  -H "Authorization: Bearer $TOKEN"
```

#### Step 5: Execute Agent Workflow with Version Pinning & Idempotency
```bash
curl -s -X POST http://localhost:8000/api/v1/agents/<agent_id>/execute \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "execution_id": "exec-dispute-001",
    "agent_version_id": "<version_id>",
    "input_payload": {
      "transaction_id": "tx_1001"
    }
  }'
```
*Returns HTTP 202 Accepted. Temporal executes the pinned version, queries the LLM using the Vault key, routes the tool call through the Tool Gateway to MCP, and validates the structured decision.*

#### Step 6: Verify Hard Guardrail Enforcement
When an agent attempts a prohibited action (e.g. attempting a write tool on a closed account or exceeding transaction thresholds), the Tool Gateway deterministic guardrail rejects the invocation regardless of model confidence:
```bash
curl -s -X GET http://localhost:8000/api/v1/tools/audit \
  -H "Authorization: Bearer $TOKEN"
```
*The audit log shows `allowed: false`, `denial_code: "GUARDRAIL_DENIED"`, and sensitive parameters redacted as `[REDACTED]`.*

---

## Testing & Quality Assurance

### Run Unit and Integration Tests
```bash
go test -count=1 ./...
```

### Run Tests with Race Detector
```bash
go test -race ./pkg/... ./services/...
```

### Build All Service Binaries
```bash
go build -o bin/gateway ./cmd/gateway
go build -o bin/worker ./cmd/worker
go build -o bin/execution ./cmd/execution
go build -o bin/mcp-server ./cmd/mcp-server
go build -o bin/registry ./cmd/registry
```

---

## Repository Structure

```
arda/
├── cmd/
│   ├── execution/        # Standalone Execution Service
│   ├── gateway/          # API Gateway ingress service
│   ├── mcp-server/       # Simulated external transaction system (MCP SSE)
│   ├── registry/         # Standalone Agent Registry gRPC service
│   ├── worker/           # Temporal background worker
│   └── workflow/         # CLI utility for workflow submission
├── deployments/
│   └── local/            # Docker Compose definitions (Postgres, Vault, Temporal)
├── dev-ui/               # Interactive Developer Console UI (HTML/CSS/JS)
├── docs/                 # Architecture Decision Records (ADRs) and specs
├── gen/                  # Generated Protobuf Go code (ardav1)
├── migrations/           # Numbered PostgreSQL migrations (000001–000005)
├── pkg/
│   ├── agentruntime/     # Agent Runtime, LLM Adapters (Anthropic, Gemini, OpenAI, Groq), Budgets
│   ├── database/         # PostgreSQL connection pool and GORM setup
│   ├── jsonschema/       # JSON Schema Draft-07 compiler and validator
│   ├── mcp/              # MCP Connection Manager, Client Transport, and Simulator
│   ├── temporal/         # Temporal Client, Workflow Definitions, and Activities
│   ├── toolgateway/      # Tool Gateway, Guardrails, Idempotency Locks, and Audit Logger
│   └── vault/            # HashiCorp Vault KV v2 tenant secret store client
├── proto/                # Protocol Buffer definitions for gRPC services
├── scripts/
│   └── migrate.sh        # Database migration runner script
└── services/
    ├── execution/        # Execution orchestration domain and HTTP handler
    ├── gateway/          # API Gateway router, authentication, identity, and proxy
    └── registry/         # Agent Registry domain logic, versioning, and storage
```