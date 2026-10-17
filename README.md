# ARDA (Auditable, Reliable, Durable Agents)

ARDA is a secure, multi-tenant platform for governed AI agent execution. It provides centralized API ingress, identity and tenant boundary isolation, cryptographic secret storage, agent version lifecycles, and durable orchestration.

---

## Current Project Status: Week 1 Foundation

The platform foundation and Week 1 milestone requirements are fully implemented and verified:

- [x] **API Gateway Ingress**: Single public HTTP entrypoint with structured request logging, token-bucket rate limiting, and request routing.
- [x] **Multi-Tenant Identity & RBAC**: Self-service company registration, tenant context propagation (`X-Tenant-ID`), and strict role-based access control across 4 distinct roles.
- [x] **Agent Registry**: Versioned agent definition management over gRPC with published version immutability and JSONB schema validation.
- [x] **Secure Credential Management**: Tenant-isolated storage for LLM and MCP server secrets in HashiCorp Vault KV v2 (raw secrets never stored in PostgreSQL or exposed in logs/responses).
- [x] **Durable Workflow Orchestration**: Integration with Temporal Server for resilient agent execution lifecycles and background worker processing.
- [x] **Reproducible Schema Migrations**: Numbered PostgreSQL schema migrations (versions 1–5) managed via `golang-migrate` with automated Docker fallback.

---

## Architecture Overview

```
                                  +-----------------------------------------+
                                  |           External HTTP Client          |
                                  +-----------------------------------------+
                                                       | (HTTP / JSON)
                                                       v
                                  +-----------------------------------------+
                                  |               API Gateway               |
                                  |  - Token Bucket Rate Limiter (process)  |
                                  |  - JWT Authentication (HMAC-SHA256)     |
                                  |  - Centralized RBAC Enforcement         |
                                  |  - Tenant Header Injection              |
                                  +-----------------------------------------+
                                                       |
         +---------------------------------------------+---------------------------------------------+
         |                                             |                                             |
         v (Internal gRPC / Protobuf)                  v (Vault KV v2 API)                           v (Temporal SDK)
+-----------------------------------+         +----------------------------------+          +----------------------------------+
|       Agent Registry Service      |         |     Vault Credential Service     |          |       Temporal Orchestrator      |
|    (ardav1.AgentRegistryService)  |         |   (Save / List / Verify / Delete |          |    (Dispatches Agent Execution   |
|   Metadata: x-tenant-id,          |         |    Tenant-Isolated API Keys)     |          |     Workflow with pinned         |
|             x-user-id, x-role     |         +----------------------------------+          |     agent_version_id)            |
+-----------------------------------+                          |                            +----------------------------------+
                 |                                             v                                             |
                 v (GORM Persistence)         +----------------------------------+                           v
+-----------------------------------+         |       HashiCorp Vault KV v2      |          +----------------------------------+
|        PostgreSQL Database        |         |  (Path: secret/tenants/{id}/...) |          |          Temporal Worker         |
|   (Agents, Versions, Memberships) |         +----------------------------------+          |    (Task Queue: arda-execution)  |
+-----------------------------------+                                                       +----------------------------------+
```

---

## Core Components

