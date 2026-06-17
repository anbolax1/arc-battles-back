-- +goose Up
-- Счётчики вместо однократного применения: задание/штраф можно применить несколько раз.
-- Стартовое задание: times — сколько раз зачтено (баллы = times × points), исполнитель = completed_by.
ALTER TABLE round_starter_tasks ADD COLUMN IF NOT EXISTS times int NOT NULL DEFAULT 0;
UPDATE round_starter_tasks SET times = 1 WHERE completed_by IS NOT NULL AND times = 0;
ALTER TABLE round_starter_tasks DROP COLUMN IF EXISTS points_awarded;
ALTER TABLE round_starter_tasks DROP COLUMN IF EXISTS completed_at;

-- Применённые усложнения участнику в раунде (штраф = times × величина).
CREATE TABLE IF NOT EXISTS round_penalties (
    id              text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    round_id        text NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    participant_id  text NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    complication_id text NOT NULL REFERENCES catalog_complications(id) ON DELETE CASCADE,
    times           int  NOT NULL DEFAULT 0,
    UNIQUE(round_id, participant_id, complication_id)
);
CREATE INDEX IF NOT EXISTS idx_rp_round ON round_penalties(round_id);
CREATE INDEX IF NOT EXISTS idx_rp_participant ON round_penalties(participant_id);

-- +goose Down
DROP TABLE IF EXISTS round_penalties;
ALTER TABLE round_starter_tasks DROP COLUMN IF EXISTS times;
ALTER TABLE round_starter_tasks ADD COLUMN IF NOT EXISTS points_awarded int;
ALTER TABLE round_starter_tasks ADD COLUMN IF NOT EXISTS completed_at timestamptz;
