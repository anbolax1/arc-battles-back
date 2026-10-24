-- +goose Up
-- Когда начался каждый рейд: «Следующий раунд» начинает рейд стороны A, «Следующий игрок» - рейд другой стороны.
-- По этим отметкам график шанса знает, когда рейд закрылся, а со временем шанс научится учитывать длину рейда.
CREATE TABLE IF NOT EXISTS raid_starts (
    id             bigserial PRIMARY KEY,
    tournament_id  text NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    round_number   int  NOT NULL,
    participant_id text NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    started_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_raid_starts_tournament ON raid_starts(tournament_id, started_at);

-- +goose Down
DROP TABLE IF EXISTS raid_starts;
