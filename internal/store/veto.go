package store

import (
	"context"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// ErrMatchStarted - карты уже не поменять: первый раунд начался.
var ErrMatchStarted = errors.New("матч уже начался")

// ListMaps - действующие карты справочника по порядку.
func (s *Store) ListMaps(ctx context.Context) ([]models.MapInfo, error) {
	rows, err := s.Pool.Query(ctx, `SELECT code, name, image, sort_order FROM maps WHERE active ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.MapInfo{}
	for rows.Next() {
		var m models.MapInfo
		if err := rows.Scan(&m.Code, &m.Name, &m.Image, &m.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Форматы матча: обычный матч 3 сезона и шоу-матч, объявленный заранее.
const (
	FormatMatch = "match"
	FormatShow  = "show"
)

// vetoOrders - порядок пиков-банов по формату. Матч: бан A, бан B, пик A (1-й раунд), бан B, бан A,
// оставшаяся карта - 2-й раунд. Шоу-матч: пик A, пик B, бан A, бан B, пик A - три раунда.
var vetoOrders = map[string][]models.VetoStep{
	FormatMatch: {
		{Action: "ban", Side: "A"},
		{Action: "ban", Side: "B"},
		{Action: "pick", Side: "A", Round: 1},
		{Action: "ban", Side: "B"},
		{Action: "ban", Side: "A"},
		{Action: "rest", Round: 2},
	},
	FormatShow: {
		{Action: "pick", Side: "A", Round: 1},
		{Action: "pick", Side: "B", Round: 2},
		{Action: "ban", Side: "A"},
		{Action: "ban", Side: "B"},
		{Action: "pick", Side: "A", Round: 3},
	},
}

// VetoOrder - порядок пиков-банов для формата матча. A - сторона с меньшим MMR или новичок сезона.
func VetoOrder(format string) []models.VetoStep {
	if order, ok := vetoOrders[format]; ok {
		return order
	}
	return vetoOrders[FormatMatch]
}

// FormatRounds - сколько раундов в матче этого формата.
func FormatRounds(format string) int {
	n := 0
	for _, st := range VetoOrder(format) {
		if st.Round > n {
			n = st.Round
		}
	}
	return n
}

// ListVeto - ходы пиков-банов матча по порядку.
func (s *Store) ListVeto(ctx context.Context, tournamentID string) ([]models.VetoAction, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT v.seq, v.action, v.side, v.map_code, m.name, v.round_number
		FROM match_veto v JOIN maps m ON m.code = v.map_code
		WHERE v.tournament_id = $1 ORDER BY v.seq`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.VetoAction{}
	for rows.Next() {
		var a models.VetoAction
		if err := rows.Scan(&a.Seq, &a.Action, &a.Side, &a.MapCode, &a.MapName, &a.RoundNumber); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// roundsStarted - начался ли хоть один раунд (после этого карты не меняются).
func roundsStarted(ctx context.Context, tx pgx.Tx, tournamentID string) (bool, error) {
	var started bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM rounds WHERE tournament_id = $1 AND status <> 'pending')`, tournamentID).Scan(&started)
	return started, err
}

func setRoundMap(ctx context.Context, tx pgx.Tx, tournamentID string, number int, mapCode string) error {
	if mapCode == "" {
		_, err := tx.Exec(ctx, `UPDATE rounds SET map = '', map_code = NULL WHERE tournament_id = $1 AND number = $2`, tournamentID, number)
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE rounds SET map = (SELECT name FROM maps WHERE code = $3), map_code = $3
		WHERE tournament_id = $1 AND number = $2`, tournamentID, number, mapCode)
	return err
}

func insertVeto(ctx context.Context, tx pgx.Tx, tournamentID string, seq int, st models.VetoStep, mapCode string) error {
	var round *int
	if st.Round > 0 {
		r := st.Round
		round = &r
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO match_veto (tournament_id, seq, action, side, map_code, round_number)
		VALUES ($1, $2, $3, $4, $5, $6)`, tournamentID, seq, st.Action, st.Side, mapCode, round)
	return err
}

// VetoMap делает следующий ход пиков-банов картой mapCode. Если по порядку дальше идёт оставшаяся
// карта и она одна, она сама уходит в свой раунд.
func (s *Store) VetoMap(ctx context.Context, tournamentID, mapCode string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if started, err := roundsStarted(ctx, tx, tournamentID); err != nil {
		return err
	} else if started {
		return ErrMatchStarted
	}
	var format string
	if err := tx.QueryRow(ctx, `SELECT format FROM tournaments WHERE id = $1`, tournamentID).Scan(&format); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	order := VetoOrder(format)
	var done int
	var used bool
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(BOOL_OR(map_code = $2), false) FROM match_veto WHERE tournament_id = $1`,
		tournamentID, mapCode).Scan(&done, &used); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM maps WHERE code = $1 AND active)`, mapCode).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if used || done >= len(order) {
		return ErrConflict
	}
	step := order[done]
	if err := insertVeto(ctx, tx, tournamentID, done+1, step, mapCode); err != nil {
		return err
	}
	if step.Round > 0 {
		if err := setRoundMap(ctx, tx, tournamentID, step.Round, mapCode); err != nil {
			return err
		}
	}
	done++

	if done < len(order) && order[done].Action == "rest" {
		rows, err := tx.Query(ctx, `
			SELECT code FROM maps WHERE active
			  AND code NOT IN (SELECT map_code FROM match_veto WHERE tournament_id = $1)`, tournamentID)
		if err != nil {
			return err
		}
		var rest []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			rest = append(rest, c)
		}
		rows.Close()
		if len(rest) == 1 {
			last := order[done]
			if err := insertVeto(ctx, tx, tournamentID, done+1, last, rest[0]); err != nil {
				return err
			}
			if err := setRoundMap(ctx, tx, tournamentID, last.Round, rest[0]); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// VetoUndo отменяет последний ход. Оставшаяся карта ставилась сама, поэтому вместе с ней
// отменяется и ход перед ней.
func (s *Store) VetoUndo(ctx context.Context, tournamentID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if started, err := roundsStarted(ctx, tx, tournamentID); err != nil {
		return err
	} else if started {
		return ErrMatchStarted
	}
	undo := func() (string, error) {
		var action string
		var round *int
		err := tx.QueryRow(ctx, `
			DELETE FROM match_veto WHERE id = (
				SELECT id FROM match_veto WHERE tournament_id = $1 ORDER BY seq DESC LIMIT 1
			) RETURNING action, round_number`, tournamentID).Scan(&action, &round)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if round != nil {
			if err := setRoundMap(ctx, tx, tournamentID, *round, ""); err != nil {
				return "", err
			}
		}
		return action, nil
	}
	action, err := undo()
	if err != nil {
		return err
	}
	if action == "rest" {
		if _, err := undo(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SetMatchMaps задаёт карты раундов вручную, если пики-баны прошли вне эфира: codes[i] - карта (i+1)-го раунда.
func (s *Store) SetMatchMaps(ctx context.Context, tournamentID string, codes []string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if started, err := roundsStarted(ctx, tx, tournamentID); err != nil {
		return err
	} else if started {
		return ErrMatchStarted
	}
	seen := map[string]bool{}
	for _, c := range codes {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM maps WHERE code = $1)`, c).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if seen[c] {
			return ErrConflict
		}
		seen[c] = true
	}
	if _, err := tx.Exec(ctx, `DELETE FROM match_veto WHERE tournament_id = $1`, tournamentID); err != nil {
		return err
	}
	for i, c := range codes {
		if err := setRoundMap(ctx, tx, tournamentID, i+1, c); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
