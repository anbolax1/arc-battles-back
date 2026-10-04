package store

import (
	"context"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const seasonCols = `id, name, status, started_at, ended_at, created_at, k_factor, start_mmr`

func scanSeason(row pgx.Row) (models.Season, error) {
	var s models.Season
	err := row.Scan(&s.ID, &s.Name, &s.Status, &s.StartedAt, &s.EndedAt, &s.CreatedAt, &s.KFactor, &s.StartMmr)
	return s, err
}

// Значения рейтинга сезона по умолчанию (правила 3 сезона): K=100, старт 1000.
const (
	DefaultSeasonK     = 100
	DefaultSeasonStart = 1000
)

func normSeasonRating(k, start int) (int, int) {
	if k <= 0 {
		k = DefaultSeasonK
	}
	if start <= 0 {
		start = DefaultSeasonStart
	}
	return k, start
}

// GetSeason - сезон по id.
func (s *Store) GetSeason(ctx context.Context, id string) (models.Season, error) {
	sn, err := scanSeason(s.Pool.QueryRow(ctx, `SELECT `+seasonCols+` FROM seasons WHERE id = $1`, id))
	if err == pgx.ErrNoRows {
		return models.Season{}, ErrNotFound
	}
	return sn, err
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

// UpdateSeason меняет название, даты и правила рейтинга сезона, не трогая его статус.
// Дату окончания можно не задавать - у любого сезона, включая активный.
func (s *Store) UpdateSeason(ctx context.Context, id, name string, startedAt time.Time, endedAt *time.Time, k, start int) (models.Season, error) {
	k, start = normSeasonRating(k, start)
	sn, err := scanSeason(s.Pool.QueryRow(ctx,
		`UPDATE seasons SET name = $2, started_at = $3, ended_at = $4, k_factor = $5, start_mmr = $6 WHERE id = $1 RETURNING `+seasonCols,
		id, name, startedAt, endedAt, k, start))
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
func (s *Store) StartNewSeason(ctx context.Context, name string, k, start int) (models.Season, error) {
	k, start = normSeasonRating(k, start)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return models.Season{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE seasons SET status = 'finished', ended_at = now() WHERE status = 'active'`); err != nil {
		return models.Season{}, err
	}
	sn, err := scanSeason(tx.QueryRow(ctx,
		`INSERT INTO seasons (name, status, k_factor, start_mmr) VALUES ($1, 'active', $2, $3) RETURNING `+seasonCols, name, k, start))
	if err != nil {
		return models.Season{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.Season{}, err
	}
	return sn, nil
}
