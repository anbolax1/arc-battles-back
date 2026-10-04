-- +goose Up
-- Сколько матчей засчитывает матч. ×2 прошлых сезонов и таблицы организатора - два матча подряд
-- (каждый от обновлённого рейтинга); с 3 сезона ×2 - один матч с удвоенным изменением MMR.
ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS games int NOT NULL DEFAULT 1;
UPDATE tournaments SET games = rating_multiplier
WHERE season_id IS NULL OR season_id IN (SELECT id FROM seasons WHERE status = 'finished');

-- Изменение MMR стороны, взятое из внешнего источника как есть (arcarena.ru): заново не считается.
ALTER TABLE participants ADD COLUMN IF NOT EXISTS mmr_delta int;

-- Сверка рейтинга игрока с официальными цифрами на дату: разница идёт в историю сезона.
CREATE TABLE IF NOT EXISTS mmr_corrections (
    id        text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    user_id   text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    season_id text REFERENCES seasons(id) ON DELETE CASCADE,
    delta     int NOT NULL,
    at        timestamptz NOT NULL,
    source    text NOT NULL DEFAULT '',
    note      text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS mmr_corrections_source ON mmr_corrections (source);

INSERT INTO app_flags (key) VALUES ('recompute_mmr') ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS mmr_corrections;
ALTER TABLE participants DROP COLUMN IF EXISTS mmr_delta;
ALTER TABLE tournaments DROP COLUMN IF EXISTS games;
