-- Migration: create_agent_versions_table (DOWN)
-- Description: Drop the agent_versions table, immutability trigger, and function

DROP TRIGGER IF EXISTS trg_agent_versions_immutability ON agent_versions;
DROP FUNCTION IF EXISTS enforce_agent_version_immutability();
DROP TABLE IF EXISTS agent_versions;
