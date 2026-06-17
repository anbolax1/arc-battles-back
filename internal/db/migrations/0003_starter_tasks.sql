-- +goose Up
-- Пул «стартовых заданий» (НЕ бонусных). Скрыт от обычных пользователей и из правил:
-- эти задания видит только организатор. Он раскидывает их по раундам турнира и
-- отмечает выполненными в эфире с присвоением баллов. В рамках турнира не повторяются.
CREATE TABLE IF NOT EXISTS starter_tasks (
    id         text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    text       text NOT NULL,
    points     int  NOT NULL DEFAULT 0,
    sort_order int  NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Назначение стартового задания на раунд + отметка о выполнении участником.
CREATE TABLE IF NOT EXISTS round_starter_tasks (
    id              text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    round_id        text NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    starter_task_id text NOT NULL REFERENCES starter_tasks(id) ON DELETE CASCADE,
    completed_by    text REFERENCES participants(id) ON DELETE SET NULL,
    points_awarded  int,
    completed_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE(round_id, starter_task_id)
);
CREATE INDEX IF NOT EXISTS idx_rst_round ON round_starter_tasks(round_id);

-- +goose Down
DROP TABLE IF EXISTS round_starter_tasks;
DROP TABLE IF EXISTS starter_tasks;
