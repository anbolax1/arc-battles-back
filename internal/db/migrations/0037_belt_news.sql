-- +goose Up
-- Новости о поясе лиги сайт пишет сам. Здесь события пояса, о которых он уже знает, и новость о каждом:
-- удалённая в кабинете новость обнуляет news_id, но событие остаётся известным и второй раз не пишется.
CREATE TABLE IF NOT EXISTS belt_news (
    key        text PRIMARY KEY,
    news_id    text REFERENCES news(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Рисунок пояса у новости без картинки: ник на поясе, звёзды защит, ничей ли пояс.
ALTER TABLE news ADD COLUMN IF NOT EXISTS belt jsonb;

-- +goose Down
ALTER TABLE news DROP COLUMN IF EXISTS belt;
DROP TABLE IF EXISTS belt_news;
