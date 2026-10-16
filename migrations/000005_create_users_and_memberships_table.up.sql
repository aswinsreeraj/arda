-- Migration: create_users_and_memberships_table (UP)
-- Description: Create persistent app_users and tenant_memberships tables with constraints and indexes

CREATE TABLE app_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    is_super_admin BOOLEAN NOT NULL DEFAULT false,
    email_verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_app_users_email_not_empty CHECK (char_length(trim(email)) > 0),
    CONSTRAINT chk_app_users_status_valid CHECK (status IN ('active', 'disabled'))
);

CREATE UNIQUE INDEX idx_app_users_email_lower ON app_users (LOWER(TRIM(email)));
CREATE INDEX idx_app_users_status ON app_users (status);

CREATE TRIGGER trg_app_users_updated_at
BEFORE UPDATE ON app_users
FOR EACH ROW
EXECUTE FUNCTION set_updated_at_timestamp();

CREATE TABLE tenant_memberships (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_tenant_memberships_user_tenant UNIQUE (user_id, tenant_id),
    CONSTRAINT chk_tenant_memberships_role_valid CHECK (role IN ('company_admin', 'agent_developer', 'reviewer')),
    CONSTRAINT chk_tenant_memberships_status_valid CHECK (status IN ('active', 'disabled'))
);

CREATE INDEX idx_tenant_memberships_user_id ON tenant_memberships(user_id);
CREATE INDEX idx_tenant_memberships_tenant_id ON tenant_memberships(tenant_id);
CREATE INDEX idx_tenant_memberships_tenant_role ON tenant_memberships(tenant_id, role);

CREATE TRIGGER trg_tenant_memberships_updated_at
BEFORE UPDATE ON tenant_memberships
FOR EACH ROW
EXECUTE FUNCTION set_updated_at_timestamp();
