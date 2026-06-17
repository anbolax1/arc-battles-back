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

// ---- Назначения по раундам ----

const rstCols = `rst.id, rst.round_id, rst.starter_task_id, st.text, st.points, rst.completed_by, rst.times`

func scanRoundStarterTask(row pgx.Row) (models.RoundStarterTask, error) {
	var t models.RoundStarterTask
	err := row.Scan(&t.ID, &t.RoundID, &t.StarterTaskID, &t.Text, &t.Points, &t.CompletedBy, &t.Times)
	return t, err
}

// ListTournamentStarterTasks возвращает все назначения стартовых заданий по всем раундам турнира.
func (s *Store) ListTournamentStarterTasks(ctx context.Context, tournamentID string) ([]models.RoundStarterTask, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+rstCols+`
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
	for rows.Next() {
		t, err := scanRoundStarterTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AssignTaskToRound назначает стартовое задание на раунд. Возвращает ErrConflict,
// если это задание уже назначено на другой раунд того же турнира (не повторяются).
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
	return scanRoundStarterTask(s.Pool.QueryRow(ctx, `
		SELECT `+rstCols+`
		FROM round_starter_tasks rst
		JOIN starter_tasks st ON st.id = rst.starter_task_id
		WHERE rst.round_id = $1 AND rst.starter_task_id = $2`, roundID, starterTaskID))
}

// UnassignRoundTask снимает назначение. Возвращает tournamentID и participantID (если был зачёт)
// для последующего пересчёта очков.
func (s *Store) UnassignRoundTask(ctx context.Context, assignmentID string) (prevParticipant *string, err error) {
	err = s.Pool.QueryRow(ctx,
		`DELETE FROM round_starter_tasks WHERE id = $1 RETURNING completed_by`, assignmentID).Scan(&prevParticipant)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return prevParticipant, err
}

// AdjustRoundTaskCount меняет счётчик зачётов задания на delta для участника (clamp ≥0).
// Если times>0 — исполнитель = participantID; если 0 — снимается. Возвращает новый times
// и предыдущего исполнителя (если он другой — его очки тоже надо пересчитать).
func (s *Store) AdjustRoundTaskCount(ctx context.Context, assignmentID, participantID string, delta int) (newTimes int, prevOwner *string, err error) {
	var owner *string
	var times int
	err = s.Pool.QueryRow(ctx, `SELECT completed_by, times FROM round_starter_tasks WHERE id = $1`, assignmentID).Scan(&owner, &times)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	if err != nil {
		return 0, nil, err
	}
	cur := 0
	if owner != nil && *owner == participantID {
		cur = times
	}
	newTimes = cur + delta
	if newTimes < 0 {
		newTimes = 0
	}
	var newOwner *string
	if newTimes > 0 {
		newOwner = &participantID
	}
	if _, err = s.Pool.Exec(ctx,
		`UPDATE round_starter_tasks SET completed_by = $2, times = $3 WHERE id = $1`,
		assignmentID, newOwner, newTimes); err != nil {
		return 0, nil, err
	}
	if owner != nil && *owner != participantID {
		prevOwner = owner // у прежнего исполнителя награда с этого задания обнулилась
	}
	return newTimes, prevOwner, nil
}
