package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// StartMmr — стартовый рейтинг каждого игрока.
const StartMmr = 1000

// mmrDelta — ЗАГЛУШКА расчёта изменения MMR по итогу турнира. Реальную формулу пользователь
// даст позже; ЭТА функция — единственная точка её замены (изолирована намеренно). Сейчас:
// фиксированные ±25 за победу/поражение; сила соперника (oppMmr) пока игнорируется.
// TODO(mmr): заменить на реальную формулу (например Elo: K·(S − E), E от разницы рейтингов).
func mmrDelta(selfMmr, oppMmr int, won bool) int {
	const k = 25
	if won {
		return k
	}
	return -k
}

// participantUsers — аккаунты стороны: одиночный user_id (1x1) либо составы members[].userId (2x2).
func participantUsers(p models.Participant) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if p.UserID != nil {
		add(*p.UserID)
	}
	if len(p.Members) > 0 {
		var ms []struct {
			UserID string `json:"userId"`
		}
		if json.Unmarshal(p.Members, &ms) == nil {
			for _, m := range ms {
				add(m.UserID)
			}
		}
	}
	return out
}

// GetUserMmr возвращает текущий MMR пользователя в режиме (StartMmr, если записи ещё нет).
func (s *Store) GetUserMmr(ctx context.Context, userID, mode string) (int, error) {
	var mmr int
	err := s.Pool.QueryRow(ctx, `SELECT mmr FROM user_mmr WHERE user_id=$1 AND mode=$2`, userID, mode).Scan(&mmr)
	if errors.Is(err, pgx.ErrNoRows) {
		return StartMmr, nil
	}
	return mmr, err
}

// recomputeUserMmr материализует кэш user_mmr из истории: mmr = StartMmr + SUM(delta).
func (s *Store) recomputeUserMmr(ctx context.Context, userID, mode string) error {
	var sum int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(delta),0) FROM mmr_history WHERE user_id=$1 AND mode=$2`, userID, mode).Scan(&sum); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO user_mmr (user_id, mode, mmr, updated_at) VALUES ($1,$2,$3, now())
		ON CONFLICT (user_id, mode) DO UPDATE SET mmr=EXCLUDED.mmr, updated_at=now()`,
		userID, mode, StartMmr+sum)
	return err
}

// ApplyTournamentMmr пересчитывает MMR по итогу турнира (идемпотентно: сначала откатывает
// прежние начисления этого турнира, затем начисляет заново — выдерживает повторный finished и
// смену победителя). Без победителя MMR не двигается.
func (s *Store) ApplyTournamentMmr(ctx context.Context, tournamentID string) error {
	t, err := s.GetTournament(ctx, tournamentID)
	if err != nil {
		return err
	}
	if err := s.RevertTournamentMmr(ctx, tournamentID); err != nil {
		return err
	}
	if t.WinnerParticipantID == nil || *t.WinnerParticipantID == "" {
		return nil
	}
	mode := t.Mode

	// Средний MMR каждой стороны (основа для будущей формулы).
	sideUsers := map[string][]string{}
	sideMmr := map[string]int{}
	for _, p := range t.Participants {
		users := participantUsers(p)
		sideUsers[p.ID] = users
		sum, n := 0, 0
		for _, u := range users {
			m, err := s.GetUserMmr(ctx, u, mode)
			if err != nil {
				return err
			}
			sum += m
			n++
		}
		if n > 0 {
			sideMmr[p.ID] = sum / n
		} else {
			sideMmr[p.ID] = StartMmr
		}
	}

	for _, p := range t.Participants {
		won := p.ID == *t.WinnerParticipantID
		// Средний MMR оппонентов (всех прочих сторон).
		oppSum, oppN := 0, 0
		for _, q := range t.Participants {
			if q.ID == p.ID {
				continue
			}
			oppSum += sideMmr[q.ID]
			oppN++
		}
		opp := StartMmr
		if oppN > 0 {
			opp = oppSum / oppN
		}
		for _, u := range sideUsers[p.ID] {
			before, err := s.GetUserMmr(ctx, u, mode)
			if err != nil {
				return err
			}
			delta := mmrDelta(sideMmr[p.ID], opp, won)
			if _, err := s.Pool.Exec(ctx, `
				INSERT INTO mmr_history (user_id, mode, tournament_id, delta, mmr_before, mmr_after)
				VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (tournament_id, user_id, mode) DO NOTHING`,
				u, mode, tournamentID, delta, before, before+delta); err != nil {
				return err
			}
			if err := s.recomputeUserMmr(ctx, u, mode); err != nil {
				return err
			}
		}
	}
	return nil
}

// RevertTournamentMmr снимает все начисления MMR этого турнира и пересчитывает затронутых игроков.
func (s *Store) RevertTournamentMmr(ctx context.Context, tournamentID string) error {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT user_id, mode FROM mmr_history WHERE tournament_id=$1`, tournamentID)
	if err != nil {
		return err
	}
	type um struct{ u, m string }
	var affected []um
	for rows.Next() {
		var a um
		if err := rows.Scan(&a.u, &a.m); err != nil {
			rows.Close()
			return err
		}
		affected = append(affected, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM mmr_history WHERE tournament_id=$1`, tournamentID); err != nil {
		return err
	}
	for _, a := range affected {
		if err := s.recomputeUserMmr(ctx, a.u, a.m); err != nil {
			return err
		}
	}
	return nil
}
