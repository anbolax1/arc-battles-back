-- +goose Up
-- Новости лиги пишет организатор в кабинете. Без даты публикации новость - черновик, с датой
-- в будущем - выйдет сама в этот момент.
CREATE TABLE IF NOT EXISTS news (
    id           text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    title        text NOT NULL,
    body         text NOT NULL DEFAULT '',
    image_path   text NOT NULL DEFAULT '',
    published_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_news_published ON news (published_at DESC) WHERE published_at IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS news;
