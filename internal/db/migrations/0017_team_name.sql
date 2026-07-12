-- +goose Up
-- Название команды 2×2 (для отображения крупнее состава). Обновляется по последнему матчу.
ALTER TABLE team_mmr ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE team_mmr DROP COLUMN IF EXISTS name;
