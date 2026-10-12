# Local Development Environment Guide

This guide describes how to configure, run, and manage the local PostgreSQL database infrastructure and schema migrations for ARDA.

---

## Prerequisites

- **Docker** and **Docker Compose** (Compose v2)
- *(Optional)* **golang-migrate** CLI (if running migrations natively without Docker)

---

## 1. Environment Setup

1. Copy the sample environment file to `.env`:
   ```bash
   cp .env.example .env
   ```
2. Verify the configuration values in `.env`. The default placeholders are configured for local development:
   - `POSTGRES_HOST=127.0.0.1`
   - `POSTGRES_PORT=5432`
   - `POSTGRES_DB=arda`
   - `POSTGRES_USER=arda_user`
   - `POSTGRES_PASSWORD=arda_password`
   - `DATABASE_URL=postgres://arda_user:arda_password@127.0.0.1:5432/arda?sslmode=disable`
   - `VAULT_ADDR=http://127.0.0.1:8200`
   - `VAULT_TOKEN=root`
   - `VAULT_MOUNT=secret`
   - `VAULT_TIMEOUT=5s`
   - `TEMPORAL_ADDRESS=127.0.0.1:7233`
   - `TEMPORAL_NAMESPACE=default`
   - `TEMPORAL_TASK_QUEUE=arda-v1-task-queue`
   - `TEMPORAL_UI_PORT=8233`

> [!NOTE]
> `.env` is explicitly ignored by Git (`.gitignore`). Never commit real credentials or production secrets to source control.

---

## 2. Starting Local Services (PostgreSQL, Vault & Temporal)

Start local PostgreSQL, Vault, and Temporal services in the background:

```bash
docker compose -f deployments/local/docker-compose.yml up -d
```

Check the health status of the containers:

```bash
docker compose -f deployments/local/docker-compose.yml ps
```

The services include automated health checks and local port bindings:
- **PostgreSQL**: `127.0.0.1:5432` (relational database for ARDA registry and application state).
- **Vault**: `127.0.0.1:8200` (KV v2 secrets engine mounted at `secret/`).
- **Temporal Server**: `127.0.0.1:7233` (gRPC frontend for workflow orchestrations). Automatically initializes isolated `temporal` and `temporal_visibility` databases in PostgreSQL.
- **Temporal Web UI**: `127.0.0.1:8233` (browser dashboard for inspecting workflows, histories, and task queues).

> [!WARNING]
> **Production Boundary**: Local container configurations (Vault `-dev` mode, Temporal `auto-setup`) are strictly for local testing and development. In production, run managed or clustered deployments with mutual TLS, external KMS unseal, and managed persistence.

To stop the services without losing data:

```bash

docker compose -f deployments/local/docker-compose.yml down
```


---

## 3. Applying Migrations

ARDA uses [golang-migrate](https://github.com/golang-migrate/migrate) to manage reproducible schema migrations.

### Option A: Using the helper script (Recommended)

The script automatically uses your host `migrate` binary if available, or falls back to the official `migrate/migrate` Docker image:

```bash
./scripts/migrate.sh up
```

### Option B: Using Docker Compose directly

```bash
docker compose -f deployments/local/docker-compose.yml --profile tools run --rm migrate up
```

---

## 4. Checking Migration Status

View the current applied migration version:

```bash
./scripts/migrate.sh status
```

Or query the migration tracking table directly:

```bash
docker compose -f deployments/local/docker-compose.yml exec postgres \
  psql -U arda_user -d arda -c "SELECT * FROM schema_migrations;"
```

---

## 5. Rolling Back Migrations

To roll back the most recent migration step:

```bash
./scripts/migrate.sh down 1
```

To roll back `N` steps, specify the count:

```bash
./scripts/migrate.sh down 2
```

---

## 6. Resetting the Local Database

> [!WARNING]
> **Data Loss Warning**: Resetting the database drops all schemas, tables, and data. Any locally created tenants, agents, and versions will be permanently removed.

### Schema Reset (re-runs migrations)

Drops all database objects and reapplies all migrations from scratch:

```bash
./scripts/migrate.sh reset
```

### Full Volume Reset (clean container state)

Stops the container and removes the persistent Docker named volume (`arda_postgres_data`):

```bash
docker compose -f deployments/local/docker-compose.yml down -v
docker compose -f deployments/local/docker-compose.yml up -d
./scripts/migrate.sh up
```

---

## 7. Schema Architecture & Immutability Rules

- **Tenants (`tenants`)**: Top-level tenant scope (`id` UUID, `name`, `status`, `created_at`, `updated_at`).
- **Agents (`agents`)**: Tenant-scoped agents (`id` UUID, `tenant_id` foreign key with `ON DELETE RESTRICT`, `name`, timestamps).
- **Agent Versions (`agent_versions`)**:
  - `(agent_id, version_number)` is strictly unique.
  - Configurations are stored as PostgreSQL-native `JSONB`.
  - **Immutability Enforcement**: A PostgreSQL row-level trigger (`trg_agent_versions_immutability`) enforces that once `published = true`, the record cannot be updated, unpublished, or deleted. Drafts (`published = false`) may transition to published.
  - Foreign key deletion is restricted (`ON DELETE RESTRICT`) to prevent cascade deletion of published version history.
- **Security Rule**: Never store plaintext LLM or MCP credentials in PostgreSQL configurations; store secret references or environment variable identifiers instead.
