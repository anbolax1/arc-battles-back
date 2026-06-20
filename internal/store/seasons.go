package store

import (
	"context"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const seasonCols = `id, name, status, started_at, ended_at, created_at`

func scanSeason(row pgx.Row) (models.Season, error) {
	var s models.Season
	err := row.Scan(&s.ID, &s.Name, &s.Status, &s.StartedAt, &s.EndedAt, &s.CreatedAt)
	return s, err
}

// ListSeasons — все сезоны, новые сверху.
func (s *Store) ListSeasons(ctx context.Context) ([]models.Season, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+seasonCols+` FROM seasons ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Season{}
	for rows.Next() {
		sn, err := scanSeason(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

// ActiveSeason — текущий активный сезон (или ErrNotFound, если нет).
func (s *Store) ActiveSeason(ctx context.Context) (models.Season, error) {
	sn, err := scanSeason(s.Pool.QueryRow(ctx, `SELECT `+seasonCols+` FROM seasons WHERE status = 'active' LIMIT 1`))
	if err == pgx.ErrNoRows {
		return models.Season{}, ErrNotFound
	}
	return sn, err
}

// UpdateSeason меняет название и даты сезона. ended_at необязательна (nil = не задана) и
// допустима для любого сезона, включая активный. Статус не трогаем. ErrNotFound — нет сезона.
func (s *Store) UpdateSeason(ctx context.Context, id, name string, startedAt time.Time, endedAt *time.Time) (models.Season, error) {
	sn, err := scanSeason(s.Pool.QueryRow(ctx,
		`UPDATE seasons SET name = $2, started_at = $3, ended_at = $4 WHERE id = $1 RETURNING `+seasonCols,
		id, name, startedAt, endedAt))
	if err == pgx.ErrNoRows {
		return models.Season{}, ErrNotFound
	}
	return sn, err
}

// DeleteSeason удаляет сезон. Турниры этого сезона НЕ удаляются — их season_id
// обнуляется автоматически (FK tournaments.season_id ON DELETE SET NULL), т.е. они
// становятся непривязанными и в новый сезон сами не попадут. ErrNotFound — если сезона нет.
func (s *Store) DeleteSeason(ctx context.Context, id string) error {
	ct, err := s.Pool.Exec(ctx, `DELETE FROM seasons WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// StartNewSeason завершает текущий активный сезон (если есть) и создаёт новый активный.
// Атомарно: ровно один active сохраняется (партиальный уникальный индекс это и страхует).
func (s *Store) StartNewSeason(ctx context.Context, name string) (models.Season, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return models.Season{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE seasons SET status = 'finished', ended_at = now() WHERE status = 'active'`); err != nil {
		return models.Season{}, err
	}
	sn, err := scanSeason(tx.QueryRow(ctx, `INSERT INTO seasons (name, status) VALUES ($1, 'active') RETURNING `+seasonCols, name))
	if err != nil {
		return models.Season{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.Season{}, err
	}
	return sn, nil
}
