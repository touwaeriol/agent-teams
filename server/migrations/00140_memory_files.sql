-- +goose Up
-- Auto memory as files (architecture.md §6.7): each project keeps MEMORY.md (the index every
-- session reads at start) and topic files (name, one-line description, content) that employees
-- read on demand with memory_read. The old one-line notes of at_project_memory move over:
-- coordinator / user notes become bullets of MEMORY.md (pinned first), lead finish summaries
-- (source '#<no>' or 'thread:<no>') become sections of thread-summaries.md, linked from MEMORY.md.
CREATE TABLE IF NOT EXISTS at_memory_files (
    project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL DEFAULT '',
    updated_by  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, name)
);

INSERT INTO at_memory_files (project_id, name, description, content, updated_by, created_at, updated_at)
SELECT m.project_id, 'thread-summaries.md', '已结束讨论串的总结（最近 30 个）',
       string_agg('## #' || substring(m.source from '[0-9]+') || COALESCE(' ' || t.title, '') || E'\n' || trim(m.text),
                  E'\n\n' ORDER BY m.created_at),
       'platform', min(m.created_at), max(m.created_at)
FROM at_project_memory m
LEFT JOIN at_threads t ON t.project_id = m.project_id AND t.no::text = substring(m.source from '[0-9]+')
WHERE m.source ~ '^(#|thread:)[0-9]+$'
GROUP BY m.project_id;

WITH notes AS (
    SELECT project_id,
           string_agg('- ' || regexp_replace(trim(text), '\s*\n\s*', ' ', 'g'), E'\n' ORDER BY pinned DESC, created_at) AS lines,
           min(created_at) AS first_at, max(created_at) AS last_at
    FROM at_project_memory
    WHERE source !~ '^(#|thread:)[0-9]+$'
    GROUP BY project_id
), summaries AS (
    SELECT project_id, created_at, updated_at FROM at_memory_files WHERE name = 'thread-summaries.md'
), ids AS (
    SELECT project_id FROM notes UNION SELECT project_id FROM summaries
)
INSERT INTO at_memory_files (project_id, name, description, content, updated_by, created_at, updated_at)
SELECT ids.project_id, 'MEMORY.md', '',
       concat_ws(E'\n', '# 项目记忆', '', n.lines,
                 CASE WHEN s.project_id IS NOT NULL THEN '- [thread-summaries.md](thread-summaries.md) — 已结束讨论串的总结（最近 30 个）' END),
       'platform', LEAST(n.first_at, s.created_at), GREATEST(n.last_at, s.updated_at)
FROM ids LEFT JOIN notes n USING (project_id) LEFT JOIN summaries s USING (project_id);

DROP TABLE IF EXISTS at_project_memory;

-- +goose Down
CREATE TABLE IF NOT EXISTS at_project_memory (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES at_projects(id) ON DELETE CASCADE,
    text        TEXT NOT NULL,
    source      TEXT NOT NULL,
    pinned      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_project_memory_project ON at_project_memory (project_id, created_at DESC);
-- Each file becomes one note (MEMORY.md pinned) so nothing written is lost.
INSERT INTO at_project_memory (id, project_id, text, source, pinned, created_at)
SELECT 'me_' || md5(project_id || '/' || name), project_id,
       CASE WHEN name = 'MEMORY.md' THEN content ELSE name || E'\n' || content END,
       'user', name = 'MEMORY.md', updated_at
FROM at_memory_files WHERE trim(content) <> '';
DROP TABLE IF EXISTS at_memory_files;
