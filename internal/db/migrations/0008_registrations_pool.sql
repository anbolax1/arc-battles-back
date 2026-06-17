-- +goose Up
-- Заявки больше не привязаны к турниру: единый пул на пользователя.
-- tournament_id теперь означает «в какой турнир поставлен» (NULL, пока в пуле).

ALTER TABLE registrations DROP CONSTRAINT IF EXISTS registrations_tournament_id_fkey;
ALTER TABLE registrations DROP CONSTRAINT IF EXISTS registrations_tournament_id_user_id_key;
ALTER TABLE registrations ALTER COLUMN tournament_id DROP NOT NULL;

-- Схлопываем прежние заявки в одну на пользователя (оставляем самую свежую).
DELETE FROM registrations a
  USING registrations b
  WHERE a.user_id = b.user_id AND a.created_at < b.created_at;

-- Прежние заявки были привязаны к турниру и могли быть accepted/declined — возвращаем в пул,
-- чтобы организатор заново распределил по новому флоу.
UPDATE registrations SET tournament_id = NULL, status = 'pending', decided_at = NULL;

ALTER TABLE registrations ADD CONSTRAINT registrations_user_id_key UNIQUE (user_id);
ALTER TABLE registrations
  ADD CONSTRAINT registrations_tournament_id_fkey
  FOREIGN KEY (tournament_id) REFERENCES tournaments(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE registrations DROP CONSTRAINT IF EXISTS registrations_user_id_key;
ALTER TABLE registrations DROP CONSTRAINT IF EXISTS registrations_tournament_id_fkey;
ALTER TABLE registrations
  ADD CONSTRAINT registrations_tournament_id_fkey
  FOREIGN KEY (tournament_id) REFERENCES tournaments(id) ON DELETE CASCADE;
