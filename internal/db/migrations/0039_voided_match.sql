-- +goose Up
-- Незасчитанный матч сыгран, но без исхода: в победы, серии, нашивки за победу и пояс не идёт.
-- Победителю MMR остаётся, проигравшему возвращается: матч испортил не он.
ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS voided boolean NOT NULL DEFAULT false;
ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS void_reason text NOT NULL DEFAULT '';

-- 23.10.2026 ERNON vs DIKEY.
UPDATE tournaments SET voided = true, void_reason = 'DIKEY убили читеры', updated_at = now()
WHERE id = '3f6add11-beed-4da8-9e7f-8b6fb24d69b3';

INSERT INTO app_flags (key) VALUES ('recompute_mmr') ON CONFLICT DO NOTHING;

-- +goose Down
ALTER TABLE tournaments DROP COLUMN IF EXISTS void_reason;
ALTER TABLE tournaments DROP COLUMN IF EXISTS voided;
INSERT INTO app_flags (key) VALUES ('recompute_mmr') ON CONFLICT DO NOTHING;
