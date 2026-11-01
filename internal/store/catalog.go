package store

import (
	"context"
	"fmt"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const taskCols = `id, text, points, value_type, kind, source, author, title, name, tag, map_codes, active, format, rarity, round_type`

// taskMaps - карты задания из параметра $n без несуществующих и повторов, в порядке карт на сайте.
func taskMaps(n int) string {
	return fmt.Sprintf(`ARRAY(SELECT code FROM maps WHERE code = ANY($%d::text[]) ORDER BY sort_order)`, n)
}

func scanTask(row pgx.Row) (models.CatalogTask, error) {
	var t models.CatalogTask
	err := row.Scan(&t.ID, &t.Text, &t.Points, &t.ValueType, &t.Kind, &t.Source, &t.Author, &t.Title,
		&t.Name, &t.Tag, &t.MapCodes, &t.Active, &t.Format, &t.Rarity, &t.RoundType)
	return t, err
}

// ListCatalogTasks - весь каталог заданий: сначала действующие, затем обычные и на время.
func (s *Store) ListCatalogTasks(ctx context.Context) ([]models.CatalogTask, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+taskCols+` FROM catalog_tasks
		ORDER BY active DESC, tag, kind, map_codes, sort_order, text`)
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

var insertTaskSQL = `
	INSERT INTO catalog_tasks (text, points, value_type, kind, source, author, title, name, tag, map_codes, active,
	                           format, rarity, round_type, sort_order)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, ` + taskMaps(10) + `, $11,
	        $12, $13, $14, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM catalog_tasks))
	RETURNING ` + taskCols

// taskDuplicateSQL - в каталоге уже есть такое же задание: то же название и описание, пул и формат.
const taskDuplicateSQL = `
	SELECT EXISTS(SELECT 1 FROM catalog_tasks
	WHERE lower(btrim(name)) = lower(btrim($1)) AND lower(btrim(text)) = lower(btrim($2))
	  AND kind = $3 AND format = $4)`

// withTaskDefaults заменяет пустую классификацию умолчаниями: так задание из любого места кода
// попадает в базу корректным, а не падает на ограничениях таблицы.
func withTaskDefaults(t models.CatalogTask) models.CatalogTask {
	if t.Format == "" {
		t.Format = models.TaskFormatAny
	}
	if t.Rarity == "" {
		t.Rarity = models.RarityCommon
	}
	if t.RoundType == "" {
		t.RoundType = models.RoundTypeAny
	}
	if t.Tag == "" {
		t.Tag = models.TaskTagNormal
	}
	return t
}

// CatalogTaskResult - итог добавления одного задания из пачки: созданное задание или причина отказа.
type CatalogTaskResult struct {
	Task      *models.CatalogTask
	Duplicate bool // такое задание уже есть в каталоге или выше в этой же пачке
}

// CreateCatalogTasks добавляет задания одной транзакцией: единственный путь создания, им пользуются
// и одиночное добавление, и вставка списком. Дубли пропускаются и помечаются в результате.
func (s *Store) CreateCatalogTasks(ctx context.Context, tasks []models.CatalogTask) ([]CatalogTaskResult, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	out := make([]CatalogTaskResult, 0, len(tasks))
	for _, t := range tasks {
		t = withTaskDefaults(t)
		var dup bool
		if err := tx.QueryRow(ctx, taskDuplicateSQL, t.Name, t.Text, t.Kind, t.Format).Scan(&dup); err != nil {
			return nil, err
		}
		if dup {
			out = append(out, CatalogTaskResult{Duplicate: true})
			continue
		}
		created, err := scanTask(tx.QueryRow(ctx, insertTaskSQL, t.Text, t.Points, t.ValueType, t.Kind, t.Source, t.Author,
			t.Title, t.Name, t.Tag, t.MapCodes, t.Active, t.Format, t.Rarity, t.RoundType))
		if err != nil {
			return nil, err
		}
		out = append(out, CatalogTaskResult{Task: &created})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) UpdateCatalogTask(ctx context.Context, id string, t models.CatalogTask) (models.CatalogTask, error) {
	t = withTaskDefaults(t)
	q := `
		UPDATE catalog_tasks
		SET text = $2, points = $3, value_type = $4, kind = $5, source = $6, author = $7, title = $8,
		    name = $9, tag = $10, map_codes = ` + taskMaps(11) + `, active = $12,
		    format = $13, rarity = $14, round_type = $15
		WHERE id = $1
		RETURNING ` + taskCols
	return scanTask(s.Pool.QueryRow(ctx, q, id, t.Text, t.Points, t.ValueType, t.Kind, t.Source, t.Author, t.Title,
		t.Name, t.Tag, t.MapCodes, t.Active, t.Format, t.Rarity, t.RoundType))
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
