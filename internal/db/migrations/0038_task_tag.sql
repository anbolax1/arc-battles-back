-- +goose Up
-- Тег задания на экране: normal - «обычное», timed - «на время». Протокол выполняют до 15-й минуты, он на время.
ALTER TABLE catalog_tasks ADD COLUMN IF NOT EXISTS tag text NOT NULL DEFAULT 'normal';
UPDATE catalog_tasks SET tag = 'timed' WHERE category = 'protocol';

-- +goose Down
ALTER TABLE catalog_tasks DROP COLUMN IF EXISTS tag;
