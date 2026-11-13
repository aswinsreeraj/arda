CREATE TABLE IF NOT EXISTS review_cases (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    execution_id VARCHAR(128) NOT NULL,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    agent_name VARCHAR(255) NOT NULL,
    agent_version_id UUID NOT NULL,
    agent_version_number INT NOT NULL DEFAULT 1,
    execution_mode VARCHAR(64) NOT NULL DEFAULT 'realtime',
    status VARCHAR(32) NOT NULL DEFAULT 'OPEN',
    proposed_decision TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    reliability_threshold DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    escalation_reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
    frozen_evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    
    decision VARCHAR(32),
    final_decision VARCHAR(64),
    resolution_reason TEXT,
    reviewer_id VARCHAR(128),
    audit_record_id VARCHAR(64),
    resolved_at TIMESTAMPTZ,
    
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_review_cases_tenant_status ON review_cases(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_review_cases_tenant_agent ON review_cases(tenant_id, agent_id);
CREATE INDEX IF NOT EXISTS idx_review_cases_execution ON review_cases(execution_id);
CREATE INDEX IF NOT EXISTS idx_review_cases_created_at ON review_cases(created_at DESC);

CREATE TABLE IF NOT EXISTS outbox_events (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id UUID NOT NULL,
    event_type VARCHAR(128) NOT NULL,
    topic VARCHAR(128) NOT NULL,
    payload JSONB NOT NULL,
    headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
    retry_count INT NOT NULL DEFAULT 0,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_outbox_events_status ON outbox_events(status, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_outbox_events_tenant ON outbox_events(tenant_id);
