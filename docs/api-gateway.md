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

## 3. Authorization Matrix & Platform Roles

ARDA enforces role-based access control (RBAC) via a centralized policy layer ([`services/gateway/auth/policy.go`](file:///home/aswin/second-project/arda/services/gateway/auth/policy.go)).

### Endpoint Group Permissions Matrix

| Endpoint Group | `super_admin` | `company_admin` | `agent_developer` | `reviewer` |
|---|:---:|:---:|:---:|:---:|
| **Tenants (`/tenants`)** | full | - *(registers via `/tenants/register`)* | - | - |
| **Users (`/users`)** | - | full *(own tenant)* | self only | self only |
| **Agents (`/agents`)** | - | full | full | read-only |
| **MCP Servers / LLM Keys** | - | full | read-only | - |
| **Executions (`/executions`)** | - | read | read + create *(via API key)* | read |
| **Reviews (`/reviews`)** | - | read + resolve | read | read + resolve |
| **Audit (`/audit`)** | - | read *(own tenant)* | read *(own tenant)* | read *(own tenant)* |

### Route-Level Mapping

| Action | Route | `super_admin` | `company_admin` | `agent_developer` | `reviewer` |
|---|---|:---:|:---:|:---:|:---:|
| **Public Registration** | `POST /api/v1/tenants/register` | ✅ (Public) | ✅ (Public) | ✅ (Public) | ✅ (Public) |
| **Public Login** | `POST /api/v1/auth/login` | ✅ (Public) | ✅ (Public) | ✅ (Public) | ✅ (Public) |
| `tenant:create` | `POST /api/v1/tenants` | ✅ | ❌ | ❌ | ❌ |
| `users:read` | `GET /api/v1/users` | ❌ | ✅ *(own tenant)* | ✅ *(self only)* | ✅ *(self only)* |
| `users:read` | `GET /api/v1/users/{userId}` | ❌ | ✅ *(own tenant)* | ✅ *(self only)* | ✅ *(self only)* |
| `users:read` | `GET /api/v1/auth/me` | ✅ *(self)* | ✅ *(self)* | ✅ *(self)* | ✅ *(self)* |
| `tenant:member:manage` | `POST /api/v1/users` | ❌ | ✅ *(own tenant)* | ❌ | ❌ |
| `tenant:member:manage` | `PATCH /api/v1/users/{userId}` | ❌ | ✅ *(own tenant)* | ❌ | ❌ |
| `tenant:member:manage` | `DELETE /api/v1/users/{userId}` | ❌ | ✅ *(own tenant)* | ❌ | ❌ |
| `tenant:member:manage` | `POST /api/v1/tenants/{tenantId}/members` | ❌ | ✅ *(own tenant)* | ❌ | ❌ |
| `tenant:member:manage` | `PATCH /api/v1/tenants/{tenantId}/members/{userId}` | ❌ | ✅ *(own tenant)* | ❌ | ❌ |
| `tenant:member:manage` | `DELETE /api/v1/tenants/{tenantId}/members/{userId}` | ❌ | ✅ *(own tenant)* | ❌ | ❌ |
| `tenant:member:read` | `GET /api/v1/tenants/{tenantId}/members` | ❌ | ✅ *(own tenant)* | ❌ | ❌ |
| `agent:create` | `POST /api/v1/agents` | ❌ | ✅ | ✅ | ❌ |
| `agent:read` | `GET /api/v1/agents` | ❌ | ✅ | ✅ | ✅ |
| `agent:read` | `GET /api/v1/agents/{id}` | ❌ | ✅ | ✅ | ✅ |
| `agent:read` | `GET /api/v1/agents/{id}/versions/latest` *(canonical)* | ❌ | ✅ | ✅ | ✅ |
| `agent:read` | `GET /api/v1/agents/{id}/versions/published` *(alias)* | ❌ | ✅ | ✅ | ✅ |
| `agent:read` | `GET /api/v1/agents/{id}/versions/published/latest` *(alias)* | ❌ | ✅ | ✅ | ✅ |
| `agent:read` | `GET /api/v1/agents/{id}/versions/{versionId}` | ❌ | ✅ | ✅ | ✅ |
| `version:create` | `POST /api/v1/agents/{id}/versions` | ❌ | ✅ | ✅ | ❌ |
| `version:update` | `PUT /api/v1/agents/{id}/versions/{versionId}` | ❌ | ✅ | ✅ | ❌ |
| `version:publish` | `POST /api/v1/agents/{id}/versions/{versionId}/publish` | ❌ | ✅ | ❌ | ✅ |
| `version:delete` | `DELETE /api/v1/agents/{id}/versions/{versionId}` | ❌ | ✅ | ❌ | ❌ |
| `agent:execute` | `POST /api/v1/agents/{id}/execute` | ❌ | ✅ | ✅ | ❌ |
| `credential:manage`| `POST /api/v1/credentials` | ❌ | ✅ | ❌ | ❌ |
| `credential:manage`| `DELETE /api/v1/credentials/{id}` | ❌ | ✅ | ❌ | ❌ |
| `credential:manage`| `POST /api/v1/credentials/{id}/verify` | ❌ | ✅ | ❌ | ❌ |
| `credential:read` | `GET /api/v1/credentials` | ❌ | ✅ | ✅ | ❌ |
| `credential:read` | `GET /api/v1/credentials/{id}` | ❌ | ✅ | ✅ | ❌ |
| `reliability:gate`| `POST /api/v1/reliability/gate` | ❌ | ✅ | ✅ | ✅ |

### Governance Rules
1. **One-Step Company Tenant Registration:** Company admins self-register their company tenant via public `POST /api/v1/tenants/register`. This provision the tenant, human user account, and `company_admin` membership atomically in one transaction, immediately returning access tokens without requiring pre-existing accounts.
2. **Platform vs. Tenant Scope Separation:** `super_admin` operates exclusively at the platform infrastructure level managing tenants (`/tenants`), but cannot access or tamper with tenant-scoped users, agents, credentials, executions, or audit trails.
3. **Separation of Duties on Publication:** `agent_developer` can create, configure, test, and update draft versions, but cannot publish versions. Only `reviewer` or `company_admin` can publish versions.
4. **Credential Isolation:** Only `company_admin` can create, modify, verify, or delete Vault credentials (MCP keys / LLM tokens). `agent_developer` has read-only access to inspect configured tools, while `reviewer` and `super_admin` have zero credential access.
5. **Published Version Immutability:** Once published, versions are immutable. Neither developers nor admins can edit or delete a published version (returns `409 Conflict`).

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

## 7. User Accounts, Authentication & Tenant Memberships

ARDA provides database-backed user identity, bcrypt password hashing, and tenant-scoped memberships.

### Endpoints

- `POST /api/v1/auth/register` (Public, rate-limited):
  - Request: `{"email": "string", "password": "string (min 8 chars)", "display_name": "string"}`
  - Email is strictly normalized (lowercase, trimmed) and validated. Uniqueness is enforced at database level (`idx_app_users_email_lower`).
  - Passwords are encrypted using memory-hard `bcrypt` hashing before storage.
  - Returns `201 Created` with safe user information (never returns password hashes).
- `POST /api/v1/auth/login` (Public, rate-limited):
  - Request: `{"email": "string", "password": "string", "tenant_id": "optional UUID"}`
  - Normalizes email, compares bcrypt hash, and verifies user account is active (`status == 'active'`).
  - Resolves active tenant membership. If `tenant_id` is specified, verifies that the user is an active member of that tenant; otherwise defaults to their first active membership.
  - Derives role and tenant context strictly from trusted database records (never client-supplied values).
  - Issues a signed access JWT (`HS256`, 1-hour expiration) with claims: `sub`, `tenant_id`, `role`, `iss`, `aud`, `exp`.
- `GET /api/v1/auth/me` (Protected):
  - Returns the authenticated user's profile, active tenant details, active role, and list of all memberships.
- `POST /api/v1/tenants` (Protected, `tenant:create`):
  - Request: `{"name": "string"}`
  - Provisions a new tenant and sets the caller as the initial `company_admin` within a single PostgreSQL transaction.
- `POST /api/v1/tenants/:tenantId/members` (Protected, `tenant:member:manage`):
  - Request: `{"user_id": "UUID", "email": "string", "role": "company_admin|agent_developer|reviewer"}`
  - Adds an existing user account to the tenant with the designated role.
  - Role cannot be `super_admin`.
- `GET /api/v1/tenants/:tenantId/members` (Protected, `tenant:member:read`):
  - Lists all members, their display names, emails, roles, and statuses for the tenant.
- `PATCH /api/v1/tenants/:tenantId/members/:userId` (Protected, `tenant:member:manage`):
  - Request: `{"role": "string", "status": "active|disabled"}`
  - Updates member role or active status.
  - Protected: Rejects any attempt to demote or deactivate the last active `company_admin` with `409 Conflict`.
- `DELETE /api/v1/tenants/:tenantId/members/:userId` (Protected, `tenant:member:manage`):
  - Removes a member from the tenant.
  - Protected: Rejects any attempt to delete the last active `company_admin` with `409 Conflict`.

---

## 8. Standardized Error Format & Observability

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
