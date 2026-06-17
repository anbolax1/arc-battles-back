-- +goose Up
-- Лёгкое превью-видео (5 сек, без звука, низкое разрешение) для автоплея в «стене» на главной;
-- полный клип грузится только по клику. Путь относительный, как у file_path/thumb_path.
ALTER TABLE highlights ADD COLUMN IF NOT EXISTS preview_path text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE highlights DROP COLUMN IF EXISTS preview_path;
