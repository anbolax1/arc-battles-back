-- +goose Up
-- Вид стартового задания (как у бонусных): pve | pvp | mixed.
ALTER TABLE starter_tasks ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'mixed';

-- +goose Down
ALTER TABLE starter_tasks DROP COLUMN IF EXISTS kind;
