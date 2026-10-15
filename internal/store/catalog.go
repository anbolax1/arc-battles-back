package store

import (
	"context"
	"fmt"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const taskCols = `id, text, points, value_type, kind, source, author, title, name, category, map_codes, active`

// taskMaps - карты задания из параметра $n без несуществующих и повторов, в порядке карт на сайте.
func taskMaps(n int) string {
	return fmt.Sprintf(`ARRAY(SELECT code FROM maps WHERE code = ANY($%d::text[]) ORDER BY sort_order)`, n)
}

func scanTask(row pgx.Row) (models.CatalogTask, error) {
	var t models.CatalogTask
	err := row.Scan(&t.ID, &t.Text, &t.Points, &t.ValueType, &t.Kind, &t.Source, &t.Author, &t.Title,
		&t.Name, &t.Category, &t.MapCodes, &t.Active)
	return t, err
}

// ListCatalogTasks - весь каталог заданий и протоколов: сначала действующие, затем по разделам.
func (s *Store) ListCatalogTasks(ctx context.Context) ([]models.CatalogTask, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+taskCols+` FROM catalog_tasks
		ORDER BY active DESC, category DESC, kind, map_codes, sort_order, text`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.CatalogTask{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CreateCatalogTask(ctx context.Context, t models.CatalogTask) (models.CatalogTask, error) {
	q := `
		INSERT INTO catalog_tasks (text, points, value_type, kind, source, author, title, name, category, map_codes, active, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, ` + taskMaps(10) + `, $11, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM catalog_tasks))
		RETURNING ` + taskCols
	return scanTask(s.Pool.QueryRow(ctx, q, t.Text, t.Points, t.ValueType, t.Kind, t.Source, t.Author, t.Title,
		t.Name, t.Category, t.MapCodes, t.Active))
}

func (s *Store) UpdateCatalogTask(ctx context.Context, id string, t models.CatalogTask) (models.CatalogTask, error) {
	q := `
		UPDATE catalog_tasks
		SET text = $2, points = $3, value_type = $4, kind = $5, source = $6, author = $7, title = $8,
		    name = $9, category = $10, map_codes = ` + taskMaps(11) + `, active = $12
		WHERE id = $1
		RETURNING ` + taskCols
	return scanTask(s.Pool.QueryRow(ctx, q, id, t.Text, t.Points, t.ValueType, t.Kind, t.Source, t.Author, t.Title,
		t.Name, t.Category, t.MapCodes, t.Active))
}

// DeleteCatalogTask удаляет задание. Если оно уже выдавалось в матчах, удалить нельзя (иначе
// пропадёт история) - тогда задание выключается и больше не раздаётся.
func (s *Store) DeleteCatalogTask(ctx context.Context, id string) error {
	var used bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM round_bonus_tasks WHERE task_id = $1)`, id).Scan(&used); err != nil {
		return err
	}
	if used {
		_, err := s.Pool.Exec(ctx, `UPDATE catalog_tasks SET active = false WHERE id = $1`, id)
		return err
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM catalog_tasks WHERE id = $1`, id)
	return err
}

const complicationCols = `id, text, penalty, value_type, source, author, title`

func scanComplication(row pgx.Row) (models.CatalogComplication, error) {
	var c models.CatalogComplication
	err := row.Scan(&c.ID, &c.Text, &c.Penalty, &c.ValueType, &c.Source, &c.Author, &c.Title)
	return c, err
}

func (s *Store) ListCatalogComplications(ctx context.Context) ([]models.CatalogComplication, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+complicationCols+` FROM catalog_complications ORDER BY sort_order, text`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.CatalogComplication{}
	for rows.Next() {
		c, err := scanComplication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CreateCatalogComplication(ctx context.Context, c models.CatalogComplication) (models.CatalogComplication, error) {
	const q = `
		INSERT INTO catalog_complications (text, penalty, value_type, source, author, title, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM catalog_complications))
		RETURNING ` + complicationCols
	return scanComplication(s.Pool.QueryRow(ctx, q, c.Text, c.Penalty, c.ValueType, c.Source, c.Author, c.Title))
}

func (s *Store) UpdateCatalogComplication(ctx context.Context, id string, c models.CatalogComplication) (models.CatalogComplication, error) {
	const q = `
		UPDATE catalog_complications
		SET text = $2, penalty = $3, value_type = $4, source = $5, author = $6, title = $7
		WHERE id = $1
		RETURNING ` + complicationCols
	return scanComplication(s.Pool.QueryRow(ctx, q, id, c.Text, c.Penalty, c.ValueType, c.Source, c.Author, c.Title))
}

func (s *Store) DeleteCatalogComplication(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM catalog_complications WHERE id = $1`, id)
	return err
}
