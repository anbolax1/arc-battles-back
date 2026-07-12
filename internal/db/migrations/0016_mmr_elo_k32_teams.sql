-- +goose Up
-- ============ MMR: Elo K=32 + командный рейтинг 2×2 + жетон «×2 рейтинга» ============
-- Заменяет заглушечную модель (±25 за исход) на настоящий Elo с K=32.
-- Новый сезон под новую формулу: старт с чистого листа (все MMR = 1000).

-- Жетон «×2 рейтинга»: турнир нового концепта = один матч; при множителе 2 этот матч
-- считается за два — Elo применяется дважды (второй раз от уже обновлённого рейтинга,
-- компаундинг), W/L +2. По умолчанию 1 (обычный матч).
ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS rating_multiplier int NOT NULL DEFAULT 1;

-- 2×2: MMR у КОМАНДЫ (пара игроков), а не у каждого участника. Ключ команды —
-- неупорядоченная пара userId (member_a < member_b лексикографически). Старт 1000,
-- сквозной по сезонам. Текущее значение материализуется из истории: 1000 + SUM(delta).
CREATE TABLE IF NOT EXISTS team_mmr (
    team_key   text PRIMARY KEY,              -- member_a || '|' || member_b (отсортированы)
    member_a   text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    member_b   text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mmr        int  NOT NULL DEFAULT 1000,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS team_mmr_history (
    id            text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    team_key      text NOT NULL,
    tournament_id text REFERENCES tournaments(id) ON DELETE CASCADE,
    delta         int  NOT NULL,
    mmr_before    int  NOT NULL,
    mmr_after     int  NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tournament_id, team_key)
);
CREATE INDEX IF NOT EXISTS idx_team_mmr_history_team ON team_mmr_history(team_key);

-- Старт с нуля: чистим накопленную историю прошлой (заглушечной) модели — у всех снова 1000.
-- (team_mmr / team_mmr_history — новые, пустые.)
TRUNCATE TABLE mmr_history;
TRUNCATE TABLE user_mmr;

-- +goose Down
DROP TABLE IF EXISTS team_mmr_history;
DROP TABLE IF EXISTS team_mmr;
ALTER TABLE tournaments DROP COLUMN IF EXISTS rating_multiplier;
