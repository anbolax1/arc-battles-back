package store

import (
	"context"
	"errors"
	"slices"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// dealTaskQueue выдаёт стороне обычные задания по одному: сразу открывается самое дешёвое, остальные ждут
// в очереди от обычных к легендарным, пока не выполнено текущее. Лимит очков набора соблюдается целиком.
func (s *Store) dealTaskQueue(ctx context.Context, roundID, participantID string, plan []poolTask) error {
	slices.SortStableFunc(plan, func(a, b poolTask) int {
		return models.RarityPoints(a.Rarity) - models.RarityPoints(b.Rarity)
	})
	for i, c := range plan {
		if i == 0 {
			pos := 0
			if err := s.insertDealtTask(ctx, roundID, participantID, c.ID, c.Rarity, &pos); err != nil {
				return err
			}
			continue
		}
		if _, err := s.Pool.Exec(ctx, `
			INSERT INTO round_task_queue (round_id, participant_id, position, task_id) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, roundID, participantID, i, c.ID); err != nil {
			return err
		}
	}
	return nil
}

// syncTaskQueue держит у стороны открытым ровно одно невыполненное задание из очереди: выполнили текущее -
// открывается следующее; сняли зачёт у раннего - лишнее открытое возвращается в очередь. Работает только в
// идущем раунде: в сыгранных раундах отметки правят счёт, но новых заданий не открывают.
func (s *Store) syncTaskQueue(ctx context.Context, roundID, participantID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Две быстрые отметки подряд не должны открыть два задания.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, roundID+"/"+participantID); err != nil {
		return err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT status = 'live' FROM rounds WHERE id = $1`, roundID).Scan(&live); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if !live {
		return nil
	}

	rows, err := tx.Query(ctx, `
		SELECT id, task_id, queue_pos FROM round_bonus_tasks
		WHERE round_id = $1 AND participant_id = $2 AND queue_pos IS NOT NULL AND completed_by IS NULL
		ORDER BY queue_pos`, roundID, participantID)
	if err != nil {
		return err
	}
	type open struct {
		id, taskID string
		pos        int
	}
	var opened []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.taskID, &o.pos); err != nil {
			rows.Close()
			return err
		}
		opened = append(opened, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	switch {
	case len(opened) > 1:
		for _, o := range opened[1:] {
			if _, err := tx.Exec(ctx, `
				INSERT INTO round_task_queue (round_id, participant_id, position, task_id) VALUES ($1, $2, $3, $4)
				ON CONFLICT DO NOTHING`, roundID, participantID, o.pos, o.taskID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM round_bonus_tasks WHERE id = $1`, o.id); err != nil {
				return err
			}
		}
	case len(opened) == 0:
		// Очки открытого задания - по редкости на этот момент, как и при раздаче.
		if _, err := tx.Exec(ctx, `
			WITH next AS (
			    DELETE FROM round_task_queue
			    WHERE round_id = $1 AND participant_id = $2
			      AND position = (SELECT min(position) FROM round_task_queue WHERE round_id = $1 AND participant_id = $2)
			    RETURNING task_id, position
			)
			INSERT INTO round_bonus_tasks (round_id, participant_id, task_id, points, queue_pos)
			SELECT $1, $2, n.task_id, array_position(`+raritySQL+`, ct.rarity), n.position
			FROM next n JOIN catalog_tasks ct ON ct.id = n.task_id
			ON CONFLICT (round_id, participant_id, task_id) DO NOTHING`, roundID, participantID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
