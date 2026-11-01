-- +goose Up
-- Какие по редкости задания игрок хочет получать: несколько редкостей, по умолчанию все пять (как раньше).
ALTER TABLE users ADD COLUMN IF NOT EXISTS task_rarities text[] NOT NULL
    DEFAULT ARRAY['common', 'uncommon', 'rare', 'epic', 'legendary'];
ALTER TABLE users ADD CONSTRAINT users_task_rarities_chk
    CHECK (cardinality(task_rarities) > 0 AND task_rarities <@ ARRAY['common', 'uncommon', 'rare', 'epic', 'legendary']);

-- Ручная настройка стороны при создании матча; NULL - берутся предпочтения игроков из профиля.
ALTER TABLE participants ADD COLUMN IF NOT EXISTS task_rarities text[];
ALTER TABLE participants ADD CONSTRAINT participants_task_rarities_chk
    CHECK (task_rarities IS NULL OR (cardinality(task_rarities) > 0
           AND task_rarities <@ ARRAY['common', 'uncommon', 'rare', 'epic', 'legendary']));

-- +goose Down
ALTER TABLE participants DROP CONSTRAINT IF EXISTS participants_task_rarities_chk;
ALTER TABLE participants DROP COLUMN IF EXISTS task_rarities;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_task_rarities_chk;
ALTER TABLE users DROP COLUMN IF EXISTS task_rarities;
