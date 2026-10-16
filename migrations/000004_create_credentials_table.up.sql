-- Migration: create_credentials_table (UP)
-- Description: Create the credentials metadata table for tenant-scoped LLM and MCP credentials

CREATE TABLE IF NOT EXISTS credentials (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    category VARCHAR(50) NOT NULL,
    provider VARCHAR(100) NOT NULL,
    reference TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_tenant_category_provider UNIQUE (tenant_id, category, provider),
    CONSTRAINT chk_category_valid CHECK (category IN ('llm', 'mcp'))
);

CREATE INDEX IF NOT EXISTS idx_credentials_tenant_id ON credentials(tenant_id);
