package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const presetCols = `id, name, slug, layout, created_at, updated_at`

func scanPreset(row pgx.Row) (models.OverlayPreset, error) {
	var p models.OverlayPreset
	err := row.Scan(&p.ID, &p.Name, &p.Slug, &p.Layout, &p.CreatedAt, &p.UpdatedAt)
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

// GetOverlayPresetByKey — пресет по адресу (slug из ссылки для OBS) или по id.
// Отдаёт pgx.ErrNoRows, если такого нет.
func (s *Store) GetOverlayPresetByKey(ctx context.Context, key string) (models.OverlayPreset, error) {
	const q = `SELECT ` + presetCols + ` FROM overlay_presets WHERE slug = $1 OR id = $1 LIMIT 1`
	return scanPreset(s.Pool.QueryRow(ctx, q, key))
}

// uniqueSlug подбирает свободный адрес: base, base-2, base-3… Пресет excludeID
// не считается занявшим адрес — иначе он не мог бы сохранить свой текущий.
func (s *Store) uniqueSlug(ctx context.Context, base, excludeID string) (string, error) {
	if base == "" {
		base = "overlay"
	}
	cand := base
	for i := 2; i < 200; i++ {
		var taken bool
		const q = `SELECT EXISTS(SELECT 1 FROM overlay_presets WHERE slug = $1 AND id <> $2)`
		if err := s.Pool.QueryRow(ctx, q, cand, excludeID).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return cand, nil
		}
		cand = fmt.Sprintf("%s-%d", base, i)
	}
	return "", errors.New("не удалось подобрать свободный адрес пресета")
}

func (s *Store) CreateOverlayPreset(ctx context.Context, name, slug string, layout []byte) (models.OverlayPreset, error) {
	slug, err := s.uniqueSlug(ctx, slug, "")
	if err != nil {
		return models.OverlayPreset{}, err
	}
	const q = `INSERT INTO overlay_presets (name, slug, layout) VALUES ($1, $2, $3) RETURNING ` + presetCols
	return scanPreset(s.Pool.QueryRow(ctx, q, name, slug, layout))
}

// UpdateOverlayPreset — пустой slug означает «адрес не менять»: перезапись
// раскладки не должна ломать уже вставленную в OBS ссылку.
func (s *Store) UpdateOverlayPreset(ctx context.Context, id, name, slug string, layout []byte) (models.OverlayPreset, error) {
	if slug != "" {
		var err error
		if slug, err = s.uniqueSlug(ctx, slug, id); err != nil {
			return models.OverlayPreset{}, err
		}
	}
	const q = `UPDATE overlay_presets SET name = $2, slug = COALESCE(NULLIF($3, ''), slug), layout = $4, updated_at = now() WHERE id = $1 RETURNING ` + presetCols
	return scanPreset(s.Pool.QueryRow(ctx, q, id, name, slug, layout))
}

func (s *Store) DeleteOverlayPreset(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM overlay_presets WHERE id = $1`, id)
	return err
}
