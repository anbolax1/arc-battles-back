package store

import (
	"context"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const presetCols = `id, name, layout, created_at, updated_at`

func scanPreset(row pgx.Row) (models.OverlayPreset, error) {
	var p models.OverlayPreset
	err := row.Scan(&p.ID, &p.Name, &p.Layout, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ListOverlayPresets — все общие пресеты (новые сверху по дате создания).
func (s *Store) ListOverlayPresets(ctx context.Context) ([]models.OverlayPreset, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+presetCols+` FROM overlay_presets ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.OverlayPreset{}
	for rows.Next() {
		p, err := scanPreset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreateOverlayPreset(ctx context.Context, name string, layout []byte) (models.OverlayPreset, error) {
	const q = `INSERT INTO overlay_presets (name, layout) VALUES ($1, $2) RETURNING ` + presetCols
	return scanPreset(s.Pool.QueryRow(ctx, q, name, layout))
}

func (s *Store) UpdateOverlayPreset(ctx context.Context, id, name string, layout []byte) (models.OverlayPreset, error) {
	const q = `UPDATE overlay_presets SET name = $2, layout = $3, updated_at = now() WHERE id = $1 RETURNING ` + presetCols
	return scanPreset(s.Pool.QueryRow(ctx, q, id, name, layout))
}

func (s *Store) DeleteOverlayPreset(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM overlay_presets WHERE id = $1`, id)
	return err
}
