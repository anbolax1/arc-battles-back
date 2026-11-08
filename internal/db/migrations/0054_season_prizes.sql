-- +goose Up
-- Призовой фонд сезона: общая сумма и суммы за места по порядку (первое - первым). Пустой список - призов нет.
ALTER TABLE seasons
    ADD COLUMN prize_pool   int   NOT NULL DEFAULT 0,
    ADD COLUMN prize_places int[] NOT NULL DEFAULT '{}',
    ADD COLUMN prize_note   text  NOT NULL DEFAULT '';

-- Призы 4 сезона, которые до этого были записаны в коде сайта.
UPDATE seasons
SET prize_pool   = 30000,
    prize_places = '{12000,9000,6000,3000}',
    prize_note   = 'Выплата призовых производится на баланс Steam. Автоматическая конвертация валюты для других стран.'
WHERE number = 4;

-- +goose Down
ALTER TABLE seasons DROP COLUMN prize_note, DROP COLUMN prize_places, DROP COLUMN prize_pool;
