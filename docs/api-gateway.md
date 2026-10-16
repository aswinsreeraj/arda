# API Gateway v1 Documentation

API Gateway v1 acts as the governed single public HTTP entry point for the ARDA platform, enforcing JWT authentication, role-based authorization, tenant context isolation, process-local rate limiting, structured observability, and downstream gRPC routing.

---

## 1. Architecture

```
                                  +-----------------------------------------+
                                  |           External HTTP Client          |
                                  +-----------------------------------------+
                                                       | (HTTP / JSON)
                                                       v
                                  +-----------------------------------------+
                                  |            LoggingMiddleware            |
                                  |   (Request ID, latency, no token leaks) |
                                  +-----------------------------------------+
                                                       |
                                                       v
                                  +-----------------------------------------+
                                  |           RateLimitMiddleware           |
                                  |      (Token-bucket, 429 on exceed)      |
                                  +-----------------------------------------+
                                                       |
                                                       v
                                  +-----------------------------------------+
                                  |         AuthenticateMiddleware          |
                                  |   (HMAC-SHA256, claim validation, exp)  |
                                  +-----------------------------------------+
                                                       |
                                                       v
                                  +-----------------------------------------+
                                  |        RequireActionMiddleware          |
                                  |   (Centralized RBAC policy matrix)      |
                                  +-----------------------------------------+
                                                       |
         +---------------------------------------------+---------------------------------------------+
         |                                             |                                             |
         v (Internal gRPC + metadata)                  v (HTTP Service)                              v (Temporal SDK)
+-----------------------------------+         +----------------------------------+          +----------------------------------+
|       Agent Registry Service      |         |     Vault Credential Service     |          |       Temporal Orchestrator      |
|    (ardav1.AgentRegistryService)  |         |      (Save / List / Verify /     |          |    (Dispatches Agent Execution   |
|   Metadata: x-tenant-id,          |         |       Delete Mock Secrets)       |          |     Workflow with pinned         |
|             x-user-id, x-role     |         +----------------------------------+          |     agent_version_id)            |
+-----------------------------------+                          |                            +----------------------------------+
                 |                                             v                                             |
                 v (GORM Persistence)         +----------------------------------+                           v
+-----------------------------------+         |       HashiCorp Vault KV v2      |          +----------------------------------+
|        PostgreSQL Database        |         |  (Tenant-isolated mock secrets;  |          |          Worker Activities       |
|    (Agents, Versions, Metadata)   |         |   no raw secrets in DB or logs)  |          |   (Future Runtime Secret Resol.) |
+-----------------------------------+         +----------------------------------+          +----------------------------------+
```

---

## 2. Authentication

