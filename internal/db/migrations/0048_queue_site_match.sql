-- +goose Up
-- Матч очереди создаёт настоящий матч сайта на двоих, поэтому размер матча больше не настраивается,
-- а у матча очереди есть ссылка на матч сайта: когда тот завершён или отменён, очередь продвигается сама.
ALTER TABLE queue_sessions DROP COLUMN IF EXISTS match_size;
ALTER TABLE queue_matches ADD COLUMN IF NOT EXISTS tournament_id text REFERENCES tournaments(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_queue_matches_tournament ON queue_matches(tournament_id) WHERE tournament_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_queue_matches_tournament;
ALTER TABLE queue_matches DROP COLUMN IF EXISTS tournament_id;
ALTER TABLE queue_sessions ADD COLUMN IF NOT EXISTS match_size smallint NOT NULL DEFAULT 2 CHECK (match_size IN (1, 2));
