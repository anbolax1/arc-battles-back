-- +goose Up
-- Сервер игрока меняется так же, как часы и ноки: заявка со скриншотом, которую сверяет модератор.
-- В region лежит код региона (kind = 'server'), value при этом 0.
ALTER TABLE stat_claims ADD COLUMN IF NOT EXISTS region text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE stat_claims DROP COLUMN IF EXISTS region;
