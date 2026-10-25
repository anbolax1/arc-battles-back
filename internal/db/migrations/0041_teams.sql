-- +goose Up
-- Команды 2×2, которые заводит организатор: пара игроков и название. MMR у пары, поэтому ключ - id игроков
-- по порядку через «|», как в team_mmr: состав сыгравшей команды уже не меняется.
CREATE TABLE IF NOT EXISTS teams (
    team_key   text PRIMARY KEY,
    member_a   text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    member_b   text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_teams_member_a ON teams(member_a);
CREATE INDEX IF NOT EXISTS idx_teams_member_b ON teams(member_b);

-- Команды сыгранных матчей сразу в списке: их можно снова выставить на матч.
INSERT INTO teams (team_key, member_a, member_b, name, created_at)
SELECT tm.team_key, tm.member_a, tm.member_b,
       COALESCE(NULLIF(tm.name, ''),
                (SELECT COALESCE(NULLIF(u.display_name, ''), u.login) FROM users u WHERE u.id = tm.member_a) || ' & ' ||
                (SELECT COALESCE(NULLIF(u.display_name, ''), u.login) FROM users u WHERE u.id = tm.member_b)),
       COALESCE((SELECT MIN(h.created_at) FROM team_mmr_history h WHERE h.team_key = tm.team_key), now())
FROM team_mmr tm
ON CONFLICT (team_key) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS teams;
