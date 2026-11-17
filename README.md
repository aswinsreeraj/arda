# ARDA (Auditable, Reliable, Durable Agents)

ARDA is an enterprise governance platform for autonomous AI agent execution. It provides centralized API ingress, cryptographic secret storage in HashiCorp Vault (BYOK), immutable agent version lifecycles, Model Context Protocol (MCP) tool integration, deterministic guardrails, gRPC-based reliability calibration gating, and resilient workflow orchestration via Temporal.

---

## 1. System Architecture

```
                                      +---------------------------------------------+
                                      |            ARDA Frontend Console            |
                                      |   (React 19, TypeScript, TanStack Query)   |
                                      +---------------------------------------------+
                                                             | (HTTP / REST)
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
            v (Internal gRPC)                                v (Vault KV v2 API)                              v (Execution Dispatcher)
+------------------------------------+          +------------------------------------+          +------------------------------------+
|       Agent Registry Service       |          |      Vault Credential Service      |          |         Execution Service          |
|    - Version Lifecycle & Drafts    |          |    - Tenant Secret Isolation       |          |    - Realtime Direct Mode          |
|    - Immutable Published Snapshots |          |    - Zero Plaintext in Database    |          |    - Durable Temporal Workflows    |
+------------------------------------+          +------------------------------------+          +------------------------------------+
            |                                                |                                                |
            v (GORM Persistence)                             v (Path: secret/tenants/{id}/...)                v (SDK Dispatch)
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
                                                                                                                          ^
                                                                                                                          |
                                      +---------------------------------------------+                                     |
                                      |          ARDA Reliability Service           |                                     |
                                      |   - Python gRPC Service (Port 50053)        |-------------------------------------+
                                      |   - Precedent Similarity (pgvector HNSW)    |  (Calibration & Human Escalation)
                                      |   - Redpanda / Kafka Feedback Stream        |
                                      +---------------------------------------------+
```

---

## 2. Core Execution Modes

ARDA supports two execution modes designed for high reliability:

1. **`durable`**: Resilient workflow execution orchestrated by Temporal. Every turn, tool invocation, and decision is recorded as an immutable event. Workflows survive process restarts, node failures, and transient downstream outages.
2. **`realtime`**: Ultra-low-latency direct synchronous agent execution without Temporal overhead, ideal for interactive customer-facing assistants and fast classification.

Both modes require the **ARDA Reliability Service**. Every decision passes through the similarity calibration gate; low-confidence outcomes and periodic calibration samples ($1$ out of every $N$ autonomous executions) escalate to the Human-in-the-Loop review queue.

---

## 3. Core Services & Network Ports

| Service / Binary | Description | Default Port |
|---|---|---|
| [`cmd/gateway`](file:///home/aswin/second-project/arda/cmd/gateway) | API Gateway ingress, authentication, identity, and tool proxy | `8000` |
| [`cmd/registry`](file:///home/aswin/second-project/arda/cmd/registry) | Standalone Agent Registry gRPC service | `50051` |
| [`cmd/worker`](file:///home/aswin/second-project/arda/cmd/worker) | Temporal Background Worker running agent runtime workflows | Background |
| [`cmd/mcp-server`](file:///home/aswin/second-project/arda/cmd/mcp-server) | External financial transaction system simulator via MCP (SSE) | `8085` |
| [`cmd/admin`](file:///home/aswin/second-project/arda/cmd/admin) | Platform Super Admin CLI (`arda-admin`) | CLI |
| **ARDA Reliability Service** | Precedent similarity evaluation & calibration engine (Python gRPC) | `50053` |
| **ARDA Frontend Console** | Multi-tenant web console for operators, admins, developers, and reviewers | `5174` |
| **PostgreSQL (`arda`)** | Primary relational database for tenants, users, agents, and audits | `5432` |
| **PostgreSQL (`arda_reliability`)**| Vector database with pgvector HNSW index for calibration embeddings | `5433` |
| **HashiCorp Vault** | BYOK secret storage with tenant KV v2 paths | `8200` |
| **Temporal Server & Web UI** | Durable workflow orchestration engine & debug interface | `7233` / `8088` |
| **Redpanda** | High-performance distributed message broker for human feedback streaming | `9092` |

---

## 4. Role-Based Access Control (RBAC) Matrix

ARDA enforces strict tenant boundary isolation and least-privilege role boundaries across 4 operational roles:

| Resource Scope | `super_admin` | `company_admin` | `agent_developer` | `reviewer` |
|---|---|---|---|---|
| **Platform Clusters** (`/api/v1/platform/*`) | Full access | - | - | - |
| **Tenants** (`/api/v1/tenants`) | Full access | - | - | - |
| **Users & Members** (`/api/v1/users`, `/members`) | Full access | Full (own tenant) | Self profile (`/me`) | Self profile (`/me`) |
| **Agents & Versions** (`/api/v1/agents`) | Full access | Full (own tenant) | Full (own tenant) | Read-only |
| **Vault Credentials** (`/api/v1/credentials`) | Full access | Full (Create / Delete) | Read metadata only | - |
| **Tool Registry** (`/api/v1/tools`) | Full access | Full (Create / Update / Delete) | Read & Register | Read-only |
| **Review Queue** (`/api/v1/reviews`) | Full access | Read & Resolve | Read-only | Read & Resolve |
| **Execution Dispatch** (`/api/v1/agents/:id/execute`) | Full access | Full | Read & Execute | Read-only |

---

## 5. Getting Started

### 1. Prerequisites
- **Go**: 1.23 or newer
- **Python**: 3.11+
- **Node.js**: 20+
- **Docker & Docker Compose**: v2+

### 2. Launch Local Infrastructure
```bash
docker compose -f deployments/local/docker-compose.yml up -d
```

### 3. Run Database Migrations
```bash
./scripts/migrate.sh up
```

### 4. Build Binaries
```bash
go build -o bin/gateway ./cmd/gateway
go build -o bin/registry ./cmd/registry
go build -o bin/worker ./cmd/worker
go build -o bin/mcp-server ./cmd/mcp-server
go build -o bin/arda-admin ./cmd/admin
```

### 5. Platform Super Admin CLI (`arda-admin`)

Manage super administrators directly from the CLI:

```bash
# List all platform users and super_admin status
./bin/arda-admin list

# Create a new super administrator
./bin/arda-admin create superadmin@arda.security password123

# Set or reset a user's password
./bin/arda-admin passwd superadmin@arda.security password123

# Promote an existing user to super_admin
./bin/arda-admin promote ops-lead@arda.security
```

---

## 6. Testing & Quality Assurance

```bash
# Run all unit tests
go test ./...

# Run tests with race detector enabled
go test -race ./pkg/... ./services/...
```
