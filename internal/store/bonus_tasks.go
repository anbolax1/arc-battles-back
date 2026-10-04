package store

import (
	"context"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// Контракты (бывш. бонусные задания). Владелец контракта = participant_id (кому выдан).
// completed_by — кто фактически выполнил: владелец → +2 балла, противник → +1 балл, NULL → не выполнен.
const rbtCols = `rbt.id, rbt.round_id, r.number, rbt.participant_id, rbt.task_id, ct.text, ct.points, ct.value_type, ct.kind, rbt.times, rbt.completed_by,
	ct.name, ct.category, COALESCE(ct.map_code, '')`

func scanRoundBonusTask(row pgx.Row) (models.RoundBonusTask, error) {
	var t models.RoundBonusTask
	err := row.Scan(&t.ID, &t.RoundID, &t.RoundNumber, &t.ParticipantID, &t.TaskID,
		&t.Text, &t.Points, &t.ValueType, &t.Kind, &t.Times, &t.CompletedBy,
		&t.Name, &t.Category, &t.MapCode)
	return t, err
}

// ListTournamentBonusTasks — все контракты участников по раундам турнира.
func (s *Store) ListTournamentBonusTasks(ctx context.Context, tournamentID string) ([]models.RoundBonusTask, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+rbtCols+`
		FROM round_bonus_tasks rbt
		JOIN catalog_tasks ct ON ct.id = rbt.task_id
		JOIN rounds r ON r.id = rbt.round_id
		WHERE r.tournament_id = $1
		ORDER BY r.number, rbt.participant_id, ct.category, ct.map_code NULLS FIRST, ct.text`, tournamentID)
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

func (s *Store) bonusByKey(ctx context.Context, roundID, participantID, taskID string) (models.RoundBonusTask, error) {
	return scanRoundBonusTask(s.Pool.QueryRow(ctx, `
		SELECT `+rbtCols+`
		FROM round_bonus_tasks rbt
		JOIN catalog_tasks ct ON ct.id = rbt.task_id
		JOIN rounds r ON r.id = rbt.round_id
		WHERE rbt.round_id = $1 AND rbt.participant_id = $2 AND rbt.task_id = $3`, roundID, participantID, taskID))
}

// AssignBonusTask выдаёт участнику контракт на раунд (ручное добавление организатором).
// ErrConflict — если этот контракт уже разыгран в ЭТОМ турнире (любой стороной): контракты
// в рамках турнира не повторяются.
func (s *Store) AssignBonusTask(ctx context.Context, roundID, participantID, taskID string) (models.RoundBonusTask, error) {
	var dup bool
	if err := s.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM round_bonus_tasks rbt
			JOIN rounds r ON r.id = rbt.round_id
			WHERE rbt.task_id = $1
			  AND r.tournament_id = (SELECT tournament_id FROM rounds WHERE id = $2)
		)`, taskID, roundID).Scan(&dup); err != nil {
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
	return s.bonusByKey(ctx, roundID, participantID, taskID)
}

// DealContracts выдаёт участнику до count случайных контрактов из пула, совместимых с типом
// игроков турнира (pvpve — любые; pve/pvp — свои + универсальные pvpve), исключая уже разыгранные
// в ЭТОМ турнире ЛЮБОЙ стороной (контракты в рамках турнира не повторяются).
func (s *Store) DealContracts(ctx context.Context, roundID, participantID string, count int) ([]models.RoundBonusTask, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT ct.id
		FROM catalog_tasks ct
		JOIN rounds r ON r.id = $1
		JOIN tournaments t ON t.id = r.tournament_id
		WHERE ct.active AND ct.category = 'task'
		  AND (t.player_type = 'pvpve' OR ct.kind = t.player_type OR ct.kind = 'pvpve')
		  AND ct.id NOT IN (
		      SELECT rbt.task_id FROM round_bonus_tasks rbt
		      JOIN rounds r2 ON r2.id = rbt.round_id
		      WHERE r2.tournament_id = t.id
		  )
		ORDER BY random()
		LIMIT $2`, roundID, count)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := []models.RoundBonusTask{}
	for _, id := range ids {
		item, err := s.AssignBonusTask(ctx, roundID, participantID, id)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, nil
}

