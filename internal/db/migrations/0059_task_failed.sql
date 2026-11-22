-- +goose Up
-- Задание на время провалено (не уложились в срок): остаётся на экране с отметкой, очков не даёт.
ALTER TABLE round_bonus_tasks ADD COLUMN IF NOT EXISTS failed boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE round_bonus_tasks DROP COLUMN IF EXISTS failed;
