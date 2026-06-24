package store

import (
	"context"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// ErrConflict — нарушение бизнес-правила (напр. задание уже назначено в этом турнире).
var ErrConflict = errors.New("конфликт")

// ---- Пул стартовых заданий ----

func scanStarterTask(row pgx.Row) (models.StarterTask, error) {
	var t models.StarterTask
	err := row.Scan(&t.ID, &t.Text, &t.Points, &t.Kind)
	return t, err
}

func (s *Store) ListStarterTasks(ctx context.Context) ([]models.StarterTask, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, text, points, kind FROM starter_tasks ORDER BY sort_order, points, text`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.StarterTask{}
	for rows.Next() {
		t, err := scanStarterTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CreateStarterTask(ctx context.Context, t models.StarterTask) (models.StarterTask, error) {
	const q = `
		INSERT INTO starter_tasks (text, points, kind, sort_order)
		VALUES ($1, $2, $3, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM starter_tasks))
		RETURNING id, text, points, kind`
	return scanStarterTask(s.Pool.QueryRow(ctx, q, t.Text, t.Points, t.Kind))
}

func (s *Store) UpdateStarterTask(ctx context.Context, id string, t models.StarterTask) (models.StarterTask, error) {
	res, err := scanStarterTask(s.Pool.QueryRow(ctx,
		`UPDATE starter_tasks SET text = $2, points = $3, kind = $4 WHERE id = $1 RETURNING id, text, points, kind`,
		id, t.Text, t.Points, t.Kind))
	if errors.Is(err, pgx.ErrNoRows) {
		return res, ErrNotFound
	}
	return res, err
}

func (s *Store) DeleteStarterTask(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM starter_tasks WHERE id = $1`, id)
	return err
}

// ---- Назначения основных заданий по раундам (зачёт раздельный по сторонам) ----

// ListTournamentStarterTasks возвращает назначения основных заданий по раундам турнира,
// каждое — с зачётом по сторонам (Done: participantId → times).
func (s *Store) ListTournamentStarterTasks(ctx context.Context, tournamentID string) ([]models.RoundStarterTask, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT rst.id, rst.round_id, rst.starter_task_id, st.text, st.points
		FROM round_starter_tasks rst
		JOIN starter_tasks st ON st.id = rst.starter_task_id
		JOIN rounds r ON r.id = rst.round_id
		WHERE r.tournament_id = $1
		ORDER BY r.number, st.points, st.text`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.RoundStarterTask{}
	index := map[string]int{}
	for rows.Next() {
		var t models.RoundStarterTask
		if err := rows.Scan(&t.ID, &t.RoundID, &t.StarterTaskID, &t.Text, &t.Points); err != nil {
			return nil, err
		}
		t.Done = []models.RoundTaskDone{}
		index[t.ID] = len(out)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Зачёт по сторонам (times>0) для всех назначений турнира одним запросом.
	dRows, err := s.Pool.Query(ctx, `
		SELECT rstd.round_starter_task_id, rstd.participant_id, rstd.times
		FROM round_starter_task_done rstd
		JOIN round_starter_tasks rst ON rst.id = rstd.round_starter_task_id
		JOIN rounds r ON r.id = rst.round_id
		WHERE r.tournament_id = $1 AND rstd.times > 0`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer dRows.Close()
	for dRows.Next() {
		var assignmentID string
		var d models.RoundTaskDone
		if err := dRows.Scan(&assignmentID, &d.ParticipantID, &d.Times); err != nil {
			return nil, err
		}
		if i, ok := index[assignmentID]; ok {
			out[i].Done = append(out[i].Done, d)
		}
	}
	return out, dRows.Err()
}

func (s *Store) starterAssignmentByKey(ctx context.Context, roundID, starterTaskID string) (models.RoundStarterTask, error) {
	var t models.RoundStarterTask
	err := s.Pool.QueryRow(ctx, `
		SELECT rst.id, rst.round_id, rst.starter_task_id, st.text, st.points
		FROM round_starter_tasks rst
		JOIN starter_tasks st ON st.id = rst.starter_task_id
		WHERE rst.round_id = $1 AND rst.starter_task_id = $2`, roundID, starterTaskID).
		Scan(&t.ID, &t.RoundID, &t.StarterTaskID, &t.Text, &t.Points)
	t.Done = []models.RoundTaskDone{}
	return t, err
}

// AssignTaskToRound назначает основное задание на раунд. ErrConflict — если задание уже
// назначено на другой раунд того же турнира (не повторяются между раундами).
func (s *Store) AssignTaskToRound(ctx context.Context, roundID, starterTaskID string) (models.RoundStarterTask, error) {
	var dup bool
	if err := s.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM round_starter_tasks rst
			JOIN rounds r ON r.id = rst.round_id
			WHERE rst.starter_task_id = $1
			  AND r.tournament_id = (SELECT tournament_id FROM rounds WHERE id = $2)
			  AND rst.round_id <> $2
		)`, starterTaskID, roundID).Scan(&dup); err != nil {
		return models.RoundStarterTask{}, err
	}
	if dup {
		return models.RoundStarterTask{}, ErrConflict
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO round_starter_tasks (round_id, starter_task_id)
		VALUES ($1, $2) ON CONFLICT (round_id, starter_task_id) DO NOTHING`, roundID, starterTaskID); err != nil {
		return models.RoundStarterTask{}, err
	}
	return s.starterAssignmentByKey(ctx, roundID, starterTaskID)
}

// UnassignRoundTask снимает назначение основного задания (каскадом — зачёты по сторонам).
// Возвращает участников, у которых был зачёт, для пересчёта очков.
func (s *Store) UnassignRoundTask(ctx context.Context, assignmentID string) ([]string, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT participant_id FROM round_starter_task_done WHERE round_starter_task_id = $1 AND times > 0`, assignmentID)
	if err != nil {
		return nil, err
	}
	affected := []string{}
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			return nil, err
		}
		affected = append(affected, pid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ct, err := s.Pool.Exec(ctx, `DELETE FROM round_starter_tasks WHERE id = $1`, assignmentID)
	if err != nil {
		return nil, err
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return affected, nil
}

// AdjustRoundTaskCount меняет счётчик зачётов основного задания стороной (participantID) на delta
// (clamp ≥0). Возвращает новый times. Каждая сторона зачитывает независимо.
func (s *Store) AdjustRoundTaskCount(ctx context.Context, assignmentID, participantID string, delta int) (int, error) {
	var times int
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO round_starter_task_done (round_starter_task_id, participant_id, times)
		VALUES ($1, $2, GREATEST($3, 0))
		ON CONFLICT (round_starter_task_id, participant_id) DO UPDATE
			SET times = GREATEST(round_starter_task_done.times + $3, 0)
		RETURNING times`, assignmentID, participantID, delta).Scan(&times)
	if err != nil {
		return 0, err
	}
	if times == 0 {
		_, _ = s.Pool.Exec(ctx,
			`DELETE FROM round_starter_task_done WHERE round_starter_task_id = $1 AND participant_id = $2`,
			assignmentID, participantID)
	}
	return times, nil
}
