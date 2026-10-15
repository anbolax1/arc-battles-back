-- +goose Up
-- Задание может быть сразу на нескольких картах; пустой список - общее задание на любую карту.
ALTER TABLE catalog_tasks ADD COLUMN IF NOT EXISTS map_codes text[] NOT NULL DEFAULT '{}';
UPDATE catalog_tasks SET map_codes = ARRAY[map_code] WHERE map_code IS NOT NULL;
ALTER TABLE catalog_tasks DROP COLUMN IF EXISTS map_code;

-- +goose Down
-- У задания на нескольких картах остаётся первая карта.
ALTER TABLE catalog_tasks ADD COLUMN IF NOT EXISTS map_code text REFERENCES maps(code) ON DELETE SET NULL;
UPDATE catalog_tasks SET map_code = map_codes[1] WHERE cardinality(map_codes) > 0;
ALTER TABLE catalog_tasks DROP COLUMN IF EXISTS map_codes;
