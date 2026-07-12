-- +goose Up
-- Одноразовые ссылки активации аккаунта-заглушки: организатор выдаёт токен, игрок задаёт
-- пароль и получает доступ. Токен живёт, пока игрок не активировал аккаунт (сменил заглушку).
CREATE TABLE IF NOT EXISTS account_claim_tokens (
    token      text PRIMARY KEY,
    user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_claim_tokens_user ON account_claim_tokens(user_id);

-- +goose Down
DROP TABLE IF EXISTS account_claim_tokens;
