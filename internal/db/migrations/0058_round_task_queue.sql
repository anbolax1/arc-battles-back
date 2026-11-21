-- +goose Up
-- Обычные задания стороны выдаются по одному: набор на раунд собирается сразу, но на экране только первое,
-- следующее открывается, когда выполнено текущее. Невыданный остаток лежит в очереди.
CREATE TABLE IF NOT EXISTS round_task_queue (
    round_id       text NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    participant_id text NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    position       int  NOT NULL,
    task_id        text NOT NULL REFERENCES catalog_tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (round_id, participant_id, position)
);
-- Место выданного задания в очереди; у заданий вне очереди (старые выдачи, ручные) пусто.
ALTER TABLE round_bonus_tasks ADD COLUMN IF NOT EXISTS queue_pos int;

-- +goose Down
ALTER TABLE round_bonus_tasks DROP COLUMN IF EXISTS queue_pos;
DROP TABLE IF EXISTS round_task_queue;
