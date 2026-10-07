-- +goose Up
-- Общие настройки сайта, одинаковые для всех посетителей: какой дизайн показывать.
CREATE TABLE IF NOT EXISTS site_settings (
    key        text PRIMARY KEY,
    value      text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO site_settings (key, value) VALUES ('design', 'classic') ON CONFLICT (key) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS site_settings;
