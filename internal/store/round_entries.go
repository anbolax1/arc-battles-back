package store

import (
	"context"
	"encoding/json"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const roundEntryCols = `id, round_id, participant_id, points, tasks, bonus, complications, updated_at`

func rawOrEmptyArr(r json.RawMessage) string {
	if len(r) > 0 {
		return string(r)
	}
	return "[]"
}

func scanRoundEntry(row pgx.Row) (models.RoundEntry, error) {
	var e models.RoundEntry
	var tasks, bonus, comps []byte
	if err := row.Scan(&e.ID, &e.RoundID, &e.ParticipantID, &e.Points, &tasks, &bonus, &comps, &e.UpdatedAt); err != nil {
		return e, err
	}
	e.Tasks = json.RawMessage(rawOrEmptyArr(tasks))
	e.Bonus = json.RawMessage(rawOrEmptyArr(bonus))
	e.Complications = json.RawMessage(rawOrEmptyArr(comps))
	return e, nil
}

// UpsertRoundEntry создаёт/обновляет результат участника в раунде по (round_id, participant_id).
func (s *Store) UpsertRoundEntry(ctx context.Context, e models.RoundEntry) (models.RoundEntry, error) {
	const q = `
		INSERT INTO round_entries (round_id, participant_id, points, tasks, bonus, complications)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (round_id, participant_id) DO UPDATE
			SET points = EXCLUDED.points, tasks = EXCLUDED.tasks,
			    bonus = EXCLUDED.bonus, complications = EXCLUDED.complications, updated_at = now()
		RETURNING ` + roundEntryCols
	return scanRoundEntry(s.Pool.QueryRow(ctx, q,
		e.RoundID, e.ParticipantID, e.Points,
		rawOrEmptyArr(e.Tasks), rawOrEmptyArr(e.Bonus), rawOrEmptyArr(e.Complications)))
}

func (s *Store) ListRoundEntries(ctx context.Context, roundID string) ([]models.RoundEntry, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+roundEntryCols+` FROM round_entries WHERE round_id = $1`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.RoundEntry{}
	for rows.Next() {
		e, err := scanRoundEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PointsBreakdown — разложение очков участника по источникам (единый источник формулы
// для пересчёта total_points и для статистики профиля).
type PointsBreakdown struct {
	Base         int // ручные очки за раунды (round_entries.points)
	Starter      int // зачтённые стартовые: times × points
	BonusFixed   int // бонусные fixed
	BonusPercent int // бонусные percent (от earned)
	Penalty      int // штрафы (число ≥0; вычитается)
}

// Earned — база, на которую начисляются percent-бонусы и штрафы.
func (b PointsBreakdown) Earned() int { return b.Base + b.Starter + b.BonusFixed }

// Total — итоговые очки участника: earned + percent-бонусы − штрафы.
func (b PointsBreakdown) Total() int { return b.Earned() + b.BonusPercent - b.Penalty }

// participantBreakdown считает разложение очков участника (без записи в БД):
//
//	earned = SUM(round_entries.points)              -- база (ручные очки за раунды)
//	       + SUM(rst.times × starter_task.points)    -- зачтённые стартовые задания
//	       + SUM(выполненных бонусных, fixed)        -- бонусные fixed
//	bonus%  = SUM(выполненных бонусных, percent → round(pts% × earned))
//	penalty = SUM(rp.times × величина)               -- fixed: penalty; percent: round(penalty% × earned)
//
// Все percent считаются от earned (база + стартовые + fixed-бонусы).
func (s *Store) participantBreakdown(ctx context.Context, participantID string) (PointsBreakdown, error) {
	var b PointsBreakdown
	if err := s.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(points), 0) FROM round_entries WHERE participant_id = $1`,
		participantID).Scan(&b.Base); err != nil {
		return b, err
	}
	if err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(rst.times * st.points), 0)
		FROM round_starter_tasks rst
		JOIN starter_tasks st ON st.id = rst.starter_task_id
		WHERE rst.completed_by = $1`, participantID).Scan(&b.Starter); err != nil {
		return b, err
	}
	if err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(rbt.times * ct.points), 0)
		FROM round_bonus_tasks rbt
		JOIN catalog_tasks ct ON ct.id = rbt.task_id
		WHERE rbt.participant_id = $1 AND ct.value_type = 'fixed'`, participantID).Scan(&b.BonusFixed); err != nil {
		return b, err
	}
	earned := b.Earned()

	// percent-бонусы (зачтённые, times>0)
	bRows, err := s.Pool.Query(ctx, `
		SELECT rbt.times, ct.points FROM round_bonus_tasks rbt
		JOIN catalog_tasks ct ON ct.id = rbt.task_id
		WHERE rbt.participant_id = $1 AND rbt.times > 0 AND ct.value_type = 'percent'`, participantID)
	if err != nil {
		return b, err
	}
	for bRows.Next() {
		var times, pct int
		if err := bRows.Scan(&times, &pct); err != nil {
			bRows.Close()
			return b, err
		}
		b.BonusPercent += times * models.EffectiveValue(pct, models.ValuePercent, earned)
	}
	bRows.Close()
	if err := bRows.Err(); err != nil {
		return b, err
	}

	// штрафы
	pRows, err := s.Pool.Query(ctx, `
		SELECT rp.times, c.penalty, c.value_type
		FROM round_penalties rp
		JOIN catalog_complications c ON c.id = rp.complication_id
		WHERE rp.participant_id = $1 AND rp.times > 0`, participantID)
	if err != nil {
		return b, err
	}
	for pRows.Next() {
		var times, value int
		var valueType string
		if err := pRows.Scan(&times, &value, &valueType); err != nil {
			pRows.Close()
			return b, err
		}
		b.Penalty += times * models.EffectiveValue(value, valueType, earned)
	}
	pRows.Close()
	if err := pRows.Err(); err != nil {
		return b, err
	}
	return b, nil
}

// RecomputeParticipantPoints пересчитывает и сохраняет total_points участника
// (= participantBreakdown.Total). ErrNotFound — если участника нет.
func (s *Store) RecomputeParticipantPoints(ctx context.Context, participantID string) (int, error) {
	b, err := s.participantBreakdown(ctx, participantID)
	if err != nil {
		return 0, err
	}
	total := b.Total()
	ct, err := s.Pool.Exec(ctx, `UPDATE participants SET total_points = $2 WHERE id = $1`, participantID, total)
	if err != nil {
		return 0, err
	}
	if ct.RowsAffected() == 0 {
		return 0, ErrNotFound
	}
	return total, nil
}
