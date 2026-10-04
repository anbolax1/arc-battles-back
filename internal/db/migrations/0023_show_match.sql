-- +goose Up
-- Шоу-матч объявляется заранее и попадает в расписание; у него три раунда и свой порядок пиков-банов.
ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS format text NOT NULL DEFAULT 'match'; -- match | show

-- +goose Down
ALTER TABLE tournaments DROP COLUMN IF EXISTS format;
