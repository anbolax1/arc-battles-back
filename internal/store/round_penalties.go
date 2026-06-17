package store

import (
	"context"

	"github.com/battle-for-respect/backend/internal/models"
)

// ListTournamentPenalties — все применённые усложнения по всем раундам турнира (times>0).
func (s *Store) ListTournamentPenalties(ctx context.Context, tournamentID string) ([]models.RoundPenalty, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT rp.id, rp.round_id, rp.participant_id, rp.complication_id, c.text, c.penalty, c.value_type, rp.times
		FROM round_penalties rp
		JOIN catalog_complications c ON c.id = rp.complication_id
		JOIN rounds r ON r.id = rp.round_id
		WHERE r.tournament_id = $1 AND rp.times > 0
		ORDER BY r.number, c.text`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.RoundPenalty{}
	for rows.Next() {
		var p models.RoundPenalty
		if err := rows.Scan(&p.ID, &p.RoundID, &p.ParticipantID, &p.ComplicationID, &p.Text, &p.Penalty, &p.ValueType, &p.Times); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AdjustRoundPenaltyCount меняет счётчик применений усложнения участнику в раунде на delta
// (clamp ≥0). Строка с нулём удаляется. Возвращает новый times.
func (s *Store) AdjustRoundPenaltyCount(ctx context.Context, roundID, participantID, complicationID string, delta int) (int, error) {
	var times int
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO round_penalties (round_id, participant_id, complication_id, times)
		VALUES ($1, $2, $3, GREATEST($4, 0))
		ON CONFLICT (round_id, participant_id, complication_id) DO UPDATE
			SET times = GREATEST(round_penalties.times + $4, 0)
		RETURNING times`, roundID, participantID, complicationID, delta).Scan(&times)
	if err != nil {
		return 0, err
	}
	if times == 0 {
		_, _ = s.Pool.Exec(ctx,
			`DELETE FROM round_penalties WHERE round_id = $1 AND participant_id = $2 AND complication_id = $3`,
			roundID, participantID, complicationID)
	}
	return times, nil
}
