-- +goose Up
-- The account's interface language (web i18n): '' = follow the browser, otherwise one of the
-- languages the web client ships. The server uses it for text it writes for that user.
ALTER TABLE at_users
    ADD COLUMN IF NOT EXISTS locale TEXT NOT NULL DEFAULT ''
        CHECK (locale IN ('', 'zh-CN', 'en', 'ko', 'ja'));

-- +goose Down
ALTER TABLE at_users DROP COLUMN IF EXISTS locale;
