package store

import (
	"context"

	"github.com/battle-for-respect/backend/internal/models"
)

// Легендарные контракты больше не выдаются: в базе остались выполненные, они входят в счёт сыгранных матчей.

// UncompleteLegendary снимает выполнение (возвращает контракт в пул доступных). Возвращает
// participantID прежнего исполнителя (если был) для пересчёта очков.
func (s *Store) UncompleteLegendary(ctx context.Context, id string) (*string, error) {
	var pid *string
	_ = s.Pool.QueryRow(ctx,
		`SELECT participant_id FROM legendary_contract_completions WHERE legendary_contract_id = $1`, id).Scan(&pid)
	if _, err := s.Pool.Exec(ctx,
		`DELETE FROM legendary_contract_completions WHERE legendary_contract_id = $1`, id); err != nil {
		return nil, err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE legendary_contracts SET status = 'available' WHERE id = $1`, id); err != nil {
		return nil, err
	}
	return pid, nil
}

// ListMatchLegendary - легендарки, выполненные в матче, с раундом и наградой (для счёта по раундам).
func (s *Store) ListMatchLegendary(ctx context.Context, tournamentID string) ([]models.LegendaryCompletion, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT lcc.id, lcc.legendary_contract_id, lcc.user_id, lcc.participant_id, lcc.nickname, lcc.tournament_id,
		       lcc.map, lcc.completed_at, lcc.round_id, COALESCE(r.number, 1), lc.text, lc.points
		FROM legendary_contract_completions lcc
		JOIN legendary_contracts lc ON lc.id = lcc.legendary_contract_id
		LEFT JOIN rounds r ON r.id = lcc.round_id
		WHERE lcc.tournament_id = $1
		ORDER BY lcc.completed_at`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.LegendaryCompletion{}
	for rows.Next() {
		var c models.LegendaryCompletion
		if err := rows.Scan(&c.ID, &c.LegendaryContractID, &c.UserID, &c.ParticipantID, &c.Nickname, &c.TournamentID,
			&c.Map, &c.CompletedAt, &c.RoundID, &c.RoundNumber, &c.LegendaryText, &c.Points); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
