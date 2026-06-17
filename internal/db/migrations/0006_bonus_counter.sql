-- +goose Up
-- Бонусные задания — на счётчик (как стартовые): times вместо done.
-- times=0 → не выполнено (переносится на следующий раунд); times≥1 → зачтено (баллы = times × величина).
ALTER TABLE round_bonus_tasks ADD COLUMN IF NOT EXISTS times int NOT NULL DEFAULT 0;
UPDATE round_bonus_tasks SET times = 1 WHERE done = true AND times = 0;
ALTER TABLE round_bonus_tasks DROP COLUMN IF EXISTS done;

-- +goose Down
ALTER TABLE round_bonus_tasks ADD COLUMN IF NOT EXISTS done boolean NOT NULL DEFAULT false;
UPDATE round_bonus_tasks SET done = (times > 0);
ALTER TABLE round_bonus_tasks DROP COLUMN IF EXISTS times;
