-- +goose Up
-- Тег, который сайт выдаёт сам по своему правилу: его, как теги ролей и чемпионов, не выдать руками.
ALTER TABLE tags ADD COLUMN IF NOT EXISTS code text;
CREATE UNIQUE INDEX IF NOT EXISTS tags_code_key ON tags (code) WHERE code IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS tags_code_key;
ALTER TABLE tags DROP COLUMN IF EXISTS code;
