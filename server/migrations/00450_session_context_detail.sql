-- +goose Up
-- The latest context breakdown a Claude session reported (session.context, protocol.md §4.10
-- 上下文细分): categories, MCP tools, memory files, skills (ContextDetail JSON; NULL = never
-- reported) and when it arrived.
ALTER TABLE at_agent_sessions
    ADD COLUMN context_detail JSONB,
    ADD COLUMN context_detail_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE at_agent_sessions DROP COLUMN context_detail, DROP COLUMN context_detail_at;
