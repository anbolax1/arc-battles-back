-- +goose Up
-- Жетоны игрока одной лентой: выдача и списание модератором (grant, ±N) и трата самим жетоном (use, -1).
-- Баланс - сумма по виду жетона.
CREATE TABLE IF NOT EXISTS token_ledger (
    id            text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    user_id       text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind          text NOT NULL, -- double | priority | reset
    action        text NOT NULL, -- grant | use
    delta         int  NOT NULL,
    -- Матч, на который поставлен жетон ×2: отменённый матч удаляется, и жетон возвращается сам.
    tournament_id text REFERENCES tournaments(id) ON DELETE CASCADE,
    season_id     text REFERENCES seasons(id) ON DELETE SET NULL, -- сезон, в котором сброшен рейтинг
    note          text NOT NULL DEFAULT '',
    created_by    text REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_token_ledger_user ON token_ledger(user_id, kind, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS token_ledger_match_user ON token_ledger(tournament_id, user_id) WHERE tournament_id IS NOT NULL;

-- Сброс рейтинга в истории MMR: на графике он подписан сбросом, а не сверкой с таблицей.
ALTER TABLE mmr_history ADD COLUMN IF NOT EXISTS reset boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE mmr_history DROP COLUMN IF EXISTS reset;
DROP TABLE IF EXISTS token_ledger;
-- Без жетонов рейтинг другой: ×2 и сбросы уходят из истории только пересчётом.
INSERT INTO app_flags (key) VALUES ('recompute_mmr') ON CONFLICT DO NOTHING;
