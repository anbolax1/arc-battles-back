-- +goose Up
-- Хайлайты: пользовательские клипы — твич-клип, скачанный к нам, или загруженный видео-файл.
-- Публикуются только после модерации организатором (status='approved'). Ссылку на
-- первоисточник (твич-клип) храним всегда. Файлы лежат в хранилище, в БД — относительные пути.
CREATE TABLE IF NOT EXISTS highlights (
    id            text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    user_id       text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tournament_id text REFERENCES tournaments(id) ON DELETE SET NULL,
    title         text NOT NULL DEFAULT '',
    source        text NOT NULL,                     -- 'twitch_clip' | 'upload'
    source_url    text NOT NULL DEFAULT '',          -- ссылка на первоисточник (твич-клип)
    file_path     text NOT NULL DEFAULT '',          -- относительный путь к видео в хранилище
    thumb_path    text NOT NULL DEFAULT '',          -- относительный путь к превью
    duration      int  NOT NULL DEFAULT 0,           -- длительность, сек (если удалось определить)
    status        text NOT NULL DEFAULT 'processing',-- processing|pending|approved|rejected|failed
    reject_reason text NOT NULL DEFAULT '',
    reviewed_by   text REFERENCES users(id) ON DELETE SET NULL,
    reviewed_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_highlights_status ON highlights(status);
CREATE INDEX IF NOT EXISTS idx_highlights_tournament ON highlights(tournament_id);
CREATE INDEX IF NOT EXISTS idx_highlights_user ON highlights(user_id);

-- +goose Down
DROP TABLE IF EXISTS highlights;