| Binary / Package | Description | Default Port |
|---|---|---|
| [`cmd/gateway`](file:///home/aswin/second-project/arda/cmd/gateway) | Public HTTP API Gateway, authentication, RBAC, and routing | `8000` |
| [`cmd/registry`](file:///home/aswin/second-project/arda/cmd/registry) | Agent Registry service (standalone gRPC service) | `50051` |
| [`cmd/worker`](file:///home/aswin/second-project/arda/cmd/worker) | Temporal background worker processing agent workflows | Background |
| [`pkg/vault`](file:///home/aswin/second-project/arda/pkg/vault) | Tenant-namespaced client library for HashiCorp Vault KV v2 | `8200` |
| [`pkg/temporal`](file:///home/aswin/second-project/arda/pkg/temporal) | Temporal client wrapper and workflow definitions | `7233` |

---

## Role-Based Access Control (RBAC) Matrix

The platform strictly enforces tenant isolation and least-privilege access:

| Endpoint Group | `super_admin` | `company_admin` | `agent_developer` | `reviewer` |
|---|---|---|---|---|
| **Tenants** (`/api/v1/tenants`) | Full access | - | - | - |
| **Users** (`/api/v1/users`) | - | Full (own tenant) | Self only (`/me`) | Self only (`/me`) |
| **Members** (`/api/v1/tenants/:id/members`) | Full access | Full (own tenant) | - | - |
| **Agents** (`/api/v1/agents`) | - | Full | Full | Read-only |
| **Credentials** (`/api/v1/credentials`) | - | Full (Create / Delete) | Read metadata only | - |
| **Executions** (`/api/v1/agents/:id/execute`) | - | Read | Read + Execute | Read |

---

## Getting Started

### 1. Prerequisites
- **Go**: 1.23 or newer
- **Docker & Docker Compose**: v2+
- **Git**

### 2. Start Local Infrastructure
Start PostgreSQL, HashiCorp Vault, Temporal, and the Temporal Web UI:
```bash
docker compose -f deployments/local/docker-compose.yml up -d
```

Check service availability:
- **PostgreSQL**: `127.0.0.1:5432` (`arda_user` / `arda_password` / database `arda`)
- **HashiCorp Vault**: `http://127.0.0.1:8200` (Dev token: `root`)
- **Temporal Server**: `127.0.0.1:7233`
- **Temporal Web UI**: [http://127.0.0.1:8233](http://127.0.0.1:8233)

### 3. Run Database Migrations
Apply versioned schema migrations (1 through 5):
```bash
./scripts/migrate.sh up
```

To inspect migration status:
```bash
./scripts/migrate.sh version
```

### 4. Start Services

**Terminal 1 — API Gateway:**
```bash
go run cmd/gateway/main.go
```
The Gateway starts on port `8000` and automatically connects to PostgreSQL, Vault, and Temporal.

**Terminal 2 — Temporal Worker:**
```bash
go run cmd/worker/main.go
```
Listens on task queue `arda-execution-queue` to execute durable agent workflows.

---

## API Quickstart & Demo Flow

### 1. Register a Company Tenant & Admin
```bash
curl -s -X POST http://localhost:8000/api/v1/tenants/register \
  -H "Content-Type: application/json" \
  -d '{
    "tenant_name": "Acme Corp",
    "admin_email": "admin@acme.com",
    "admin_password": "SecurePassword123!",
    "admin_display_name": "Alice Admin"
  }'
```
*Response includes tenant ID, user ID, active `company_admin` membership, and an access JWT.*

### 2. Log In
```bash
curl -s -X POST http://localhost:8000/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "admin@acme.com",
    "password": "SecurePassword123!"
  }'
```

Save the returned token:
```bash
export TOKEN="<jwt_token_from_login_response>"
```

### 3. Create an Agent & Initial Version
```bash
curl -s -X POST http://localhost:8000/api/v1/agents \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Customer Support Triage",
    "prompt_template": "You are a customer support agent. Help resolve queries.",
    "tools": ["search_knowledge_base", "create_ticket"],
    "reliability_threshold": 0.85,
    "execution_mode": "supervised"
  }'
```

### 4. Publish Version 1 (Immutable)
```bash
curl -s -X POST http://localhost:8000/api/v1/agents/<agent_id>/versions/<version_id>/publish \
  -H "Authorization: Bearer $TOKEN"
```
*Once published, this version cannot be modified or deleted.*

### 5. Store an LLM Credential in Vault
```bash
curl -s -X POST http://localhost:8000/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "category": "llm",
    "provider": "openai",
    "secret_value": "sk-test-live-key"
  }'
```
*The raw secret is stored exclusively under `secret/tenants/{tenant_id}/llm/openai` in Vault. PostgreSQL only stores masked metadata.*

### 6. Execute Agent Workflow via Temporal
```bash
curl -s -X POST http://localhost:8000/api/v1/agents/<agent_id>/execute \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "input": "Summarize customer complaint #1029"
  }'
```
*Dispatches an asynchronous execution workflow to Temporal. Monitor live execution at [http://127.0.0.1:8233](http://127.0.0.1:8233).*

---

## Testing & Quality Assurance

### Run Unit and Integration Tests
```bash
go test -v ./...
```

### Run Tests with Race Detector
```bash
go test -race ./...
```

### Static Analysis
```bash
go vet ./...
```

### Standalone Live Verification Scripts
Located in `scripts/manual/` (ignored by Git, tagged with `//go:build ignore`):
```bash
# Verify live Vault storage, tenant isolation, and secret deletion
go run scripts/manual/verify_live_vault.go

# Verify live Temporal workflow submission and completion
go run scripts/manual/verify_live_temporal.go

# Verify full multi-tenant identity lifecycle
go run scripts/manual/verify_identity_live.go

# Run complete End-to-End acceptance suite
go run scripts/manual/verify_e2e_acceptance.go
```

---

## Repository Structure

```
arda/
├── cmd/
│   ├── gateway/          # API Gateway service entrypoint
│   ├── registry/         # Standalone Agent Registry gRPC service entrypoint
│   ├── worker/           # Temporal background worker entrypoint
│   └── workflow/         # CLI utility for workflow dispatch
├── deployments/
│   └── local/            # Docker Compose setup for PostgreSQL, Vault, and Temporal
├── docs/                 # Architecture Decision Records (ADRs) and Gateway specs
├── gen/                  # Generated Protobuf Go code (ardav1)
├── migrations/           # Versioned PostgreSQL schema migrations (000001–000005)
├── pkg/
│   ├── database/         # PostgreSQL connection pool and GORM configuration
│   ├── temporal/         # Temporal client wrapper and workflow definitions
│   └── vault/            # HashiCorp Vault KV v2 secret store interface and client
├── proto/                # Protocol Buffer definitions for inter-service gRPC
├── scripts/
│   ├── manual/           # Standalone developer acceptance verification scripts
│   └── migrate.sh        # Migration runner with Docker and CLI fallback
└── services/
    ├── gateway/          # API Gateway implementation (auth, proxy, ratelimit, identity)
    └── registry/         # Agent Registry domain logic, gRPC/HTTP handlers, and store
```