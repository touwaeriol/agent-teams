-- +goose Up
-- Session management per role (shared-members.md §5.1). Percentages are of context_limit, the
-- role's own context limit in tokens (NULL = the model's window), not of the model's window:
--   context_limit         tokens the role's sessions may use (e.g. 600000)
--   compact_between_pct   claiming the next task with context ≥ limit × pct compacts first (NULL = never)
--   compact_in_task_pct   the runtime compacts by itself at limit × pct (NULL = the runtime's default)
--   clear_after_tasks     a session that did this many tasks in a row is cleared (0 = never, NULL = n/a)
--   compact_focus         what a compaction must keep, added to its instructions
-- Existing roles get the defaults of their kind: writing members 60/80/8 with a focus, review and
-- decision gates —/80/1, lead and coordinator —/70/—. Employees carry their role in spec (a
-- snapshot taken when they are staffed), so the snapshots get the same values.
ALTER TABLE at_roles
    ADD COLUMN context_limit INT,
    ADD COLUMN compact_between_pct INT,
    ADD COLUMN compact_in_task_pct INT,
    ADD COLUMN clear_after_tasks INT,
    ADD COLUMN compact_focus TEXT NOT NULL DEFAULT '';
UPDATE at_roles SET compact_between_pct = 60, compact_in_task_pct = 80, clear_after_tasks = 8,
    compact_focus = '保留当前任务 id、分支、改过的文件、做过的决定、还没做完的事和已知的坑'
    WHERE tier = 'member' AND gate NOT IN ('review', 'decision');
UPDATE at_roles SET compact_in_task_pct = 80, clear_after_tasks = 1 WHERE tier = 'member' AND gate IN ('review', 'decision');
UPDATE at_roles SET compact_in_task_pct = 70 WHERE tier IN ('lead', 'coordinator');
UPDATE at_employees e SET spec = e.spec || jsonb_build_object('context_limit', r.context_limit,
    'compact_between_pct', r.compact_between_pct, 'compact_in_task_pct', r.compact_in_task_pct,
    'clear_after_tasks', r.clear_after_tasks, 'compact_focus', r.compact_focus)
    FROM at_roles r WHERE r.id = e.role_id AND jsonb_typeof(e.spec) = 'object';

-- +goose Down
UPDATE at_employees SET spec = spec - 'context_limit' - 'compact_between_pct' - 'compact_in_task_pct'
    - 'clear_after_tasks' - 'compact_focus' WHERE jsonb_typeof(spec) = 'object';
ALTER TABLE at_roles DROP COLUMN context_limit, DROP COLUMN compact_between_pct, DROP COLUMN compact_in_task_pct,
    DROP COLUMN clear_after_tasks, DROP COLUMN compact_focus;
