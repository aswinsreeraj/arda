-- Migration: create_tenants_table (DOWN)
-- Description: Drop the tenants table and triggers

DROP TRIGGER IF EXISTS trg_tenants_updated_at ON tenants;
DROP TABLE IF EXISTS tenants;
DROP FUNCTION IF EXISTS set_updated_at_timestamp();
