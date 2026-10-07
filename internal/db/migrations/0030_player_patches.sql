-- +goose Up
-- Нашивки игроков за сезон. Таблица пересчитывается по матчам целиком: дата и матч нашивки берутся
-- из матча, где она получена, поэтому от пересчёта не меняются. У «Охотника» строка на каждую ступень.
CREATE TABLE IF NOT EXISTS player_patches (
    user_id       text        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    season_id     text        NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    code          text        NOT NULL,
    tier          int         NOT NULL DEFAULT 1,
    tournament_id text        REFERENCES tournaments(id) ON DELETE CASCADE,
    earned_at     timestamptz NOT NULL,
    -- сезон ещё идёт: итоговая нашивка («Первый номер» и другие) закрепится при закрытии сезона
    provisional   boolean     NOT NULL DEFAULT false,
    detail        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (user_id, season_id, code, tier)
);
CREATE INDEX IF NOT EXISTS player_patches_season_idx ON player_patches (season_id, code);
CREATE INDEX IF NOT EXISTS player_patches_tournament_idx ON player_patches (tournament_id);

-- +goose Down
DROP TABLE IF EXISTS player_patches;
