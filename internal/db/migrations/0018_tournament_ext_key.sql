-- +goose Up
-- Детерминированный внешний ключ матча из таблицы (режим+дата+игроки+повтор) — чтобы синк
-- обновлял тот же турнир (стабильный id, ссылки /tournament/{id} не ломаются), а не пересоздавал.
ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS ext_key text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS tournaments_ext_key_uniq ON tournaments(ext_key) WHERE ext_key <> '';

-- +goose Down
DROP INDEX IF EXISTS tournaments_ext_key_uniq;
ALTER TABLE tournaments DROP COLUMN IF EXISTS ext_key;
