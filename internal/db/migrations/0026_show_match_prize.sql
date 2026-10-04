-- +goose Up
-- Шоу-матч анонсируют с призом и картинкой-превью; картинка лежит в хранилище медиа.
ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS prize text NOT NULL DEFAULT '';
ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS preview_path text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tournaments DROP COLUMN IF EXISTS preview_path;
ALTER TABLE tournaments DROP COLUMN IF EXISTS prize;
