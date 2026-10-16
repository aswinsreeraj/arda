-- Migration: create_users_and_memberships_table (DOWN)
-- Description: Drop tenant_memberships and app_users tables

DROP TABLE IF EXISTS tenant_memberships CASCADE;
DROP TABLE IF EXISTS app_users CASCADE;
