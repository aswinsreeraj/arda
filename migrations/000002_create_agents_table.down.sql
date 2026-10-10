-- Migration: create_agents_table (DOWN)
-- Description: Drop the agents table and triggers

DROP TRIGGER IF EXISTS trg_agents_updated_at ON agents;
DROP TABLE IF EXISTS agents;
