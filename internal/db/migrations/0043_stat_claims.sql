-- +goose Up
-- Часы в игре и ноки за всё время игрок вписывает сам и прикладывает скриншот из игры. Число видно
-- в профиле сразу с пометкой «не подтверждено», пока модератор не сверит его со скриншотом.
CREATE TABLE IF NOT EXISTS stat_claims (
    id         text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       text NOT NULL,                   -- hours | knocks
    value      int  NOT NULL,
    screenshot text NOT NULL,                   -- файл в хранилище медиа
    status     text NOT NULL DEFAULT 'pending', -- pending | approved | rejected
    reason     text NOT NULL DEFAULT '',        -- почему отклонено: игрок видит это в своём профиле
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    decided_by text REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_stat_claims_user ON stat_claims(user_id, kind, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_stat_claims_status ON stat_claims(status, created_at);

-- +goose Down
DROP TABLE IF EXISTS stat_claims;
