# ADR 0001: HTTP API Gateway to gRPC Agent Registry Architecture

## Status

Accepted

## Context

ARDA is a multi-tenant AI agent governance and execution platform. In the initial platform foundation phase:
- The API Gateway and Agent Registry both ran as in-process Gin HTTP servers where the Gateway forwarded HTTP requests in-memory to the Registry handler.
- Protobuf contracts (`proto/arda/v1/agent.proto`) had been defined and Go stubs generated, but no operational gRPC server or client implementation was wired between the Gateway and Registry.
- Multi-tenancy, RBAC, published-version immutability, and Vault credential referencing were enforced, but internal service boundaries needed a clear architectural contract.

As the platform evolves toward independent deployability, separate microservice containerization, and strict internal service boundaries, the architecture required:
1. Retaining external client simplicity using standard RESTful HTTP/JSON.
2. Establishing a typed, high-performance internal RPC boundary for internal service communication.
3. Propagating trusted caller identity and tenant isolation across the internal transport boundary without exposing internal endpoints publicly.
4. Managing mock LLM and MCP credentials in HashiCorp Vault with non-sensitive metadata stored in PostgreSQL.

## Decision

1. **External HTTP/JSON Entry Point**:
   - The API Gateway remains the sole public entry point for external clients over HTTP/JSON.
   - Public clients continue interacting with `/api/v1/agents`, `/api/v1/agents/:id/versions/*`, `/api/v1/credentials`, and `/api/v1/agents/:id/execute`.
   - The Gateway is responsible for terminating TLS, enforcing process-local rate limiting, validating JWT signatures/claims, and enforcing centralized RBAC.

2. **Internal gRPC Boundary for Agent Registry**:
   - The Agent Registry implements `ardav1.AgentRegistryServiceServer` and registers it with a standard Go `grpc.Server`.
   - All Registry business logic in `services/registry/service` and GORM persistence in `services/registry/store` are preserved and invoked by the gRPC server handlers.
   - The API Gateway uses the generated `ardav1.AgentRegistryServiceClient` to communicate with the Agent Registry.
   - In distributed deployments, the Gateway connects over TCP to `REGISTRY_GRPC_ADDR` (e.g. `127.0.0.1:50051`). In single-binary local development, it can connect via an in-memory `bufconn` listener, ensuring the exact same gRPC serialization, interceptor pipeline, and error mapping are exercised in all environments.

3. **Trusted Internal Identity Propagation**:
   - The Gateway derives caller identity (`tenant_id`, `user_id`, `role`) strictly from the cryptographically validated JWT.
   - The Gateway injects this trusted context into outgoing gRPC metadata headers: `x-tenant-id`, `x-user-id`, and `x-role`.
   - The Agent Registry's `InternalAuthInterceptor` extracts `x-tenant-id` from incoming metadata and validates it against any `tenant_id` provided in request messages. If missing or mismatched, requests are rejected with `codes.PermissionDenied`.
   - Normal protobuf request fields are never treated as proof of authentication.

4. **Vault Credential Management Flow**:
   - The Gateway provides `/api/v1/credentials` endpoints for storing, retrieving metadata for, verifying, and deleting mock LLM and MCP credentials.
   - Actual secret values are written directly to HashiCorp Vault KV v2 under `secret/data/tenants/{tenant_id}/credentials/{category}/{provider}`.
   - Only non-sensitive metadata and logical Vault references (e.g. `secret:credentials/llm/openai#api_key`) are persisted in PostgreSQL. Raw secret values are never stored in PostgreSQL, returned in API responses, or written to logs.
   - Credential management is explicitly separated from runtime secret resolution, which will occur inside Temporal workflow execution activities when real LLM/MCP providers are integrated.

5. **Canonical Version-Resolution Route**:
   - The canonical endpoint for resolving the latest published version is `GET /api/v1/agents/:id/versions/latest`.
   - Direct aliases `GET /api/v1/agents/:id/versions/published` and `GET /api/v1/agents/:id/versions/published/latest` are supported for backwards compatibility.
   - Specific version lookups use `GET /api/v1/agents/:id/versions/:versionId`, which validates that `:versionId` is a valid UUID, returning HTTP 400 if malformed.

## Consequences

### Positive
- **Clear Service Boundaries**: The Agent Registry is decoupled from HTTP transport concerns and can be deployed, scaled, or migrated independently.
- **Strict Protocol Contracts**: Protobuf and gRPC ensure strongly-typed request/response validation, backward compatibility rules, and code generation across languages.
- **Defense in Depth**: Authenticated tenant context is verified both at the external Gateway boundary and inside the Registry gRPC interceptor.
- **Zero Raw Secret Storage**: PostgreSQL contains only non-sensitive metadata and logical references.
- **Zero Client Breakage**: External HTTP paths, request payloads, and response envelopes remain identical.

### Negative
- **Additional Hop**: Communication across network boundaries adds serialization and transport overhead compared to direct in-memory Go function calls.
- **Operational Complexity**: Running separate gRPC listeners requires configuring internal networking, ports, and service discovery in containerized environments.

## Security Considerations

1. **Private Binding**: The Registry gRPC listener must bind only to loopback (`127.0.0.1`) in local deployments or a private VPC/container network in distributed deployments. It must never be exposed directly to the public internet.
2. **Context Integrity**: The Registry ignores external headers and trusts only metadata propagated by the Gateway.
3. **No Secret Disclosure**: Credential responses return metadata and references; raw secrets are inaccessible via the API.
4. **Immutability Enforcement**: Published agent versions cannot be modified or deleted at either the service layer or the database trigger layer.

## Rejected Alternatives

1. **Public gRPC API**:
   - *Rejected*: Forcing external frontend and API consumers to speak gRPC would break web client compatibility and standard REST HTTP ergonomics.
2. **Direct In-Process Gin-to-Gin Forwarding**:
   - *Rejected*: In-process HTTP routing between Gateway and Registry prevented true microservice separation and left the protobuf/gRPC contracts unused.
3. **Trusting Protobuf Request `tenant_id` Without Metadata Interceptor**:
   - *Rejected*: Allowing arbitrary callers to specify `tenant_id` in request payloads without interceptor validation would create severe tenant-spoofing vulnerabilities.
4. **Storing Raw Secrets in PostgreSQL**:
   - *Rejected*: Violates ARDA's security requirement that credentials must reside exclusively in HashiCorp Vault.
