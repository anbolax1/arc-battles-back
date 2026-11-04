-- +goose Up
-- Прежние логин и имя игрока после принудительной смены ника: по ним по-прежнему можно войти.
-- Имя уникально без учёта регистра на все аккаунты, поэтому новый игрок не может занять чужой прежний ник.
CREATE TABLE IF NOT EXISTS user_login_aliases (
    alias      text NOT NULL,
    user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS user_login_aliases_alias_key ON user_login_aliases (lower(alias));
CREATE INDEX IF NOT EXISTS idx_user_login_aliases_user ON user_login_aliases (user_id);

-- +goose Down
DROP TABLE IF EXISTS user_login_aliases;
