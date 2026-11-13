ALTER TABLE audit_events DROP COLUMN IF EXISTS mode;
ALTER TABLE audit_events DROP COLUMN IF EXISTS mode_type;
ALTER TABLE audit_events DROP COLUMN IF EXISTS execution_mode;

ALTER TABLE tool_invocations DROP COLUMN IF EXISTS mode;
ALTER TABLE tool_invocations DROP COLUMN IF EXISTS mode_type;

