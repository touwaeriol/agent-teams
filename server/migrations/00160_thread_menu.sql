-- +goose Up
-- The thread ⋯ menu: 置顶 (pinned_at: pinned threads sort first in their group, newest pin
-- first), 添加提醒 (remind_at: when the timers runner should bring the thread back; it moves
-- to reminded_at exactly once, which puts the thread under 等你处理 until the user opens it or
-- clears the reminder) and 发送反馈 (at_feedback: what users send to the administrators, kept
-- when the thread is deleted).
ALTER TABLE at_threads ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMPTZ;
ALTER TABLE at_threads ADD COLUMN IF NOT EXISTS remind_at TIMESTAMPTZ;
ALTER TABLE at_threads ADD COLUMN IF NOT EXISTS reminded_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS at_threads_remind_at ON at_threads (remind_at) WHERE remind_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS at_feedback (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    project_id      TEXT NOT NULL DEFAULT '',
    thread_id       TEXT NOT NULL DEFAULT '',
    thread_no       INT NOT NULL DEFAULT 0,
    thread_title    TEXT NOT NULL DEFAULT '',
    text            TEXT NOT NULL,
    content         TEXT NOT NULL DEFAULT '',
    handled_at      TIMESTAMPTZ,
    handled_by      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_feedback_created ON at_feedback (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS at_feedback;
DROP INDEX IF EXISTS at_threads_remind_at;
ALTER TABLE at_threads DROP COLUMN IF EXISTS reminded_at;
ALTER TABLE at_threads DROP COLUMN IF EXISTS remind_at;
ALTER TABLE at_threads DROP COLUMN IF EXISTS pinned_at;
