package store

import (
	"context"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// Протоколы (бывш. усложнения). На игрока действует РОВНО ОДИН протокол за турнир, без повторов
// между сторонами. Штраф = минуты в рейде за нарушение (times = число нарушений = минут); на ОЧКИ
// НЕ влияет. Хранится в round_penalties (round_id, participant_id, complication_id, times).

// ListTournamentPenalties — протоколы сторон по раундам турнира (с числом нарушений times).
func (s *Store) ListTournamentPenalties(ctx context.Context, tournamentID string) ([]models.RoundPenalty, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT rp.id, rp.round_id, rp.participant_id, rp.complication_id, c.text, c.penalty, c.value_type, rp.times
		FROM round_penalties rp
		JOIN catalog_complications c ON c.id = rp.complication_id
		JOIN rounds r ON r.id = rp.round_id
		WHERE r.tournament_id = $1
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

// SetParticipantProtocol назначает участнику РОВНО ОДИН протокол в раунде (заменяет прежний,
// сбрасывая счётчик нарушений). complicationID="" — снять протокол. ErrConflict — если этот
// протокол уже действует на другую сторону (без повторов в рамках раунда).
func (s *Store) SetParticipantProtocol(ctx context.Context, roundID, participantID, complicationID string) error {
	if complicationID == "" {
		_, err := s.Pool.Exec(ctx,
			`DELETE FROM round_penalties WHERE round_id = $1 AND participant_id = $2`, roundID, participantID)
		return err
	}
	var taken bool
	if err := s.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM round_penalties
			WHERE round_id = $1 AND complication_id = $2 AND participant_id <> $3)`,
		roundID, complicationID, participantID).Scan(&taken); err != nil {
		return err
	}
	if taken {
		return ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`DELETE FROM round_penalties WHERE round_id = $1 AND participant_id = $2`, roundID, participantID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO round_penalties (round_id, participant_id, complication_id, times) VALUES ($1, $2, $3, 0)`,
		roundID, participantID, complicationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AdjustProtocolViolations меняет число нарушений (= минут штрафа) протокола стороны на delta
// (clamp ≥0). ErrNotFound — если у стороны протокол не назначен. Возвращает новое число нарушений.
func (s *Store) AdjustProtocolViolations(ctx context.Context, roundID, participantID string, delta int) (int, error) {
	var times int
	err := s.Pool.QueryRow(ctx, `
		UPDATE round_penalties SET times = GREATEST(times + $3, 0)
		WHERE round_id = $1 AND participant_id = $2
		RETURNING times`, roundID, participantID, delta).Scan(&times)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return times, err
}
