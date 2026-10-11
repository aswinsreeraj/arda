# API Gateway v1 Documentation

API Gateway v1 acts as the governed single point of entry for the ARDA platform, enforcing JWT authentication, role-based authorization, tenant context isolation, process-local rate limiting, structured observability, and downstream routing.

---

## 1. Architecture

```
                                  +-----------------------------------------+
                                  |             Client Request              |
                                  +-----------------------------------------+
                                                       |
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
                         +-----------------------------+-----------------------------+
                         |                                                           |
                         v                                                           v
        +-----------------------------------+                       +----------------------------------+
        |        Agent Registry Route       |                       |     Reliability Service Route    |
        |   (Downstream Context-propagated  |                       |   (gRPC GateRequest / GateResponse   |
        |       tenant-scoped queries)      |                       |       with validated tenant_id)      |
        +-----------------------------------+                       +----------------------------------+
```

---

## 2. Authentication

- **Library**: Maintained standard library [`github.com/golang-jwt/jwt/v5`](https://github.com/golang-jwt/jwt).
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
  - Downstream services receive the proven `tenant_id` exclusively through typed request context (`context.Context`).

---

## 3. Authorization Matrix

ARDA enforces role-based access control (RBAC) via a centralized policy layer ([`services/gateway/auth/policy.go`](file:///home/aswin/second-project/arda/services/gateway/auth/policy.go)):

| Action | Route | `super_admin` | `company_admin` | `agent_developer` | `reviewer` |
|---|---|:---:|:---:|:---:|:---:|
| `agent:create` | `POST /api/v1/agents` | ✅ | ✅ | ✅ | ❌ |
| `agent:read` | `GET /api/v1/agents/{id}` | ✅ | ✅ | ✅ | ✅ |
| `version:create` | `POST /api/v1/agents/{id}/versions` | ✅ | ✅ | ✅ | ❌ |
| `version:update` | `PUT /api/v1/agents/{id}/versions/{versionId}` | ✅ | ✅ | ✅ | ❌ |
| `version:publish` | `POST /api/v1/agents/{id}/versions/{versionId}/publish` | ✅ | ✅ | ❌ | ✅ |
| `version:delete` | `DELETE /api/v1/agents/{id}/versions/{versionId}` | ✅ | ✅ | ❌ | ❌ |
| `reliability:gate` | `POST /api/v1/reliability/gate` | ✅ | ✅ | ✅ | ✅ |

*Note on Governance:* `agent_developer` can create and edit draft configurations, but cannot publish them without a `reviewer` or `company_admin` approval.

---

## 4. Rate Limiting

- **Implementation**: Process-local token-bucket limiter using `golang.org/x/time/rate`.
- **Scope**: Keyed by authenticated `tenant_id` (or client IP for unauthenticated routes).
- **Defaults**: Configurable via `RATE_LIMIT_RPS` (default: 20 req/s) and `RATE_LIMIT_BURST` (default: 40 tokens).
- **Behavior on Exceed**: Returns HTTP `429 Too Many Requests` with `Retry-After: 1` header.

> [!WARNING]
> **Process-Local Limitation**: The rate limiter is currently in-memory and local to a single process instance. It is not globally synchronized across multiple horizontal Gateway replicas. Distributed rate limiting (e.g. via Redis token buckets) is planned for a future milestone.

---

## 5. Downstream Routing

1. **Agent Registry**:
   - Routes: `/api/v1/agents` and `/api/v1/agents/{id}/versions/*`.
   - Propagates validated `tenant_id` via request context directly into the registry layer, preserving existing tenant-scoped queries.
2. **Reliability Service (gRPC Proxy)**:
   - Route: `POST /api/v1/reliability/gate`.
   - Reuses generated protobuf contracts ([`proto/arda/v1/reliability.proto`](file:///home/aswin/second-project/arda/proto/arda/v1/reliability.proto) & [`ardav1.ReliabilityServiceClient`](file:///home/aswin/second-project/arda/gen/go/arda/v1/reliability_grpc.pb.go#L36)).
   - Automatically injects `tenant_id` from verified JWT claims into the gRPC `GateRequest`.
   - Returns HTTP 503 `UPSTREAM_UNAVAILABLE` if upstream address is unconfigured.
   - Returns HTTP 502 `UPSTREAM_ERROR` if upstream gRPC call fails.

---

## 6. Standardized Error Format & Observability

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
- `FORBIDDEN` (403): Role lacks permission for the requested action.
- `RATE_LIMIT_EXCEEDED` (429): Request quota exceeded.
- `VALIDATION_FAILED` (400): Malformed JSON, invalid UUID, or out-of-range parameters.
- `UPSTREAM_UNAVAILABLE` (503): Downstream service address not configured.
- `UPSTREAM_ERROR` (502): Downstream service communication failure.

### Structured Logging
Every request produces structured log lines with request ID, latency, and status:
```text
[GATEWAY] req_id=362aa282-cbe3-421d-8e97-3fa0596050e2 method=POST path=/api/v1/agents status=201 duration_ms=4 tenant_id=780d2863-5edf-4dd4-bd22-5ecdfeeacec4 user_id=alice-developer role=company_admin
```
Bearer tokens and credentials are never written to logs.

---

## 7. Running Locally & Testing

### Running the Gateway

```bash
export JWT_SECRET="your-development-secret-key-32bytes-long"
export JWT_ISSUER="arda-auth"
export JWT_AUDIENCE="arda-api"
export PORT="8000"
export DATABASE_URL="postgres://arda_user:arda_password@127.0.0.1:5432/arda?sslmode=disable"
export RELIABILITY_SERVICE_ADDR="127.0.0.1:50051" # optional

go run cmd/gateway/main.go
```

### Running Test Suite

```bash
go test -count=1 ./services/gateway/...
go test -count=1 ./...
go vet ./...
```

---

## 8. Remaining Integration Gaps & Limitations

1. **IdP / Token Issuance Service**:
   - The Gateway validates JWTs signed by the trusted authority. An external Identity Provider (e.g., Auth0, Keycloak, or ARDA Auth service) is required to issue user credentials and login tokens.
2. **Distributed Rate Limiting**:
   - The current token-bucket limiter is process-local. Scaling gateway instances horizontally requires integrating an out-of-process store (e.g. Redis).
3. **Reliability Python Service Upstream**:
   - The Gateway includes the complete gRPC routing client for `arda.v1.ReliabilityService`. Connecting requires running the `arda-reliability` Python gRPC backend on the target network port.
