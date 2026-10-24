# Agent Registry v1 Documentation

Agent Registry v1 provides governed agent configuration management, immutable version history, and durable tenant isolation for the ARDA platform.

---

## 1. Architecture Overview

The Agent Registry is implemented with a clear separation of concerns across layered components:

- **Protobuf & gRPC Interface (`proto/arda/v1/agent.proto` & `services/registry/grpc`)**:
  Declares and implements the canonical internal service contract (`ardav1.AgentRegistryService`). Owns the internal service boundary behind the API Gateway.
- **Domain Layer (`services/registry/domain`)**:
  Defines `Agent`, `AgentVersion`, and `Configuration` models, domain error codes, execution modes, and business rules.
- **Store Layer (`services/registry/store`)**:
  Persistence abstraction backed by GORM and PostgreSQL. Maps PostgreSQL constraints and trigger errors to domain errors.
- **Service Layer (`services/registry/service`)**:
  Orchestrates agent and version lifecycles with strict tenant scoping. Enforces version numbering, publication, and draft modification rules.
- **HTTP Layer (`services/registry/http`)**:
  Gin-based HTTP router and handlers for standalone local development and backwards compatibility.
- **Entrypoint (`cmd/registry/main.go`)**:
  Runs both the gRPC server on `REGISTRY_GRPC_PORT` (default: 50051) and the HTTP server on `PORT` (default: 8080) with graceful shutdown.

---

## 2. Internal gRPC Service Boundary

In the ARDA platform architecture, external clients talk to the API Gateway over HTTP/JSON. The API Gateway communicates with the Agent Registry exclusively over gRPC:

```
External Client (HTTP/JSON)
      ↓
API Gateway (JWT Validation, RBAC, Rate Limiting)
      ↓ (gRPC + Metadata: x-tenant-id, x-user-id, x-role)
Agent Registry Server (InternalAuthInterceptor)
      ↓
Registry Service Layer
      ↓
GORM Persistence / PostgreSQL
```

### Supported RPC Methods (`ardav1.AgentRegistryService`)
1. `CreateAgent`: Creates an agent and optional initial draft version.
2. `GetAgent`: Retrieves agent detail by ID including latest published and draft versions.
3. `ListAgents`: Lists agents for the authenticated tenant with pagination (`page_size`, `page_token`).
4. `CreateAgentVersion`: Creates a new draft version for an agent.
5. `GetAgentVersion`: Retrieves a specific version of an agent.
6. `GetLatestPublishedVersion`: Resolves the latest published version of an agent.
7. `UpdateDraftVersion`: Updates an existing draft version's configuration. Rejects updates if published.
8. `PublishVersion`: Transitions a draft version to published (sets `published = true`, `published_at = now`).
9. `DeleteDraftVersion`: Deletes an unpublished draft version. Rejects deletion if published.

### Internal Authentication & Anti-Spoofing
- The Agent Registry never trusts `tenant_id` fields provided in incoming request bodies alone.
- The `InternalAuthInterceptor` inspects incoming gRPC metadata for `x-tenant-id`.
- The interceptor verifies that the authenticated caller identity matches any requested `tenant_id`. Any attempt to spoof or query another tenant's data fails with `codes.PermissionDenied`.

---

## 3. Configuration & Supported Execution Modes

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
> **Credential Security**: Never store plaintext LLM API keys or MCP tokens in configuration fields. Store secret vault references (e.g., `secret:credentials/llm/openai#api_key`). Raw secret values are rejected by validation.

---

## 4. Immutability Guarantees

1. **Database-Level Protection**: The PostgreSQL trigger `trg_agent_versions_immutability` blocks any `UPDATE` or `DELETE` on rows where `published = true`.
2. **Application-Level Protection**: The service layer returns `409 PUBLISHED_VERSION_IMMUTABLE` when attempting to edit or delete a published version.
3. **Draft Transition**: Drafts (`published = false`) may be edited and transitioned to published. Transitioning automatically records `published_at = CURRENT_TIMESTAMP`.
4. **Historical Preservation**: Publishing a newer version (e.g., v2) never overwrites or unpublishes older versions. Executions can reliably pin any exact `agent_version_id`.

---

## 5. Version Resolution Route Conventions

When accessing the registry through the API Gateway, the following route conventions are supported:

- **Canonical Route:**
  - `GET /api/v1/agents/:id/versions/latest`
- **Supported Direct Aliases:**
  - `GET /api/v1/agents/:id/versions/published`
  - `GET /api/v1/agents/:id/versions/published/latest`
- **Specific Version Route:**
  - `GET /api/v1/agents/:id/versions/:versionId` (validates UUID)

---

## 6. Running Locally & Testing

