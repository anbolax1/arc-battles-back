package store

import (
	"context"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const rbtCols = `rbt.id, rbt.round_id, r.number, rbt.participant_id, rbt.task_id, ct.text, ct.points, ct.value_type, rbt.times`

func scanRoundBonusTask(row pgx.Row) (models.RoundBonusTask, error) {
	var t models.RoundBonusTask
	err := row.Scan(&t.ID, &t.RoundID, &t.RoundNumber, &t.ParticipantID, &t.TaskID, &t.Text, &t.Points, &t.ValueType, &t.Times)
	return t, err
}

// ListTournamentBonusTasks — все бонусные задания участников по раундам турнира.
func (s *Store) ListTournamentBonusTasks(ctx context.Context, tournamentID string) ([]models.RoundBonusTask, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+rbtCols+`
		FROM round_bonus_tasks rbt
		JOIN catalog_tasks ct ON ct.id = rbt.task_id
		JOIN rounds r ON r.id = rbt.round_id
		WHERE r.tournament_id = $1
		ORDER BY r.number, ct.text`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.RoundBonusTask{}
	for rows.Next() {
		t, err := scanRoundBonusTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AssignBonusTask добавляет участнику бонусное задание на раунд. ErrConflict — если это
// задание у участника в турнире уже есть (бонусные не повторяются).
func (s *Store) AssignBonusTask(ctx context.Context, roundID, participantID, taskID string) (models.RoundBonusTask, error) {
	var dup bool
	if err := s.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM round_bonus_tasks rbt
			JOIN rounds r ON r.id = rbt.round_id
			WHERE rbt.participant_id = $1 AND rbt.task_id = $2
			  AND r.tournament_id = (SELECT tournament_id FROM rounds WHERE id = $3)
		)`, participantID, taskID, roundID).Scan(&dup); err != nil {
		return models.RoundBonusTask{}, err
	}
	if dup {
		return models.RoundBonusTask{}, ErrConflict
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO round_bonus_tasks (round_id, participant_id, task_id)
		VALUES ($1, $2, $3) ON CONFLICT (round_id, participant_id, task_id) DO NOTHING`,
		roundID, participantID, taskID); err != nil {
		return models.RoundBonusTask{}, err
	}
	return scanRoundBonusTask(s.Pool.QueryRow(ctx, `
		SELECT `+rbtCols+`
		FROM round_bonus_tasks rbt
		JOIN catalog_tasks ct ON ct.id = rbt.task_id
		JOIN rounds r ON r.id = rbt.round_id
		WHERE rbt.round_id = $1 AND rbt.participant_id = $2 AND rbt.task_id = $3`, roundID, participantID, taskID))
}

// AdjustBonusTaskCount меняет счётчик зачётов бонусного задания на delta (clamp ≥0).
// times=0 → не выполнено (переносится). Возвращает participantID и новый times.
func (s *Store) AdjustBonusTaskCount(ctx context.Context, id string, delta int) (participantID string, newTimes int, err error) {
	err = s.Pool.QueryRow(ctx,
		`UPDATE round_bonus_tasks SET times = GREATEST(times + $2, 0) WHERE id = $1 RETURNING participant_id, times`,
		id, delta).Scan(&participantID, &newTimes)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	return participantID, newTimes, err
}

// RemoveBonusTask снимает бонусное задание. Возвращает participantID для пересчёта.
func (s *Store) RemoveBonusTask(ctx context.Context, id string) (participantID string, err error) {
	err = s.Pool.QueryRow(ctx,
		`DELETE FROM round_bonus_tasks WHERE id = $1 RETURNING participant_id`, id).Scan(&participantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return participantID, err
}
