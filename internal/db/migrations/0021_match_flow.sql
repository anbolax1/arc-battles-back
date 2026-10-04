-- +goose Up
-- ============ 3 сезон: карты, пики-баны, два раунда, задания по картам, протоколы за +1, MMR по сезонам ============

-- Справочник карт: код нужен пикам-банам и заданиям на карту, картинка - превью на карточке.
CREATE TABLE IF NOT EXISTS maps (
    code       text PRIMARY KEY,
    name       text NOT NULL,
    image      text NOT NULL DEFAULT '',
    sort_order int  NOT NULL DEFAULT 0,
    active     boolean NOT NULL DEFAULT true
);
INSERT INTO maps (code, name, image, sort_order) VALUES
    ('stormy_flows',  'Бурные потоки',      '/maps/stormy_flows.jpg',  1),
    ('blue_gate',     'Синие ворота',       '/maps/blue_gate.jpg',     2),
    ('buried_city',   'Погребённый город',  '/maps/buried_city.jpg',   3),
    ('spaceport',     'Космопорт',          '/maps/spaceport.jpg',     4),
    ('dam',           'Поле битвы у Дамбы', '/maps/dam.jpg',           5),
    ('stella_montis', 'Стелла Монтис',      '/maps/stella_montis.jpg', 6)
ON CONFLICT (code) DO NOTHING;

-- По коду карты раунда раздаются задания на эту карту; название по-прежнему в rounds.map.
ALTER TABLE rounds ADD COLUMN IF NOT EXISTS map_code text REFERENCES maps(code) ON DELETE SET NULL;

-- Пики-баны матча: порядок ходов, чей ход, карта и раунд, в который она ушла.
CREATE TABLE IF NOT EXISTS match_veto (
    id            text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tournament_id text NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    seq           int  NOT NULL,
    action        text NOT NULL,              -- ban | pick | rest
    side          text NOT NULL DEFAULT '',   -- A | B; у оставшейся карты пусто
    map_code      text NOT NULL REFERENCES maps(code),
    round_number  int,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tournament_id, seq),
    UNIQUE (tournament_id, map_code)
);

-- Задания и протоколы 3 сезона живут в одном каталоге; задание бывает привязано к карте.
ALTER TABLE catalog_tasks ADD COLUMN IF NOT EXISTS category text NOT NULL DEFAULT 'task'; -- task | protocol
ALTER TABLE catalog_tasks ADD COLUMN IF NOT EXISTS map_code text REFERENCES maps(code) ON DELETE SET NULL;
ALTER TABLE catalog_tasks ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '';
ALTER TABLE catalog_tasks ADD COLUMN IF NOT EXISTS active boolean NOT NULL DEFAULT true;

-- Легендарка относится к раунду, в котором её выполнили, - для счёта по раундам.
ALTER TABLE legendary_contract_completions ADD COLUMN IF NOT EXISTS round_id text REFERENCES rounds(id) ON DELETE SET NULL;

-- Журнал матча для ленты в пульте и отмены последнего действия.
CREATE TABLE IF NOT EXISTS match_log (
    id             text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tournament_id  text NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    round_number   int  NOT NULL DEFAULT 0,
    participant_id text REFERENCES participants(id) ON DELETE SET NULL,
    kind           text NOT NULL,             -- task | points | legendary
    text           text NOT NULL DEFAULT '',
    delta          int  NOT NULL DEFAULT 0,
    undo           jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_match_log_tournament ON match_log(tournament_id, created_at);

-- Рейтинг сезона: K-фактор Эло и стартовый MMR, с которого все начинают сезон.
ALTER TABLE seasons ADD COLUMN IF NOT EXISTS k_factor  int NOT NULL DEFAULT 100;
ALTER TABLE seasons ADD COLUMN IF NOT EXISTS start_mmr int NOT NULL DEFAULT 1000;
-- Завершённые сезоны считались с K=32: их таблицы остаются такими, какими были.
UPDATE seasons SET k_factor = 32 WHERE status = 'finished';

-- MMR живёт в рамках сезона; пустой ключ - турнир без сезона.
ALTER TABLE mmr_history      ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT '';
ALTER TABLE team_mmr_history ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_mmr_history_season ON mmr_history(season_key, mode);
CREATE INDEX IF NOT EXISTS idx_team_mmr_history_season ON team_mmr_history(season_key);

-- Флаги разовых действий при старте сервера: здесь - пересчитать MMR по сезонам.
CREATE TABLE IF NOT EXISTS app_flags (
    key        text PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO app_flags (key) VALUES ('recompute_mmr') ON CONFLICT (key) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS app_flags;
DROP INDEX IF EXISTS idx_team_mmr_history_season;
DROP INDEX IF EXISTS idx_mmr_history_season;
ALTER TABLE team_mmr_history DROP COLUMN IF EXISTS season_key;
ALTER TABLE mmr_history DROP COLUMN IF EXISTS season_key;
ALTER TABLE seasons DROP COLUMN IF EXISTS start_mmr;
ALTER TABLE seasons DROP COLUMN IF EXISTS k_factor;
DROP TABLE IF EXISTS match_log;
ALTER TABLE legendary_contract_completions DROP COLUMN IF EXISTS round_id;
ALTER TABLE catalog_tasks DROP COLUMN IF EXISTS active;
ALTER TABLE catalog_tasks DROP COLUMN IF EXISTS name;
ALTER TABLE catalog_tasks DROP COLUMN IF EXISTS map_code;
ALTER TABLE catalog_tasks DROP COLUMN IF EXISTS category;
DROP TABLE IF EXISTS match_veto;
ALTER TABLE rounds DROP COLUMN IF EXISTS map_code;
DROP TABLE IF EXISTS maps;
