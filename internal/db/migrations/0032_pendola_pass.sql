-- +goose Up
-- Перевал Пендола (Pendola Pass) - карта из обновления Frozen Trail. Дата появления карты нужна,
-- чтобы в пиках-банах сыгранного матча не стояли карты, которых тогда ещё не было.
ALTER TABLE maps ADD COLUMN IF NOT EXISTS added_at timestamptz NOT NULL DEFAULT 'epoch';
ALTER TABLE maps ALTER COLUMN added_at SET DEFAULT now();
INSERT INTO maps (code, name, image, sort_order) VALUES
    ('pendola_pass', 'Перевал Пендола', '/maps/pendola_pass.jpg', 7)
ON CONFLICT (code) DO NOTHING;

-- +goose Down
-- Карту из сыгранных пиков-банов не удалить: она просто уходит из пула.
UPDATE maps SET active = false WHERE code = 'pendola_pass';
DELETE FROM maps WHERE code = 'pendola_pass' AND NOT EXISTS (SELECT 1 FROM match_veto WHERE map_code = 'pendola_pass');
ALTER TABLE maps DROP COLUMN IF EXISTS added_at;
