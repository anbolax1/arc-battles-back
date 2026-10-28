-- +goose Up
-- Любимое игрока в профиле: заполняет он сам, если у него роль подписчика или выше.
CREATE TABLE IF NOT EXISTS player_favorites (
    user_id    text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    teammates  text[] NOT NULL DEFAULT '{}', -- id игроков в выбранном порядке, до пяти
    map_code   text NOT NULL DEFAULT '',
    weapon     text NOT NULL DEFAULT '',
    augment    text NOT NULL DEFAULT '',
    shield     text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS player_favorites;
