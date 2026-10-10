-- Migration: create_agent_versions_table (UP)
-- Description: Create agent_versions table with JSONB configuration, version uniqueness,
-- and database-level immutability enforcement for published versions.

CREATE TABLE agent_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    version_number INTEGER NOT NULL,
    configuration JSONB NOT NULL,
    published BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMPTZ,
    CONSTRAINT chk_agent_versions_version_positive CHECK (version_number > 0),
    CONSTRAINT chk_agent_versions_configuration_object CHECK (jsonb_typeof(configuration) = 'object'),
    CONSTRAINT chk_agent_versions_publication_timestamp CHECK (
        (published = false AND published_at IS NULL) OR
        (published = true AND published_at IS NOT NULL)
    ),
    CONSTRAINT uq_agent_versions_agent_id_version_number UNIQUE (agent_id, version_number)
);

-- Indexes for agent-version lookups
CREATE INDEX idx_agent_versions_agent_id ON agent_versions(agent_id);
CREATE INDEX idx_agent_versions_agent_id_published ON agent_versions(agent_id, published);
CREATE INDEX idx_agent_versions_agent_id_version_desc ON agent_versions(agent_id, version_number DESC);

-- Trigger function enforcing database-level immutability for published versions
CREATE OR REPLACE FUNCTION enforce_agent_version_immutability()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        -- Published versions cannot be modified or unpublished
        IF OLD.published THEN
            RAISE EXCEPTION 'Published agent version % is immutable and cannot be modified.', OLD.id;
        END IF;

        -- Draft transitioning to published: automatically set published_at if omitted
        IF NEW.published AND NEW.published_at IS NULL THEN
            NEW.published_at := CURRENT_TIMESTAMP;
        END IF;

        NEW.updated_at := CURRENT_TIMESTAMP;
        RETURN NEW;
    ELSIF TG_OP = 'DELETE' THEN
        -- Published versions cannot be deleted
        IF OLD.published THEN
            RAISE EXCEPTION 'Published agent version % is immutable and cannot be deleted.', OLD.id;
        END IF;

        RETURN OLD;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_agent_versions_immutability
BEFORE UPDATE OR DELETE ON agent_versions
FOR EACH ROW
EXECUTE FUNCTION enforce_agent_version_immutability();
