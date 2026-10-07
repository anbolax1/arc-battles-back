-- +goose Up
-- Номер сезона - для адреса страницы итогов (/season/3). Прежним сезонам берётся из названия, если в нём
-- одно короткое число и оно ни у кого не повторяется; новым ставится следующий по счёту.
ALTER TABLE seasons ADD COLUMN IF NOT EXISTS number int;
WITH parsed AS (
    SELECT id, substring(name from '^\D*(\d{1,3})\D*$')::int AS n FROM seasons
)
UPDATE seasons s SET number = p.n
FROM parsed p
WHERE s.id = p.id AND p.n IS NOT NULL AND s.number IS NULL
  AND (SELECT count(*) FROM parsed q WHERE q.n = p.n) = 1;
CREATE UNIQUE INDEX IF NOT EXISTS seasons_number_uniq ON seasons (number) WHERE number IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS seasons_number_uniq;
ALTER TABLE seasons DROP COLUMN IF EXISTS number;
