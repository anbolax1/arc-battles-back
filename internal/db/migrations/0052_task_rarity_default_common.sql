-- +goose Up
-- По умолчанию игрок получает только обычные задания; другие редкости он выбирает сам в профиле.
-- Те, у кого стоят все пять редкостей, ничего не выбирали: предпочтения появились вместе с этим умолчанием.
ALTER TABLE users ALTER COLUMN task_rarities SET DEFAULT ARRAY['common'];
UPDATE users SET task_rarities = ARRAY['common']
WHERE task_rarities = ARRAY['common', 'uncommon', 'rare', 'epic', 'legendary'];

-- +goose Down
-- Выбранное игроками остаётся как есть, возвращается только прежнее умолчание.
ALTER TABLE users ALTER COLUMN task_rarities SET DEFAULT ARRAY['common', 'uncommon', 'rare', 'epic', 'legendary'];
