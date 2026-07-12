package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// StartMmr — стартовый рейтинг каждого игрока и каждой команды.
const StartMmr = 1000

// kFactor — коэффициент Elo. Фиксированный K=32 (сверено с боевой таблицей организатора:
// формула воспроизводит текущие MMR игроков/команд, см. сессию по внедрению).
const kFactor = 32.0

// expectedScore — ожидаемый счёт игрока с рейтингом self против opp (классический Elo).
func expectedScore(self, opp int) float64 {
	return 1.0 / (1.0 + math.Pow(10, float64(opp-self)/400.0))
}

// eloWinMagnitude — насколько меняется рейтинг по итогу матча «победитель vs проигравший»
// (победитель +N, проигравший −N — zero-sum). mult — жетон «×2 рейтинга»: при mult=2 Elo
// применяется дважды, причём ВТОРОЙ раз считается от уже обновлённого рейтинга (компаундинг),
// как в таблице (там ×2-матч записан двумя строками подряд). Округление каждого начисления —
// к ближайшему целому (half away from zero, как math.Round).
func eloWinMagnitude(winnerMmr, loserMmr, mult int) int {
	if mult < 1 {
		mult = 1
	}
	w, l := winnerMmr, loserMmr
	total := 0
	for i := 0; i < mult; i++ {
		g := int(math.Round(kFactor * expectedScore(l, w))) // = K·(ожидаемый счёт проигравшего)
		total += g
		w += g
		l -= g
	}
	return total
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

// teamKeyFromParticipant возвращает ключ команды 2×2 = отсортированная пара userId
// (member_a < member_b). ok=false, если состав неполный (нет ровно двух игроков) —
// такую команду рейтинговать нельзя.
func teamKeyFromParticipant(p models.Participant) (key, memberA, memberB string, ok bool) {
	users := participantUsers(p)
	if len(users) != 2 {
		return "", "", "", false
	}
	sort.Strings(users)
	return users[0] + "|" + users[1], users[0], users[1], true
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

// BestTeamMmr — лучший (максимальный) MMR среди команд 2×2, где состоит игрок
// (StartMmr, если игрок ещё не сыграл ни одного 2×2). Для профиля.
func (s *Store) BestTeamMmr(ctx context.Context, userID string) (int, error) {
	var mmr *int
	err := s.Pool.QueryRow(ctx,
		`SELECT MAX(mmr) FROM team_mmr WHERE member_a=$1 OR member_b=$1`, userID).Scan(&mmr)
	if err != nil {
		return StartMmr, err
	}
	if mmr == nil {
		return StartMmr, nil
	}
	return *mmr, nil
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

// ensureTeam гарантирует наличие строки команды в team_mmr (со стартовым 1000) и возвращает её
// текущий MMR. Ключ и состав — из teamKeyFromParticipant.
func (s *Store) ensureTeam(ctx context.Context, key, memberA, memberB, name string) (int, error) {
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO team_mmr (team_key, member_a, member_b, mmr, name)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (team_key) DO UPDATE SET name = EXCLUDED.name WHERE EXCLUDED.name <> ''`,
		key, memberA, memberB, StartMmr, name); err != nil {
		return StartMmr, err
	}
	var mmr int
	err := s.Pool.QueryRow(ctx, `SELECT mmr FROM team_mmr WHERE team_key=$1`, key).Scan(&mmr)
	return mmr, err
}

// recomputeTeamMmr материализует кэш team_mmr из истории: mmr = StartMmr + SUM(delta).
func (s *Store) recomputeTeamMmr(ctx context.Context, key string) error {
	var sum int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(delta),0) FROM team_mmr_history WHERE team_key=$1`, key).Scan(&sum); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE team_mmr SET mmr=$2, updated_at=now() WHERE team_key=$1`, key, StartMmr+sum)
	return err
}

// ApplyTournamentMmr пересчитывает MMR по итогу турнира (турнир нового концепта = один матч,
// ровно две стороны). Идемпотентно: сначала откатывает прежние начисления этого турнира, затем
// начисляет заново (выдерживает повторный finished и смену победителя). Без победителя или если
// сторон не ровно две — MMR не двигается. 1×1 — рейтинг игроков (user_mmr); 2×2 — рейтинг КОМАНД
// (team_mmr по паре userId). Жетон «×2 рейтинга» (rating_multiplier=2) применяет Elo дважды.
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
	// Ровно две стороны: победитель и проигравший.
	if len(t.Participants) != 2 {
		return nil
	}
	var winner, loser models.Participant
	found := false
	for _, p := range t.Participants {
		if p.ID == *t.WinnerParticipantID {
			winner, found = p, true
		} else {
			loser = p
		}
	}
	if !found {
		return nil
	}
	mult := t.RatingMultiplier
	if mult < 1 {
		mult = 1
	}
	if t.Mode == "2x2" {
		return s.applyTeamMatch(ctx, tournamentID, winner, loser, mult)
	}
	return s.applyUserMatch(ctx, tournamentID, winner, loser, mult)
}

// applyUserMatch — начисление 1×1 (рейтинг игроков).
func (s *Store) applyUserMatch(ctx context.Context, tournamentID string, winner, loser models.Participant, mult int) error {
	if winner.UserID == nil || loser.UserID == nil || *winner.UserID == "" || *loser.UserID == "" {
		return nil
	}
	wu, lu := *winner.UserID, *loser.UserID
	rw, err := s.GetUserMmr(ctx, wu, "1x1")
	if err != nil {
		return err
	}
	rl, err := s.GetUserMmr(ctx, lu, "1x1")
	if err != nil {
		return err
	}
	mag := eloWinMagnitude(rw, rl, mult)
	if err := s.insertUserMmrHistory(ctx, tournamentID, wu, "1x1", mag, rw); err != nil {
		return err
	}
	if err := s.insertUserMmrHistory(ctx, tournamentID, lu, "1x1", -mag, rl); err != nil {
		return err
	}
	if err := s.recomputeUserMmr(ctx, wu, "1x1"); err != nil {
		return err
	}
	return s.recomputeUserMmr(ctx, lu, "1x1")
}

func (s *Store) insertUserMmrHistory(ctx context.Context, tournamentID, userID, mode string, delta, before int) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO mmr_history (user_id, mode, tournament_id, delta, mmr_before, mmr_after)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tournament_id, user_id, mode) DO NOTHING`,
		userID, mode, tournamentID, delta, before, before+delta)
	return err
}

// applyTeamMatch — начисление 2×2 (рейтинг КОМАНД). Неполные составы не рейтингуются.
func (s *Store) applyTeamMatch(ctx context.Context, tournamentID string, winner, loser models.Participant, mult int) error {
	wk, wa, wb, ok1 := teamKeyFromParticipant(winner)
	lk, la, lb, ok2 := teamKeyFromParticipant(loser)
	if !ok1 || !ok2 || wk == lk {
		return nil
	}
	rw, err := s.ensureTeam(ctx, wk, wa, wb, winner.Name)
	if err != nil {
		return err
	}
	rl, err := s.ensureTeam(ctx, lk, la, lb, loser.Name)
	if err != nil {
		return err
	}
	mag := eloWinMagnitude(rw, rl, mult)
	if err := s.insertTeamMmrHistory(ctx, tournamentID, wk, mag, rw); err != nil {
		return err
	}
	if err := s.insertTeamMmrHistory(ctx, tournamentID, lk, -mag, rl); err != nil {
		return err
	}
	if err := s.recomputeTeamMmr(ctx, wk); err != nil {
		return err
	}
	return s.recomputeTeamMmr(ctx, lk)
}

func (s *Store) insertTeamMmrHistory(ctx context.Context, tournamentID, key string, delta, before int) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO team_mmr_history (team_key, tournament_id, delta, mmr_before, mmr_after)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tournament_id, team_key) DO NOTHING`,
		key, tournamentID, delta, before, before+delta)
	return err
}

// RevertTournamentMmr снимает все начисления MMR этого турнира (и игроков, и команд) и
// пересчитывает затронутых.
func (s *Store) RevertTournamentMmr(ctx context.Context, tournamentID string) error {
	// --- Игроки (1×1) ---
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT user_id, mode FROM mmr_history WHERE tournament_id=$1`, tournamentID)
	if err != nil {
		return err
	}
	type um struct{ u, m string }
	var users []um
	for rows.Next() {
		var a um
		if err := rows.Scan(&a.u, &a.m); err != nil {
			rows.Close()
			return err
		}
		users = append(users, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM mmr_history WHERE tournament_id=$1`, tournamentID); err != nil {
		return err
	}
	for _, a := range users {
		if err := s.recomputeUserMmr(ctx, a.u, a.m); err != nil {
			return err
		}
	}

	// --- Команды (2×2) ---
	trows, err := s.Pool.Query(ctx, `SELECT DISTINCT team_key FROM team_mmr_history WHERE tournament_id=$1`, tournamentID)
	if err != nil {
		return err
	}
	var teams []string
	for trows.Next() {
		var k string
		if err := trows.Scan(&k); err != nil {
			trows.Close()
			return err
		}
		teams = append(teams, k)
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM team_mmr_history WHERE tournament_id=$1`, tournamentID); err != nil {
		return err
	}
	for _, k := range teams {
		if err := s.recomputeTeamMmr(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// PopulateTournamentMmrChanges заполняет t.MmrChanges — изменение MMR каждой стороны за ЭТОТ матч
// (из mmr_history для 1×1, из team_mmr_history для 2×2). Пусто, если матч ещё не начислен.
func (s *Store) PopulateTournamentMmrChanges(ctx context.Context, t *models.Tournament) {
	t.MmrChanges = []models.ParticipantMmr{}
	for _, p := range t.Participants {
		var before, after, delta int
		var err error
		if t.Mode == "2x2" {
			key, _, _, ok := teamKeyFromParticipant(p)
			if !ok {
				continue
			}
			err = s.Pool.QueryRow(ctx,
				`SELECT mmr_before, mmr_after, delta FROM team_mmr_history WHERE tournament_id=$1 AND team_key=$2`,
				t.ID, key).Scan(&before, &after, &delta)
		} else {
			if p.UserID == nil {
				continue
			}
			err = s.Pool.QueryRow(ctx,
				`SELECT mmr_before, mmr_after, delta FROM mmr_history WHERE tournament_id=$1 AND user_id=$2 AND mode='1x1'`,
				t.ID, *p.UserID).Scan(&before, &after, &delta)
		}
		if err == nil {
			t.MmrChanges = append(t.MmrChanges, models.ParticipantMmr{
				ParticipantID: p.ID, Before: before, After: after, Delta: delta,
			})
		}
	}
}

// RecomputeAllMmr полностью пересчитывает MMR (игроков и команд) с нуля по ВСЕМ завершённым
// турнирам в хронологическом порядке — для синка, где меняется состав завершённых матчей.
func (s *Store) RecomputeAllMmr(ctx context.Context) error {
	for _, q := range []string{`TRUNCATE mmr_history`, `TRUNCATE team_mmr_history`, `DELETE FROM user_mmr`, `DELETE FROM team_mmr`} {
		if _, err := s.Pool.Exec(ctx, q); err != nil {
			return err
		}
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id FROM tournaments WHERE status='finished' ORDER BY COALESCE(starts_at, created_at), created_at`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.ApplyTournamentMmr(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
