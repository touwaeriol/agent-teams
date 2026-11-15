-- +goose Up
-- git.result, cred.request and job ownership checks look commands up by job id;
-- without this index each lookup scans the node's whole command history.
CREATE INDEX IF NOT EXISTS at_node_commands_job
    ON at_node_commands (node_id, (body->>'job_id')) WHERE type LIKE 'git.%';

-- +goose Down
DROP INDEX IF EXISTS at_node_commands_job;
