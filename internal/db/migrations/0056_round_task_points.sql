-- +goose Up
-- Очки выданного задания фиксируются при раздаче (по редкости): правка каталога не меняет счёт сыгранных матчей.
-- Для старых выдач берутся очки из каталога, как они считались раньше.
ALTER TABLE round_bonus_tasks ADD COLUMN IF NOT EXISTS points int;
UPDATE round_bonus_tasks rbt SET points = ct.points FROM catalog_tasks ct WHERE ct.id = rbt.task_id AND rbt.points IS NULL;
ALTER TABLE round_bonus_tasks ALTER COLUMN points SET NOT NULL;

-- +goose Down
ALTER TABLE round_bonus_tasks DROP COLUMN IF EXISTS points;
