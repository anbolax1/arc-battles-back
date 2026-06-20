-- +goose Up
-- Сезоны: периоды рейтинга. Турнир привязан к сезону; рейтинг считается в рамках
-- сезона (или all-time без фильтра). Ровно один активный сезон одновременно.
CREATE TABLE IF NOT EXISTS seasons (
    id         text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    name       text NOT NULL,
    status     text NOT NULL DEFAULT 'active', -- active | finished
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Не более одного активного сезона (как один live-турнир).
CREATE UNIQUE INDEX IF NOT EXISTS seasons_one_active ON seasons ((status)) WHERE status = 'active';

ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS season_id text REFERENCES seasons(id) ON DELETE SET NULL;

-- Посев первого сезона + бэкфилл всех существующих турниров в него.
INSERT INTO seasons (name, status) VALUES ('Сезон 1', 'active');
UPDATE tournaments SET season_id = (SELECT id FROM seasons ORDER BY created_at LIMIT 1) WHERE season_id IS NULL;

-- +goose Down
ALTER TABLE tournaments DROP COLUMN IF EXISTS season_id;
DROP TABLE IF EXISTS seasons;