```bash
# Start required local infrastructure
docker compose -f deployments/local/docker-compose.yml up -d

# Run migrations
./scripts/migrate.sh up

# Run Agent Registry service (starts gRPC on :50051 and HTTP on :8080)
export GRPC_PORT="50051"
export PORT="8080"
export DATABASE_URL="postgres://arda_user:arda_password@127.0.0.1:5432/arda?sslmode=disable"

go run cmd/registry/main.go
```

---

## 7. Structured-Output Configuration (JSON Schema Draft-07)

Agents can optionally declare an `output_schema` to enforce schema-validated structured responses from LLM executions. When configured, ARDA's agent runtime verifies the model's completion against the contract and surfaces a parsed JSON object directly in the execution result.

### Supported JSON Schema Version & Features
ARDA implements a zero-dependency, safe validator adhering to **JSON Schema Draft-07** (`http://json-schema.org/draft-07/schema#`). Supported keywords include:
- **Core Types:** `object`, `array`, `string`, `number`, `integer`, `boolean`, `null` (supports union types like `["string", "null"]`).
- **Object Constraints:** `properties`, `required`, `additionalProperties` (boolean or sub-schema), `minProperties`, `maxProperties`, `patternProperties`.
- **Array Constraints:** `items` (single schema or tuple schema array), `additionalItems`, `minItems`, `maxItems`, `uniqueItems`.
- **String Constraints:** `minLength`, `maxLength`, `pattern` (bounded regex), `format`.
- **Numeric Constraints:** `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf`.
- **Value Matching:** `enum`, `const`.
- **Combinators:** `allOf`, `anyOf`, `oneOf`, `not`.
- **Local Definitions:** Local references to `#/definitions/...` and `#/$defs/...`.

### Security Bounds & DoS Guardrails
- **No Remote Network Fetching:** Remote `$ref` URIs (e.g., `http://`, `https://`, `ftp://`, or external files) are strictly forbidden and rejected at configuration time (`ErrRemoteRefForbidden`).
- **Schema Size Limit:** Schema definition must not exceed **64 KB** (`ErrSchemaTooLarge`).
- **Schema Depth Limit:** Nesting depth is capped at **10 levels** (`ErrSchemaTooDeep`).
- **Regex Length Limit:** Pattern expressions are limited to **250 characters** (`ErrInvalidRegex`).
- **Response Size Limit:** Model completions exceeding **1 MB** before JSON parsing are immediately rejected (`ErrOutputTooLarge`).
- **Strict Parsing:** Markdown code fences (e.g. ` ```json `) and trailing syntax are NOT silently stripped. Model outputs must be pure JSON.

### Configuration Example
```json
{
  "name": "SentimentAnalyzer",
  "prompt_template": "Classify the sentiment of: {{input.text}}. Output only JSON matching schema.",
  "execution_mode": "autonomous",
  "reliability_threshold": 0.9,
  "output_schema": {
    "name": "SentimentScore",
    "description": "Normalized sentiment label and numeric score",
    "strict": true,
    "schema": {
      "$schema": "http://json-schema.org/draft-07/schema#",
      "type": "object",
      "required": ["sentiment", "confidence", "tags"],
      "properties": {
        "sentiment": {
          "type": "string",
          "enum": ["positive", "neutral", "negative"]
        },
        "confidence": {
          "type": "number",
          "minimum": 0.0,
          "maximum": 1.0
        },
        "tags": {
          "type": "array",
          "items": { "type": "string" },
          "uniqueItems": true
        }
      },
      "additionalProperties": false
    }
  }
}
```

### Result Compatibility
For structured agents, the execution result contains:
```json
{
  "status": "SUCCESS",
  "structured_output": {
    "sentiment": "positive",
    "confidence": 0.98,
    "tags": ["service", "onboarding"]
  },
  "output": {
    "content": "{\"sentiment\": \"positive\", \"confidence\": 0.98, \"tags\": [\"service\", \"onboarding\"]}",
    "response": "{\"sentiment\": \"positive\", \"confidence\": 0.98, \"tags\": [\"service\", \"onboarding\"]}",
    "structured_output": {
      "sentiment": "positive",
      "confidence": 0.98,
      "tags": ["service", "onboarding"]
    }
  }
}
```

### Failure & Retry Semantics
- **Schema Violations (`ErrSchemaViolation`)** & **Malformed JSON (`ErrInvalidOutputJSON`)**: Categorized as `CategoryPermanent`. In Temporal, these immediately fail the execution with a non-retryable `PermanentExecutionError`, preventing infinite retry loops and runaway token costs.
- **Provider Failures (Rate limits 429, Server errors 503)**: Categorized as `CategoryTransient` and retried according to the bounded Temporal activity retry policy.
- **Backward Compatibility**: Agents without `output_schema` return standard string outputs in `Output["content"]` and `Output["response"]` without `structured_output`.

