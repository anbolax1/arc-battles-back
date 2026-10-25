-- +goose Up
-- В матче 2×2 ноки и ручные очки ставятся за игрока команды. Сумма стороны остаётся в round_entries -
-- по ней счёт, шанс и статистика, а здесь записано, чья она.
CREATE TABLE IF NOT EXISTS round_member_entries (
    round_id       text NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    participant_id text NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    user_id        text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    points         int  NOT NULL DEFAULT 0, -- вместе с очками за ноки, как в round_entries
    knocks         int  NOT NULL DEFAULT 0,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (round_id, participant_id, user_id)
);

-- Задание команды засчитывается один раз - тому игроку, кто его выполнил.
ALTER TABLE round_bonus_tasks ADD COLUMN IF NOT EXISTS completed_user_id text REFERENCES users(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE round_bonus_tasks DROP COLUMN IF EXISTS completed_user_id;
DROP TABLE IF EXISTS round_member_entries;
