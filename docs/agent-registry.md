# Agent Registry v1 Documentation

Agent Registry v1 provides governed agent configuration management and immutable version history for the ARDA platform.

---

## Architecture Overview

The Agent Registry is implemented with a clear separation of concerns:

- **Domain Layer (`services/registry/domain`)**:
  Defines `Agent`, `AgentVersion`, and `Configuration` models, domain error codes, execution modes, and validation logic.
- **Store Layer (`services/registry/store`)**:
  Persistence abstraction with a PostgreSQL implementation using parameterized SQL queries. Maps PostgreSQL constraints and trigger errors to domain errors.
- **Service Layer (`services/registry/service`)**:
  Orchestrates agent and version lifecycles with strict tenant scoping. Prevents cross-tenant access and enforces version publishing rules.
- **HTTP Layer (`services/registry/http`)**:
  REST handlers, tenant context middleware, route registration, and standardized error responses.
- **Entrypoint (`cmd/registry/main.go`)**:
  Local service runner with graceful shutdown and configurable connection settings.

---

## Tenant Scoping & Security Boundary

- **Trusted Context**: The tenant ID is never accepted as an authoritative identity from an arbitrary request body. It is supplied via trusted application context.
- **Tenant Isolation**: Every database read and write explicitly filters by `tenant_id`. Unscoped lookups are never performed. If Tenant B queries an agent ID belonging to Tenant A, a `404 NOT_FOUND` is returned so existence is not revealed.
- **Authentication Gap & Remaining Work**:
  - The current service relies on an explicit `TenantResolver` interface and `TenantMiddleware`, reading from the `X-Tenant-ID` header for local development.
  - In production, this resolver will be replaced by API Gateway JWT validation middleware that cryptographically verifies tenant identity and injects it into context.

---

## Configuration & Supported Execution Modes

The versioned agent configuration (`Configuration`) is stored as PostgreSQL-native `JSONB`:

| Field | Type | Required | Description |
|---|---|---|---|
| `prompt_template` | `string` | Yes | System prompt instruction template (non-empty). |
| `tools` | `[]string` | Yes | List of allowed tool identifiers. |
| `reliability_threshold` | `float64` | Yes | Calibrated confidence gate threshold in `[0.0, 1.0]`. |
| `execution_mode` | `string` | Yes | Operating governance mode. |
| `guardrails` | `object` | No | Safety limits, token caps, or policy constraints. |
| `budgets` | `object` | No | Cost, execution, or token budgets. |

### Supported Execution Modes
- **`autonomous`**: Agent actions proceed without gate intervention if confidence meets threshold.
- **`supervised`**: Agent actions require supervisory approval or review.
- **`human_in_the_loop`**: Actions require direct human confirmation.
- **`gated`**: Evaluated against the reliability service contract before execution.

> [!IMPORTANT]
> **Credential Security**: Never store plaintext LLM API keys or MCP tokens in configuration fields. Store secret vault references (e.g., `secret:vault-key-id` or `env:VARIABLE_NAME`). The domain layer rejects configurations containing raw API tokens.

---

## Immutability Guarantees

1. **Database-Level Protection**: The PostgreSQL trigger `trg_agent_versions_immutability` blocks any `UPDATE` or `DELETE` on rows where `published = true`.
2. **Application-Level Protection**: The service layer returns `409 PUBLISHED_VERSION_IMMUTABLE` when attempting to edit or delete a published version.
3. **Draft Transition**: Drafts (`published = false`) may be edited and transitioned to published. Transitioning automatically records `published_at = CURRENT_TIMESTAMP`.
4. **Historical Preservation**: Publishing a newer version (e.g., v2) never overwrites or unpublishes older versions. Executions can reliably pin any exact `agent_version_id`.

---

## API Endpoints & Curl Examples

Set the base URL and tenant headers:
```bash
BASE_URL="http://127.0.0.1:8080"
TENANT_A="a0000000-0000-0000-0000-000000000001"
TENANT_B="b0000000-0000-0000-0000-000000000002"
```

### 1. Create an Agent (with initial draft version)
```bash
curl -X POST "${BASE_URL}/api/v1/agents" \
  -H "Content-Type: application/json" \
  -H "X-Tenant-ID: ${TENANT_A}" \
  -d '{
    "name": "Customer Support Agent",
    "prompt_template": "You are a customer support agent.",
    "tools": ["kb_search", "create_ticket"],
    "reliability_threshold": 0.85,
    "execution_mode": "supervised",
    "guardrails": {"max_tokens": 2048},
    "budgets": {"monthly_usd": 100.0}
  }'
```

### 2. Retrieve an Agent by ID
```bash
curl -X GET "${BASE_URL}/api/v1/agents/<AGENT_ID>" \
  -H "X-Tenant-ID: ${TENANT_A}"
```

*(If called with `X-Tenant-ID: ${TENANT_B}`, returns `404 Not Found`)*

### 3. Create a Draft Agent Version
```bash
curl -X POST "${BASE_URL}/api/v1/agents/<AGENT_ID>/versions" \
  -H "Content-Type: application/json" \
  -H "X-Tenant-ID: ${TENANT_A}" \
  -d '{
    "configuration": {
      "prompt_template": "Updated prompt v2.",
      "tools": ["kb_search", "escalate"],
      "reliability_threshold": 0.90,
      "execution_mode": "autonomous"
    }
  }'
```

### 4. Publish a Draft Version
```bash
curl -X POST "${BASE_URL}/api/v1/agents/<AGENT_ID>/versions/<VERSION_ID>/publish" \
  -H "X-Tenant-ID: ${TENANT_A}"
```

### 5. Resolve the Latest Published Version
```bash
curl -X GET "${BASE_URL}/api/v1/agents/<AGENT_ID>/versions/latest" \
  -H "X-Tenant-ID: ${TENANT_A}"
```

### 6. Verification of Immutability (Attempted Modification of Published Version)
```bash
curl -X PUT "${BASE_URL}/api/v1/agents/<AGENT_ID>/versions/<PUBLISHED_VERSION_ID>" \
  -H "Content-Type: application/json" \
  -H "X-Tenant-ID: ${TENANT_A}" \
  -d '{
    "prompt_template": "Tampered prompt",
    "tools": ["tamper"],
    "reliability_threshold": 0.5,
    "execution_mode": "autonomous"
  }'
```
*Response: `409 Conflict` (`PUBLISHED_VERSION_IMMUTABLE`)*
