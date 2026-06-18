-- +goose Up
-- Переход с Twitch OAuth на вход по логину/паролю.
-- Старые аккаунты были привязаны к Twitch — удаляем их вместе с зависимыми данными
-- (registrations/highlights уходят по ON DELETE CASCADE, ссылки в participants
-- обнуляются по ON DELETE SET NULL). Турниры/раунды/каталог сохраняются.
DELETE FROM users;

-- twitch_id больше не нужен; вместо него — хеш пароля.
ALTER TABLE users DROP COLUMN IF EXISTS twitch_id;
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash text NOT NULL DEFAULT '';

-- Эпоха сессий для серверной ревокации: токены с iat < tokens_valid_after недействительны
-- (logout/смена роли двигают эту отметку — «выйти» реально завершает сессию на сервере).
ALTER TABLE users ADD COLUMN IF NOT EXISTS tokens_valid_after timestamptz NOT NULL DEFAULT now();

-- Логин уникален без учёта регистра (вход «без учёта регистра», без дублей-омографов).
CREATE UNIQUE INDEX IF NOT EXISTS users_login_lower_key ON users (lower(login));

-- Роли переехали на user|superadmin (иерархия в коде). Дефолт колонки — обычный user.
ALTER TABLE users ALTER COLUMN role SET DEFAULT 'user';

-- +goose Down
DROP INDEX IF EXISTS users_login_lower_key;
ALTER TABLE users DROP COLUMN IF EXISTS tokens_valid_after;
ALTER TABLE users DROP COLUMN IF EXISTS password_hash;
ALTER TABLE users ADD COLUMN IF NOT EXISTS twitch_id text;
