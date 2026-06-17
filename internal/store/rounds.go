package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

func scanRound(row pgx.Row) (models.Round, error) {
	var r models.Round
	err := row.Scan(&r.ID, &r.TournamentID, &r.Number, &r.Map, &r.Status)
	return r, err
}

func (s *Store) ListRounds(ctx context.Context, tournamentID string) ([]models.Round, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, tournament_id, number, map, status FROM rounds WHERE tournament_id = $1 ORDER BY number`,
		tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.Round{}
	for rows.Next() {
		r, err := scanRound(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CreateRound(ctx context.Context, r models.Round) (models.Round, error) {
	const q = `
		INSERT INTO rounds (tournament_id, number, map, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tournament_id, number) DO UPDATE
			SET map = EXCLUDED.map, status = EXCLUDED.status
		RETURNING id, tournament_id, number, map, status`
	if r.Status == "" {
		r.Status = "pending"
	}
	return scanRound(s.Pool.QueryRow(ctx, q, r.TournamentID, r.Number, r.Map, r.Status))
}

// UpdateRound частично обновляет раунд (статус/карта) по id. ErrNotFound — если нет.
func (s *Store) UpdateRound(ctx context.Context, id string, status, mapName *string) (models.Round, error) {
	const cols = `id, tournament_id, number, map, status`
	sets := []string{}
	args := []any{}
	n := 1
	if status != nil {
		sets = append(sets, fmt.Sprintf("status = $%d", n))
		args = append(args, *status)
		n++
	}
	if mapName != nil {
		sets = append(sets, fmt.Sprintf("map = $%d", n))
		args = append(args, *mapName)
		n++
	}
	if len(sets) == 0 {
		r, err := scanRound(s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM rounds WHERE id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return r, ErrNotFound
		}
		return r, err
	}
	args = append(args, id)
	q := `UPDATE rounds SET ` + strings.Join(sets, ", ") + fmt.Sprintf(` WHERE id = $%d RETURNING `, n) + cols
	r, err := scanRound(s.Pool.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}