- **Library**: Standard library [`github.com/golang-jwt/jwt/v5`](https://github.com/golang-jwt/jwt).
- **Supported Algorithm**: HMAC-SHA256 (`HS256`).
- **Signature & Security Guarantees**:
  - The algorithm is hardcoded and strictly verified using `jwt.WithValidMethods([]string{"HS256"})`.
  - The `"none"` algorithm and unsigned tokens are unconditionally rejected.
  - Fails closed: if `JWT_SECRET` is missing, empty, or less than 16 bytes, the server refuses to start.
- **Claims Verified**:
  - `sub`: User ID (required).
  - `iss`: Issuer (verified against configured `JWT_ISSUER`, e.g. `arda-auth`).
  - `aud`: Audience (verified against configured `JWT_AUDIENCE`, e.g. `arda-api`).
  - `exp`: Expiry timestamp (required; expired tokens rejected with 401).
  - `nbf` / `iat`: Not before / issued at timestamps verified when present.
  - `tenant_id`: Tenant UUID string (required; parsed to `uuid.UUID`).
  - `role`: Role string (required; validated against allowed ARDA roles).
- **Anti-Spoofing Guarantee**:
  - Client headers like `X-Tenant-ID` or JSON body fields like `"tenant_id"` are never trusted.
  - Incoming `X-Tenant-ID` headers are stripped at the gateway boundary.
  - Downstream services receive the proven `tenant_id` exclusively through typed request context (`context.Context`) and internal gRPC metadata headers (`x-tenant-id`, `x-user-id`, `x-role`).

---

## 3. Authorization Matrix

ARDA enforces role-based access control (RBAC) via a centralized policy layer ([`services/gateway/auth/policy.go`](file:///home/aswin/second-project/arda/services/gateway/auth/policy.go)):

| Action | Route | `super_admin` | `company_admin` | `agent_developer` | `reviewer` |
|---|---|:---:|:---:|:---:|:---:|
| `agent:create` | `POST /api/v1/agents` | ✅ | ✅ | ✅ | ❌ |
| `agent:read` | `GET /api/v1/agents` | ✅ | ✅ | ✅ | ✅ |
| `agent:read` | `GET /api/v1/agents/{id}` | ✅ | ✅ | ✅ | ✅ |
| `agent:read` | `GET /api/v1/agents/{id}/versions/latest` *(canonical)* | ✅ | ✅ | ✅ | ✅ |
| `agent:read` | `GET /api/v1/agents/{id}/versions/published` *(alias)* | ✅ | ✅ | ✅ | ✅ |
| `agent:read` | `GET /api/v1/agents/{id}/versions/published/latest` *(alias)* | ✅ | ✅ | ✅ | ✅ |
| `agent:read` | `GET /api/v1/agents/{id}/versions/{versionId}` | ✅ | ✅ | ✅ | ✅ |
| `version:create` | `POST /api/v1/agents/{id}/versions` | ✅ | ✅ | ✅ | ❌ |
| `version:update` | `PUT /api/v1/agents/{id}/versions/{versionId}` | ✅ | ✅ | ✅ | ❌ |
| `version:publish` | `POST /api/v1/agents/{id}/versions/{versionId}/publish` | ✅ | ✅ | ❌ | ✅ |
| `version:delete` | `DELETE /api/v1/agents/{id}/versions/{versionId}` | ✅ | ✅ | ❌ | ❌ |
| `agent:execute` | `POST /api/v1/agents/{id}/execute` | ✅ | ✅ | ✅ | ❌ |
| `reliability:gate`| `POST /api/v1/reliability/gate` | ✅ | ✅ | ✅ | ✅ |
| `credential:manage`| `POST /api/v1/credentials` | ✅ | ✅ | ✅ | ❌ |
| `credential:manage`| `DELETE /api/v1/credentials/{id}` | ✅ | ✅ | ✅ | ❌ |
| `credential:manage`| `POST /api/v1/credentials/{id}/verify` | ✅ | ✅ | ✅ | ❌ |
| `credential:read` | `GET /api/v1/credentials` | ✅ | ✅ | ✅ | ❌ |
| `credential:read` | `GET /api/v1/credentials/{id}` | ✅ | ✅ | ✅ | ❌ |

### Governance Rules
1. **Separation of Duties on Publication:** `agent_developer` can create, configure, test, and update draft versions, but cannot publish versions. Only `reviewer`, `company_admin`, or `super_admin` can publish versions.
2. **Reviewer Read-Only Boundary:** `reviewer` evaluates and publishes draft versions and gates executions, but cannot create agents, edit drafts, delete drafts, trigger executions, or manage credentials.
3. **Published Version Immutability:** Once published, versions are immutable. Neither developers nor admins can edit or delete a published version (returns `409 Conflict`).

---

## 4. Version Resolution Route Conventions

To resolve the latest published version of an agent, the API Gateway provides:

1. **Canonical Route:**
   - `GET /api/v1/agents/:id/versions/latest`
2. **Supported Direct Aliases:**
   - `GET /api/v1/agents/:id/versions/published`
   - `GET /api/v1/agents/:id/versions/published/latest`
3. **Specific Version Route:**
   - `GET /api/v1/agents/:id/versions/:versionId`
   - Requires a valid UUID for `:versionId`. If a non-UUID parameter (e.g. invalid string) is provided, it is rejected with HTTP `400 VALIDATION_FAILED` rather than being misinterpreted as a version.

---

## 5. Gateway-to-Registry gRPC Boundary

The API Gateway is the public point of contact for external clients over HTTP/JSON. The Agent Registry runs behind the Gateway as a gRPC service implementing `ardav1.AgentRegistryService`:

- **Identity Propagation:** The Gateway propagates authenticated caller context to the Registry using gRPC outgoing metadata:
  - `x-tenant-id`: Authenticated tenant UUID string.
  - `x-user-id`: Caller user ID string.
  - `x-role`: Caller role string.
- **Tenant Validation:** The Agent Registry's `InternalAuthInterceptor` validates that `x-tenant-id` is present and matches any `tenant_id` supplied in the protobuf request body. If there is a mismatch or missing metadata, the call fails immediately with `codes.PermissionDenied`.
- **Status Code Mapping:**
  - `codes.InvalidArgument` → HTTP `400 VALIDATION_FAILED`
  - `codes.NotFound` → HTTP `404 NOT_FOUND`
  - `codes.AlreadyExists` → HTTP `409 VERSION_CONFLICT`
  - `codes.FailedPrecondition` → HTTP `409 PUBLISHED_IMMUTABLE`
  - `codes.Unauthenticated` → HTTP `401 UNAUTHENTICATED`
  - `codes.PermissionDenied` → HTTP `403 PERMISSION_DENIED`
  - `codes.Unavailable` → HTTP `503 UPSTREAM_UNAVAILABLE`

---

## 6. Vault Credential Management

The Gateway provides endpoints for managing mock LLM provider and MCP server credentials using HashiCorp Vault KV v2:

- `POST /api/v1/credentials`: Saves a credential.
  - Body: `{"category": "llm|mcp", "provider": "string", "secret_value": "string"}`
  - Secret value is written directly to Vault under `secret/data/tenants/{tenant_id}/credentials/{category}/{provider}`.
  - Metadata is stored in PostgreSQL table `credentials` with safe logical reference `secret:credentials/{category}/{provider}#{field}`.
  - Response returns `201 Created` with metadata and reference. **Raw secret values are NEVER returned in API responses, persisted in PostgreSQL, or logged.**
- `GET /api/v1/credentials`: Lists non-sensitive credential metadata for the authenticated tenant.
- `GET /api/v1/credentials/:id`: Retrieves credential metadata and secret reference for the tenant.
- `POST /api/v1/credentials/:id/verify`: Safely verifies that the stored secret can be resolved through Vault without disclosing the secret value.
- `DELETE /api/v1/credentials/:id`: Removes the secret from Vault and deletes the metadata from PostgreSQL.

### Scope Distinction: Storage vs Runtime Resolution
- **Implemented Scope:** Credential storage, retrieval of metadata/references, administrative verification, and deletion.
- **Future Scope:** Runtime secret resolution inside Temporal execution activities when real LLM providers and MCP servers are integrated.

---

## 7. Standardized Error Format & Observability

All error responses strictly adhere to the ARDA error format:

```json
{
  "error": {
    "code": "UNAUTHENTICATED",
    "message": "invalid or expired bearer token",
    "details": {}
  }
}
```

### Common Error Codes
- `UNAUTHENTICATED` (401): Missing, expired, malformed, or invalid JWT signature.
- `PERMISSION_DENIED` (403): Role lacks permission for the requested action.
- `NOT_FOUND` (404): Resource not found or belongs to another tenant.
- `VERSION_CONFLICT` / `PUBLISHED_IMMUTABLE` (409): Attempting to update or delete a published version.
- `RATE_LIMIT_EXCEEDED` (429): Request quota exceeded.
- `VALIDATION_FAILED` (400): Malformed JSON, invalid UUID, or out-of-range parameters.
- `UPSTREAM_UNAVAILABLE` (503): Downstream gRPC service address not reachable.
- `UPSTREAM_ERROR` (502): Downstream service communication failure.

---

## 8. Running Locally & Testing

```bash
# Start required local infrastructure
docker compose -f deployments/local/docker-compose.yml up -d

# Run migrations
./scripts/migrate.sh up

# Run Gateway
export JWT_SECRET="super-secure-local-dev-jwt-secret-change-in-production"
export REGISTRY_GRPC_ADDR="127.0.0.1:50051"
export VAULT_ADDR="http://127.0.0.1:8200"
export VAULT_TOKEN="root"
export TEMPORAL_ADDRESS="127.0.0.1:7233"

go run cmd/gateway/main.go
```
