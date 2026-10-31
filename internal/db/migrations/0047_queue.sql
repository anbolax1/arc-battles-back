-- +goose Up
-- Очередь выступлений: сессия на стрим, записи игроков и команд, матчи очереди и журнал событий.
-- Порядок и защита от дублей держатся на ограничениях БД; изменения идут под блокировкой строки сессии.

CREATE TABLE IF NOT EXISTS queue_sessions (
    id                 text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    title              text NOT NULL,
    format             text NOT NULL CHECK (format IN ('1x1', '2x2')),
    status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'finished')),
    registration_open  boolean NOT NULL DEFAULT true,
    paused             boolean NOT NULL DEFAULT false,
    -- Сколько секунд у вызванного на подтверждение готовности; само время ничего не исключает.
    confirm_timeout_sec int NOT NULL DEFAULT 60 CHECK (confirm_timeout_sec BETWEEN 10 AND 600),
    -- Сколько записей выходит на один матч: две стороны или один выступающий.
    match_size         smallint NOT NULL DEFAULT 2 CHECK (match_size IN (1, 2)),
    -- Растёт с каждым изменением очереди: по нему клиенты понимают, что состояние устарело.
    rev                bigint NOT NULL DEFAULT 1,
    created_by         text REFERENCES users(id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    finished_at        timestamptz
);
-- Одна живая очередь на формат: отдельные очереди 1×1 и 2×2 идут параллельно.
CREATE UNIQUE INDEX IF NOT EXISTS queue_sessions_one_active ON queue_sessions(format) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_queue_sessions_created ON queue_sessions(created_at DESC);

CREATE TABLE IF NOT EXISTS queue_entries (
    id              text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    session_id      text NOT NULL REFERENCES queue_sessions(id) ON DELETE CASCADE,
    team_key        text,                          -- команда 2×2; у одиночной записи пусто
    team_name       text NOT NULL DEFAULT '',
    status          text NOT NULL DEFAULT 'waiting'
                    CHECK (status IN ('waiting', 'invited', 'ready', 'performing', 'skipped', 'left', 'removed')),
    position        int CHECK (position IS NULL OR position > 0),
    performed_count int NOT NULL DEFAULT 0,
    skipped_count   int NOT NULL DEFAULT 0,
    joined_at       timestamptz NOT NULL DEFAULT now(),
    invited_at      timestamptz,
    invite_expires_at timestamptz,
    added_by        text REFERENCES users(id) ON DELETE SET NULL, -- кто добавил вручную
    closed_at       timestamptz,
    UNIQUE (id, session_id),
    -- Две записи не делят одну позицию; проверка в конце транзакции, поэтому порядок можно перенумеровать разом.
    CONSTRAINT queue_entries_position_uq UNIQUE (session_id, position) DEFERRABLE INITIALLY DEFERRED,
    -- Позиция есть только у тех, кто в очереди.
    CONSTRAINT queue_entries_position_active CHECK ((status IN ('left', 'removed')) = (position IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_queue_entries_session ON queue_entries(session_id, position);

CREATE TABLE IF NOT EXISTS queue_entry_members (
    entry_id   text NOT NULL,
    session_id text NOT NULL,
    user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    active     boolean NOT NULL DEFAULT true,      -- запись ещё в очереди; сбрасывает триггер ниже
    ready_at   timestamptz,                        -- когда игрок подтвердил готовность
    PRIMARY KEY (entry_id, user_id),
    FOREIGN KEY (entry_id, session_id) REFERENCES queue_entries(id, session_id) ON DELETE CASCADE
);
-- Игрок стоит в очереди сессии один раз: ни дубля, ни второй команды с тем же игроком.
CREATE UNIQUE INDEX IF NOT EXISTS queue_member_once ON queue_entry_members(session_id, user_id) WHERE active;
CREATE INDEX IF NOT EXISTS idx_queue_members_user ON queue_entry_members(user_id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION queue_entry_close_members() RETURNS trigger AS $$
BEGIN
    UPDATE queue_entry_members SET active = false WHERE entry_id = NEW.id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS queue_entry_closed ON queue_entries;
CREATE TRIGGER queue_entry_closed
    AFTER UPDATE OF status ON queue_entries
    FOR EACH ROW
    WHEN (NEW.status IN ('left', 'removed') AND OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION queue_entry_close_members();

CREATE TABLE IF NOT EXISTS queue_matches (
    id            text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    session_id    text NOT NULL REFERENCES queue_sessions(id) ON DELETE CASCADE,
    number        int NOT NULL,
    status        text NOT NULL DEFAULT 'preparing' CHECK (status IN ('preparing', 'live', 'finished', 'cancelled')),
    created_by    text REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    started_at    timestamptz,
    finished_at   timestamptz,
    UNIQUE (session_id, number)
);
-- В сессии не больше одного матча в подготовке или в эфире.
CREATE UNIQUE INDEX IF NOT EXISTS queue_match_one_open ON queue_matches(session_id) WHERE status IN ('preparing', 'live');

CREATE TABLE IF NOT EXISTS queue_match_entries (
    match_id text NOT NULL REFERENCES queue_matches(id) ON DELETE CASCADE,
    entry_id text NOT NULL REFERENCES queue_entries(id) ON DELETE CASCADE,
    side     smallint NOT NULL CHECK (side IN (1, 2)),
    label    text NOT NULL,                        -- как записан выступающий на момент вызова
    mmr      int NOT NULL DEFAULT 0,
    snapshot jsonb NOT NULL DEFAULT '[]',          -- игроки записи на момент вызова
    PRIMARY KEY (match_id, entry_id),
    UNIQUE (match_id, side)
);

CREATE TABLE IF NOT EXISTS queue_events (
    id         bigserial PRIMARY KEY,
    session_id text NOT NULL REFERENCES queue_sessions(id) ON DELETE CASCADE,
    at         timestamptz NOT NULL DEFAULT now(),
    kind       text NOT NULL,
    actor_id   text REFERENCES users(id) ON DELETE SET NULL,
    entry_id   text,
    match_id   text,
    label      text NOT NULL DEFAULT '',
    data       jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_queue_events_session ON queue_events(session_id, id);

-- +goose Down
DROP TABLE IF EXISTS queue_events;
DROP TABLE IF EXISTS queue_match_entries;
DROP TABLE IF EXISTS queue_matches;
DROP TRIGGER IF EXISTS queue_entry_closed ON queue_entries;
DROP FUNCTION IF EXISTS queue_entry_close_members();
DROP TABLE IF EXISTS queue_entry_members;
DROP TABLE IF EXISTS queue_entries;
DROP TABLE IF EXISTS queue_sessions;
