-- +goose Up
-- Классификация заданий: формат матча, редкость, тип раунда. «На время» остаётся в tag (timed) и от редкости не зависит.
-- Старые задания: формат any (раздаются в 1x1 и 2x2, как раньше), редкость common, раунд any.
ALTER TABLE catalog_tasks ADD COLUMN IF NOT EXISTS format text NOT NULL DEFAULT 'any';
ALTER TABLE catalog_tasks ADD COLUMN IF NOT EXISTS rarity text NOT NULL DEFAULT 'common';
ALTER TABLE catalog_tasks ADD COLUMN IF NOT EXISTS round_type text NOT NULL DEFAULT 'any';

ALTER TABLE catalog_tasks ADD CONSTRAINT catalog_tasks_format_chk CHECK (format IN ('any', '1x1', '2x2'));
ALTER TABLE catalog_tasks ADD CONSTRAINT catalog_tasks_rarity_chk CHECK (rarity IN ('common', 'uncommon', 'rare', 'epic', 'legendary'));
ALTER TABLE catalog_tasks ADD CONSTRAINT catalog_tasks_round_type_chk CHECK (round_type IN ('any', 'free', 'paid'));
ALTER TABLE catalog_tasks ADD CONSTRAINT catalog_tasks_tag_chk CHECK (tag IN ('normal', 'timed'));

-- +goose Down
ALTER TABLE catalog_tasks DROP CONSTRAINT IF EXISTS catalog_tasks_tag_chk;
ALTER TABLE catalog_tasks DROP CONSTRAINT IF EXISTS catalog_tasks_round_type_chk;
ALTER TABLE catalog_tasks DROP CONSTRAINT IF EXISTS catalog_tasks_rarity_chk;
ALTER TABLE catalog_tasks DROP CONSTRAINT IF EXISTS catalog_tasks_format_chk;
ALTER TABLE catalog_tasks DROP COLUMN IF EXISTS round_type;
ALTER TABLE catalog_tasks DROP COLUMN IF EXISTS rarity;
ALTER TABLE catalog_tasks DROP COLUMN IF EXISTS format;
