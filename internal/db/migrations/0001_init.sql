-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id           text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    twitch_id    text UNIQUE NOT NULL,
    login        text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    avatar_url   text NOT NULL DEFAULT '',
    email        text NOT NULL DEFAULT '',
    role         text NOT NULL DEFAULT 'viewer',
    embark_id    text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tournaments (
    id                    text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    title                 text NOT NULL,
    mode                  text NOT NULL DEFAULT '1x1',   -- 1x1 | 2x2
    status                text NOT NULL DEFAULT 'upcoming', -- draft|upcoming|live|finished
    total_rounds          int  NOT NULL DEFAULT 3,
    maps                  jsonb NOT NULL DEFAULT '[]',
    starts_at             timestamptz,
    winner_participant_id text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS participants (
    id            text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tournament_id text NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    kind          text NOT NULL DEFAULT 'player',  -- player | team
    user_id       text REFERENCES users(id) ON DELETE SET NULL,
    name          text NOT NULL,
    seed          int  NOT NULL DEFAULT 0,
    total_points  int  NOT NULL DEFAULT 0,
    members       jsonb NOT NULL DEFAULT '[]',      -- [{name, userId?}] для режима 2x2
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_participants_tournament ON participants(tournament_id);

CREATE TABLE IF NOT EXISTS rounds (
    id            text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tournament_id text NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    number        int  NOT NULL,
    map           text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT 'pending',
    UNIQUE(tournament_id, number)
);

CREATE TABLE IF NOT EXISTS round_entries (
    id             text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    round_id       text NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    participant_id text NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    points         int  NOT NULL DEFAULT 0,
    tasks          jsonb NOT NULL DEFAULT '[]',
    bonus          jsonb NOT NULL DEFAULT '[]',
    complications  jsonb NOT NULL DEFAULT '[]',
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE(round_id, participant_id)
);

CREATE TABLE IF NOT EXISTS registrations (
    id            text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tournament_id text NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    user_id       text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    embark_id     text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT 'pending', -- pending|accepted|declined
    note          text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    decided_at    timestamptz,
    UNIQUE(tournament_id, user_id)
);

-- Справочник заданий (он же пул «бонусных заданий»: участник выбирает по 2 на раунд).
CREATE TABLE IF NOT EXISTS catalog_tasks (
    id         text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    text       text NOT NULL,
    points     int  NOT NULL DEFAULT 1,          -- величина: баллы (fixed) или процент (percent)
    value_type text NOT NULL DEFAULT 'fixed',     -- fixed | percent
    kind       text NOT NULL DEFAULT 'mixed',     -- pve | pvp | mixed
    source     text NOT NULL DEFAULT 'official',  -- official | boosty
    author     text NOT NULL DEFAULT '',          -- ник автора (для заданий от Boosty)
    title      text NOT NULL DEFAULT '',          -- «титул» автора, напр. «Архитектор Арены»
    sort_order int  NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS catalog_complications (
    id         text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    text       text NOT NULL,
    penalty    int  NOT NULL DEFAULT 1,           -- величина штрафа: баллы (fixed) или процент (percent)
    value_type text NOT NULL DEFAULT 'fixed',     -- fixed | percent
    source     text NOT NULL DEFAULT 'official',  -- official | boosty
    author     text NOT NULL DEFAULT '',
    title      text NOT NULL DEFAULT '',
    sort_order int  NOT NULL DEFAULT 0
);

-- Живое состояние оверлея для OBS (единственная строка id=1).
CREATE TABLE IF NOT EXISTS live_state (
    id         int PRIMARY KEY DEFAULT 1,
    data       jsonb NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT live_state_singleton CHECK (id = 1)
);
INSERT INTO live_state (id, data) VALUES (1, '{}') ON CONFLICT (id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS live_state;
DROP TABLE IF EXISTS catalog_complications;
DROP TABLE IF EXISTS catalog_tasks;
DROP TABLE IF EXISTS registrations;
DROP TABLE IF EXISTS round_entries;
DROP TABLE IF EXISTS rounds;
DROP TABLE IF EXISTS participants;
DROP TABLE IF EXISTS tournaments;
DROP TABLE IF EXISTS users;
