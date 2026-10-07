-- +goose Up
-- Ноки стороны за раунд - отдельным счётчиком для статистики игроков; их очки по-прежнему в points.
ALTER TABLE round_entries ADD COLUMN IF NOT EXISTS knocks int NOT NULL DEFAULT 0;

-- Ручные очки раунда на arcarena - ноки по 3 и задания соперника по 1, а таких заданий не больше двух.
UPDATE round_entries re SET knocks = re.points / 3
FROM rounds r JOIN tournaments t ON t.id = r.tournament_id
WHERE re.round_id = r.id AND t.ext_key LIKE 'arena|%';

-- В пульте нок шёл ручными очками с подписью в журнале: ноки берутся оттуда, а отмена из журнала
-- снимает уже нок, а не просто очки.
UPDATE round_entries re SET knocks = LEAST(j.n, re.points / 3)
FROM (SELECT undo->>'roundId' AS round_id, participant_id, COUNT(*)::int AS n
      FROM match_log WHERE kind = 'points' AND delta = 3 AND text LIKE '%: нок рейдера'
      GROUP BY 1, 2) j
WHERE re.round_id = j.round_id AND re.participant_id = j.participant_id;
UPDATE match_log SET kind = 'knock', undo = undo || '{"type": "knock", "applied": 1}'::jsonb
WHERE kind = 'points' AND delta = 3 AND text LIKE '%: нок рейдера';

-- +goose Down
UPDATE match_log SET kind = 'points', undo = undo || jsonb_build_object('type', 'points', 'applied', delta)
WHERE kind = 'knock';
ALTER TABLE round_entries DROP COLUMN IF EXISTS knocks;
