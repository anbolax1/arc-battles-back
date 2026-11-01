-- +goose Up
-- Протоколов больше нет: протокол - это задание на время. Переносим их в задания (tag = timed),
-- а какие строки были протоколами, запоминаем для отката. Колонка category остаётся, всегда 'task'.
CREATE TABLE IF NOT EXISTS catalog_task_protocol_backup (task_id text PRIMARY KEY);
INSERT INTO catalog_task_protocol_backup (task_id)
SELECT id FROM catalog_tasks WHERE category = 'protocol'
ON CONFLICT DO NOTHING;
UPDATE catalog_tasks SET category = 'task', tag = 'timed' WHERE category = 'protocol';

-- +goose Down
UPDATE catalog_tasks SET category = 'protocol' WHERE id IN (SELECT task_id FROM catalog_task_protocol_backup);
DROP TABLE IF EXISTS catalog_task_protocol_backup;
