-- +goose Up
-- Время завершения матча: от него идёт кулдаун игрока, по нему очередь видит «последний матч».
ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS finished_at timestamptz;

-- Точное время завершения прошлых матчей известно только у матчей очереди. У остальных остаётся пустым,
-- и вместо него берётся время начала.
UPDATE tournaments t SET finished_at = m.finished_at
FROM queue_matches m
WHERE m.tournament_id = t.id AND m.status = 'finished' AND m.finished_at IS NOT NULL
  AND t.status = 'finished' AND t.finished_at IS NULL;

-- Время ставит база в момент перехода матча в «завершён»: его не пропустит ни пульт, ни старое расписание.
-- Матч, которому вернули другой статус, времени завершения не имеет.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION tournaments_finished_at() RETURNS trigger AS $$
BEGIN
    IF NEW.status IS DISTINCT FROM 'finished' THEN
        NEW.finished_at := NULL;
    ELSIF OLD.status IS DISTINCT FROM 'finished' THEN
        NEW.finished_at := COALESCE(NEW.finished_at, now());
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS tournaments_finished_at ON tournaments;
CREATE TRIGGER tournaments_finished_at
    BEFORE UPDATE OF status ON tournaments
    FOR EACH ROW EXECUTE FUNCTION tournaments_finished_at();

-- Матчи игрока ищутся по нему самому: в 1×1 он в user_id, в 2×2 - в составе members.
CREATE INDEX IF NOT EXISTS idx_participants_user ON participants(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_participants_members ON participants USING gin (members jsonb_path_ops);

-- Ручные настройки кулдауна игрока. Сам кулдаун считается от завершения его последнего матча,
-- поэтому строка есть только у тех, кому организатор что-то менял.
CREATE TABLE IF NOT EXISTS player_cooldowns (
    user_id      text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    -- Своя длительность в минутах; пусто - общая из site_settings.
    minutes      int CHECK (minutes IS NULL OR minutes BETWEEN 1 AND 10080),
    disabled     boolean NOT NULL DEFAULT false,
    -- Кулдаун снят в этот момент: матчи, завершённые раньше, его больше не дают.
    cleared_at   timestamptz,
    -- Кулдаун, поставленный вручную, идёт до этого момента.
    manual_until timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   text REFERENCES users(id) ON DELETE SET NULL
);

-- Журнал правок кулдаунов. Ник хранится на момент правки: после удаления аккаунта запись остаётся понятной.
CREATE TABLE IF NOT EXISTS cooldown_log (
    id       bigserial PRIMARY KEY,
    at       timestamptz NOT NULL DEFAULT now(),
    actor_id text REFERENCES users(id) ON DELETE SET NULL,
    user_id  text REFERENCES users(id) ON DELETE SET NULL, -- пусто - общая настройка
    label    text NOT NULL DEFAULT '',
    action   text NOT NULL,
    data     jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_cooldown_log_user ON cooldown_log(user_id, id DESC);

-- Общая длительность кулдауна в минутах: 3 часа. 0 - кулдауна нет ни у кого, кроме заданных вручную.
INSERT INTO site_settings (key, value) VALUES ('queue_cooldown_minutes', '180') ON CONFLICT (key) DO NOTHING;

-- +goose Down
DELETE FROM site_settings WHERE key = 'queue_cooldown_minutes';
DROP TABLE IF EXISTS cooldown_log;
DROP TABLE IF EXISTS player_cooldowns;
DROP INDEX IF EXISTS idx_participants_members;
DROP INDEX IF EXISTS idx_participants_user;
DROP TRIGGER IF EXISTS tournaments_finished_at ON tournaments;
DROP FUNCTION IF EXISTS tournaments_finished_at();
ALTER TABLE tournaments DROP COLUMN IF EXISTS finished_at;
