package store

import (
	"context"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// Контракты (бывш. бонусные задания). Владелец контракта = participant_id (кому выдан).
// completed_by — кто фактически выполнил: владелец → +2 балла, противник → +1 балл, NULL → не выполнен.
const rbtCols = `rbt.id, rbt.round_id, r.number, rbt.participant_id, rbt.task_id, ct.text, ct.points, ct.value_type, ct.kind, rbt.times, rbt.completed_by`

func scanRoundBonusTask(row pgx.Row) (models.RoundBonusTask, error) {
	var t models.RoundBonusTask
	err := row.Scan(&t.ID, &t.RoundID, &t.RoundNumber, &t.ParticipantID, &t.TaskID,
		&t.Text, &t.Points, &t.ValueType, &t.Kind, &t.Times, &t.CompletedBy)
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
		ORDER BY r.number, rbt.participant_id, ct.text`, tournamentID)
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
// ErrConflict — если этот контракт у участника в турнире уже есть (не повторяются).
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
	return s.bonusByKey(ctx, roundID, participantID, taskID)
}

// DealContracts выдаёт участнику до count случайных контрактов из пула, совместимых с типом
// игроков турнира (pvpve — любые; pve/pvp — свои + универсальные pvpve), исключая уже выданные.
func (s *Store) DealContracts(ctx context.Context, roundID, participantID string, count int) ([]models.RoundBonusTask, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT ct.id
		FROM catalog_tasks ct
		JOIN rounds r ON r.id = $1
		JOIN tournaments t ON t.id = r.tournament_id
		WHERE (t.player_type = 'pvpve' OR ct.kind = t.player_type OR ct.kind = 'pvpve')
		  AND ct.id NOT IN (
		      SELECT rbt.task_id FROM round_bonus_tasks rbt
		      JOIN rounds r2 ON r2.id = rbt.round_id
		      WHERE rbt.participant_id = $2 AND r2.tournament_id = t.id
		  )
		ORDER BY random()
		LIMIT $3`, roundID, participantID, count)
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

// MarkContract отмечает исполнителя контракта: by = owner (владелец, +2) | opponent (противник, +1)
// | none (снять отметку). Возвращает участников, чьи очки надо пересчитать (прежний + новый исполнитель).
func (s *Store) MarkContract(ctx context.Context, id, by string) ([]string, error) {
	var roundID, owner string
	var prev *string
	err := s.Pool.QueryRow(ctx,
		`SELECT round_id, participant_id, completed_by FROM round_bonus_tasks WHERE id = $1`, id).
		Scan(&roundID, &owner, &prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var target *string
	switch by {
	case "owner":
		target = &owner
	case "opponent":
		opp, err := s.OpponentParticipant(ctx, roundID, owner)
		if err != nil {
			return nil, err
		}
		target = &opp
	case "none", "":
		target = nil
	default:
		return nil, ErrConflict
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE round_bonus_tasks SET completed_by = $2 WHERE id = $1`, id, target); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	if prev != nil {
		set[*prev] = true
	}
	if target != nil {
		set[*target] = true
	}
	affected := make([]string, 0, len(set))
	for k := range set {
		affected = append(affected, k)
	}
	return affected, nil
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
