-- +goose Up
-- User → role assignments move into Casbin's own table (data-model.md §1.1, server/README.md
-- "Authorization"): one grouping row (ptype 'g', v0 = user id, v1 = admin | user) per user,
-- read and written only through the Casbin enforcer. at_users.role is dropped: there is no
-- second copy, no trigger and no sync. The route policy (p lines) stays in the embedded
-- authz/policy.csv. Layout and index are the standard casbin_rule of the Casbin adapters.
-- v1 (Python) is retired, so dropping its column is allowed (architecture.md §14, P6).

CREATE TABLE casbin_rule (
    id BIGSERIAL PRIMARY KEY,
    ptype VARCHAR(100) NOT NULL DEFAULT '',
    v0 VARCHAR(100) NOT NULL DEFAULT '',
    v1 VARCHAR(100) NOT NULL DEFAULT '',
    v2 VARCHAR(100) NOT NULL DEFAULT '',
    v3 VARCHAR(100) NOT NULL DEFAULT '',
    v4 VARCHAR(100) NOT NULL DEFAULT '',
    v5 VARCHAR(100) NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_casbin_rule ON casbin_rule (ptype, v0, v1, v2, v3, v4, v5);

-- Anything but 'admin' becomes 'user' (v1 only ever wrote these two).
INSERT INTO casbin_rule (ptype, v0, v1)
SELECT 'g', id, CASE WHEN role = 'admin' THEN 'admin' ELSE 'user' END FROM at_users;

ALTER TABLE at_users DROP COLUMN role;

-- +goose Down
ALTER TABLE at_users ADD COLUMN role TEXT NOT NULL DEFAULT 'user';
UPDATE at_users u SET role = 'admin'
 WHERE EXISTS (SELECT 1 FROM casbin_rule r WHERE r.ptype = 'g' AND r.v0 = u.id AND r.v1 = 'admin');
DROP TABLE casbin_rule;