// OpponentParticipant — другая сторона в раунде (для зачёта «выполнил контракт противника»).
func (s *Store) OpponentParticipant(ctx context.Context, roundID, participantID string) (string, error) {
	var oppID string
	err := s.Pool.QueryRow(ctx, `
		SELECT p.id FROM participants p
		JOIN rounds r ON r.tournament_id = p.tournament_id
		WHERE r.id = $1 AND p.id <> $2
		ORDER BY p.seed LIMIT 1`, roundID, participantID).Scan(&oppID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return oppID, err
}

// MarkResult - итог отметки задания: прежний и новый исполнитель и чьи очки пересчитать.
type MarkResult struct {
	RoundID  string
	Owner    string
	Prev     *string
	Target   *string
	Affected []string
}

// MarkContract отмечает исполнителя контракта: by = owner (владелец) | opponent (противник, +1)
// | none (снять отметку). Возвращает участников, чьи очки надо пересчитать (прежний + новый исполнитель).
func (s *Store) MarkContract(ctx context.Context, id, by string) ([]string, error) {
	res, err := s.MarkTask(ctx, id, by)
	return res.Affected, err
}

// MarkTask - отметка исполнителя задания. Протокол засчитывается только владельцу.
func (s *Store) MarkTask(ctx context.Context, id, by string) (MarkResult, error) {
	var res MarkResult
	var category string
	err := s.Pool.QueryRow(ctx, `
		SELECT rbt.round_id, rbt.participant_id, rbt.completed_by, ct.category
		FROM round_bonus_tasks rbt JOIN catalog_tasks ct ON ct.id = rbt.task_id
		WHERE rbt.id = $1`, id).Scan(&res.RoundID, &res.Owner, &res.Prev, &category)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, ErrNotFound
	}
	if err != nil {
		return res, err
	}

	switch by {
	case "owner":
		// Зачёт владельцем имеет приоритет: ставится всегда и отменяет балл противника (перетирает completed_by).
		res.Target = &res.Owner
	case "opponent":
		// Протокол - личное задание, противнику его не засчитать. Задание засчитывается противнику,
		// ТОЛЬКО если владелец сам его не выполнил (иначе красть нечего).
		if category == "protocol" || (res.Prev != nil && *res.Prev == res.Owner) {
			return res, ErrConflict
		}
		opp, err := s.OpponentParticipant(ctx, res.RoundID, res.Owner)
		if err != nil {
			return res, err
		}
		res.Target = &opp
	case "none", "":
		res.Target = nil
	default:
		return res, ErrConflict
	}
	if err := s.SetTaskCompletedBy(ctx, id, res.Target); err != nil {
		return res, err
	}
	set := map[string]bool{}
	if res.Prev != nil {
		set[*res.Prev] = true
	}
	if res.Target != nil {
		set[*res.Target] = true
	}
	for k := range set {
		res.Affected = append(res.Affected, k)
	}
	return res, nil
}

// SetTaskCompletedBy ставит исполнителя задания напрямую (для отмены из журнала).
func (s *Store) SetTaskCompletedBy(ctx context.Context, id string, by *string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE round_bonus_tasks SET completed_by = $2 WHERE id = $1`, id, by)
	return err
}

// RemoveBonusTask снимает контракт. Возвращает участников для пересчёта (владелец + исполнитель).
func (s *Store) RemoveBonusTask(ctx context.Context, id string) ([]string, error) {
	var owner string
	var completedBy *string
	err := s.Pool.QueryRow(ctx,
		`DELETE FROM round_bonus_tasks WHERE id = $1 RETURNING participant_id, completed_by`, id).
		Scan(&owner, &completedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	set := map[string]bool{owner: true}
	if completedBy != nil {
		set[*completedBy] = true
	}
	affected := make([]string, 0, len(set))
	for k := range set {
		affected = append(affected, k)
	}
	return affected, nil
}

// GetBonusAssignment - выданное задание и матч, к которому оно относится.
func (s *Store) GetBonusAssignment(ctx context.Context, id string) (models.RoundBonusTask, string, error) {
	var tournamentID string
	row := s.Pool.QueryRow(ctx, `
		SELECT `+rbtCols+`, r.tournament_id
		FROM round_bonus_tasks rbt
		JOIN catalog_tasks ct ON ct.id = rbt.task_id
		JOIN rounds r ON r.id = rbt.round_id
		WHERE rbt.id = $1`, id)
	var t models.RoundBonusTask
	err := row.Scan(&t.ID, &t.RoundID, &t.RoundNumber, &t.ParticipantID, &t.TaskID,
		&t.Text, &t.Points, &t.ValueType, &t.Kind, &t.Times, &t.CompletedBy,
		&t.Name, &t.Category, &t.MapCode, &tournamentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, "", ErrNotFound
	}
	return t, tournamentID, err
}
