-- +goose Up
-- Бонусные задания участника по раундам (catalog_tasks = пул бонусных). Игрок выбирает
-- бонусные на раунд; невыполненные «переносятся» (видны и в следующих раундах, пока не done).
-- Организатор отмечает выполнение в эфире — начисляются баллы (fixed или percent от earned).
CREATE TABLE IF NOT EXISTS round_bonus_tasks (
    id             text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    round_id       text NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    participant_id text NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    task_id        text NOT NULL REFERENCES catalog_tasks(id) ON DELETE CASCADE,
    done           boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE(round_id, participant_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_rbt_participant ON round_bonus_tasks(participant_id);
CREATE INDEX IF NOT EXISTS idx_rbt_round ON round_bonus_tasks(round_id);

-- +goose Down
DROP TABLE IF EXISTS round_bonus_tasks;
