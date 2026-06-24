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

// Очки за контракт: свой выполненный контракт даёт ContractOwnPoints, выполненный контракт
// противника — ContractCrossPoints. Легендарный контракт — LegendaryPoints (если у записи нет
// собственного points).
const (
	ContractOwnPoints   = 2
	ContractCrossPoints = 1
)

// PointsBreakdown — разложение очков участника по источникам (единый источник формулы для
// пересчёта total_points и для статистики профиля). Новая концепция: протоколы НЕ влияют на
// очки (штраф = минуты в рейде), процентных наград нет.
type PointsBreakdown struct {
	Base      int // ручная корректировка раунда (round_entries.points)
	Main      int // основные задания раунда (per-side): SUM(times × points)
	Contracts int // контракты: 2 × свои выполненные + 1 × чужие выполненные
	Legendary int // легендарные контракты: SUM(points) выполненных участником
}

// Total — итоговые очки участника в турнире (определяют победителя раунда).
func (b PointsBreakdown) Total() int { return b.Base + b.Main + b.Contracts + b.Legendary }

// participantBreakdown считает разложение очков участника (без записи в БД):
//
//	base      = SUM(round_entries.points)                              -- ручная корректировка
//	main      = SUM(rstd.times × starter_task.points)                  -- основные задания (своя сторона)
//	contracts = SUM(2 за свой выполненный + 1 за выполненный контракт противника)
//	legendary = SUM(points выполненных легендарных контрактов)
func (s *Store) participantBreakdown(ctx context.Context, participantID string) (PointsBreakdown, error) {
	var b PointsBreakdown
	if err := s.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(points), 0) FROM round_entries WHERE participant_id = $1`,
		participantID).Scan(&b.Base); err != nil {
		return b, err
	}
	// Основные задания: per-side зачёт через round_starter_task_done.
	if err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(rstd.times * st.points), 0)
		FROM round_starter_task_done rstd
		JOIN round_starter_tasks rst ON rst.id = rstd.round_starter_task_id
		JOIN starter_tasks st ON st.id = rst.starter_task_id
		WHERE rstd.participant_id = $1`, participantID).Scan(&b.Main); err != nil {
		return b, err
	}
	// Контракты: свой выполненный = 2, выполненный контракт противника = 1.
	if err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN participant_id = $1 THEN $2 ELSE $3 END), 0)
		FROM round_bonus_tasks
		WHERE completed_by = $1`, participantID, ContractOwnPoints, ContractCrossPoints).Scan(&b.Contracts); err != nil {
		return b, err
	}
	// Легендарные контракты, выполненные этим участником.
	if err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(lc.points), 0)
		FROM legendary_contract_completions lcc
		JOIN legendary_contracts lc ON lc.id = lcc.legendary_contract_id
		WHERE lcc.participant_id = $1`, participantID).Scan(&b.Legendary); err != nil {
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
