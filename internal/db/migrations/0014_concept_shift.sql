-- +goose Up
-- ============ Смена концепции 2026-06: контракты / протоколы / 1 раунд / MMR / легендарные контракты ============

-- РОВНО 1 РАУНД на турнир: новые турниры — один раунд (один рейд).
ALTER TABLE tournaments ALTER COLUMN total_rounds SET DEFAULT 1;

-- Тип игроков турнира: pve | pvp | pvpve (определяет пул основных заданий и контрактов).
ALTER TABLE tournaments ADD COLUMN IF NOT EXISTS player_type text NOT NULL DEFAULT 'pvpve';

-- Терминология вида заданий: 'mixed' → 'pvpve' (контракты и основные задания).
UPDATE catalog_tasks SET kind = 'pvpve' WHERE kind = 'mixed';
UPDATE starter_tasks  SET kind = 'pvpve' WHERE kind = 'mixed';
ALTER TABLE catalog_tasks ALTER COLUMN kind SET DEFAULT 'pvpve';
ALTER TABLE starter_tasks  ALTER COLUMN kind SET DEFAULT 'pvpve';

-- КОНТРАКТЫ (бывш. бонусные задания): владелец = participant_id (кому выдан контракт).
-- completed_by — кто фактически выполнил (NULL = не выполнен). Свой выполнил → 2 балла владельцу;
-- противник выполнил → 1 балл противнику. Награда фиксированная (в коде), без percent.
ALTER TABLE round_bonus_tasks ADD COLUMN IF NOT EXISTS completed_by text REFERENCES participants(id) ON DELETE SET NULL;
-- Бэкфилл: ранее зачтённые (times>0) считаем выполненными владельцем.
UPDATE round_bonus_tasks SET completed_by = participant_id WHERE times > 0 AND completed_by IS NULL;

-- ОСНОВНЫЕ ЗАДАНИЯ РАУНДА (бывш. стартовые): раздельный зачёт по сторонам — одинаковы у обеих,
-- каждая выполняет независимо. Назначение остаётся в round_starter_tasks; выполнение — здесь.
CREATE TABLE IF NOT EXISTS round_starter_task_done (
    id                    text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    round_starter_task_id text NOT NULL REFERENCES round_starter_tasks(id) ON DELETE CASCADE,
    participant_id        text NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    times                 int  NOT NULL DEFAULT 0,
    UNIQUE(round_starter_task_id, participant_id)
);
CREATE INDEX IF NOT EXISTS idx_rstd_participant ON round_starter_task_done(participant_id);
-- Бэкфилл из старой однопользовательской модели (completed_by/times на самом назначении).
INSERT INTO round_starter_task_done (round_starter_task_id, participant_id, times)
SELECT id, completed_by, times FROM round_starter_tasks
WHERE completed_by IS NOT NULL AND times > 0
ON CONFLICT (round_starter_task_id, participant_id) DO NOTHING;

-- MMR: рейтинг по исходам. Старт 1000, по режимам (1x1 / 2x2), сквозной по сезонам.
-- Текущее значение материализуется из истории: mmr = 1000 + SUM(history.delta).
CREATE TABLE IF NOT EXISTS user_mmr (
    user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mode       text NOT NULL,            -- 1x1 | 2x2
    mmr        int  NOT NULL DEFAULT 1000,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, mode)
);

CREATE TABLE IF NOT EXISTS mmr_history (
    id            text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    user_id       text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mode          text NOT NULL,
    tournament_id text REFERENCES tournaments(id) ON DELETE CASCADE,
    delta         int  NOT NULL,
    mmr_before    int  NOT NULL,
    mmr_after     int  NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tournament_id, user_id, mode)
);
CREATE INDEX IF NOT EXISTS idx_mmr_history_user ON mmr_history(user_id, mode);

-- ЛЕГЕНДАРНЫЕ КОНТРАКТЫ: глобальный пул, 10 баллов, выполнимы один раз НАВСЕГДА.
CREATE TABLE IF NOT EXISTS legendary_contracts (
    id         text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    text       text NOT NULL,
    points     int  NOT NULL DEFAULT 10,
    kind       text NOT NULL DEFAULT 'pvpve',     -- pve | pvp | pvpve
    source     text NOT NULL DEFAULT 'official',  -- official | boosty
    author     text NOT NULL DEFAULT '',
    title      text NOT NULL DEFAULT '',
    status     text NOT NULL DEFAULT 'available', -- available | done
    sort_order int  NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Журнал выполнения легендарных контрактов (ник / дата / карта / турнир / участник).
-- Уникальный индекс по контракту = «выполнен один раз навсегда».
CREATE TABLE IF NOT EXISTS legendary_contract_completions (
    id                    text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    legendary_contract_id text NOT NULL REFERENCES legendary_contracts(id) ON DELETE CASCADE,
    user_id               text REFERENCES users(id) ON DELETE SET NULL,
    participant_id        text REFERENCES participants(id) ON DELETE SET NULL,
    nickname              text NOT NULL DEFAULT '',
    tournament_id         text REFERENCES tournaments(id) ON DELETE SET NULL,
    map                   text NOT NULL DEFAULT '',
    completed_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_lcc_participant ON legendary_contract_completions(participant_id);
CREATE UNIQUE INDEX IF NOT EXISTS lcc_one_per_contract ON legendary_contract_completions(legendary_contract_id);

-- +goose Down
DROP TABLE IF EXISTS legendary_contract_completions;
DROP TABLE IF EXISTS legendary_contracts;
DROP TABLE IF EXISTS mmr_history;
DROP TABLE IF EXISTS user_mmr;
DROP TABLE IF EXISTS round_starter_task_done;
ALTER TABLE round_bonus_tasks DROP COLUMN IF EXISTS completed_by;
ALTER TABLE tournaments DROP COLUMN IF EXISTS player_type;
ALTER TABLE tournaments ALTER COLUMN total_rounds SET DEFAULT 3;
