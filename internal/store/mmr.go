package store

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
)

// StartMmr и legacyK - правила рейтинга для турниров без сезона.
const (
	StartMmr = 1000
	legacyK  = 32
)

// seasonRule - правила рейтинга, по которым считается матч: сезон, K-фактор Эло и стартовый MMR.
// Пустой Key - турнир без сезона.
type seasonRule struct {
	Key   string
	K     int
	Start int
}

func (s *Store) tournamentSeasonRule(ctx context.Context, tournamentID string) (seasonRule, error) {
	var r seasonRule
	err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(t.season_id, ''), COALESCE(sn.k_factor, $2::int), COALESCE(sn.start_mmr, $3::int)
		FROM tournaments t LEFT JOIN seasons sn ON sn.id = t.season_id
		WHERE t.id = $1`, tournamentID, legacyK, StartMmr).Scan(&r.Key, &r.K, &r.Start)
	return r, err
}

// activeSeasonRule - правила текущего сезона; без активного сезона - правила «вне сезона».
func (s *Store) activeSeasonRule(ctx context.Context) seasonRule {
	r := seasonRule{Key: "", K: legacyK, Start: StartMmr}
	_ = s.Pool.QueryRow(ctx, `SELECT id, k_factor, start_mmr FROM seasons WHERE status = 'active' LIMIT 1`).
		Scan(&r.Key, &r.K, &r.Start)
	return r
}

// ActiveSeasonStart - стартовый MMR текущего сезона (с ним показываются ещё не игравшие).
func (s *Store) ActiveSeasonStart(ctx context.Context) int { return s.activeSeasonRule(ctx).Start }

// expectedScore — ожидаемый счёт игрока с рейтингом self против opp (классический Elo).
func expectedScore(self, opp int) float64 {
	return 1.0 / (1.0 + math.Pow(10, float64(opp-self)/400.0))
}

// eloWinMagnitude - насколько меняется рейтинг по итогу матча (победитель +N, проигравший -N).
// Матч, засчитанный за несколько (games: ×2 прошлых сезонов и таблицы), - несколько начислений
// подряд, каждое от обновлённого рейтинга; иначе жетон ×2 (mult) просто удваивает начисление.
// Каждое начисление округляется к ближайшему целому.
func eloWinMagnitude(winnerMmr, loserMmr, mult, games, k int) int {
	if games < 1 {
		games = 1
	}
	per := mult / games
	if per < 1 {
		per = 1
	}
	w, l := winnerMmr, loserMmr
	total := 0
	for i := 0; i < games; i++ {
		g := int(math.Round(float64(k)*expectedScore(l, w))) * per // = K·(ожидаемый счёт проигравшего)
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

// userSeasonMmr - MMR игрока в сезоне: стартовый MMR сезона плюс изменения за его матчи.
func (s *Store) userSeasonMmr(ctx context.Context, userID, mode string, rule seasonRule) (int, error) {
	var sum int
	err := s.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(delta), 0) FROM mmr_history WHERE user_id = $1 AND mode = $2 AND season_key = $3`,
		userID, mode, rule.Key).Scan(&sum)
	return rule.Start + sum, err
}

// GetUserMmr возвращает MMR пользователя в текущем сезоне.
func (s *Store) GetUserMmr(ctx context.Context, userID, mode string) (int, error) {
	return s.userSeasonMmr(ctx, userID, mode, s.activeSeasonRule(ctx))
}

// BestTeamMmr - лучший MMR в текущем сезоне среди команд 2×2, где состоит игрок
// (старт сезона, если команд нет). Для профиля.
func (s *Store) BestTeamMmr(ctx context.Context, userID string) (int, error) {
	start := s.activeSeasonRule(ctx).Start
	var mmr *int
	err := s.Pool.QueryRow(ctx,
		`SELECT MAX(mmr) FROM team_mmr WHERE member_a=$1 OR member_b=$1`, userID).Scan(&mmr)
	if err != nil {
		return start, err
	}
	if mmr == nil {
		return start, nil
	}
	return *mmr, nil
}

// recomputeUserMmr обновляет кэш user_mmr - MMR игрока в текущем сезоне. Строка есть только у
// тех, кто в этом сезоне играл: по ним считается место в таблице.
func (s *Store) recomputeUserMmr(ctx context.Context, userID, mode string) error {
	rule := s.activeSeasonRule(ctx)
	var sum, n int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(delta), 0), COUNT(tournament_id) FROM mmr_history WHERE user_id = $1 AND mode = $2 AND season_key = $3`,
		userID, mode, rule.Key).Scan(&sum, &n); err != nil {
		return err
	}
	if n == 0 {
		_, err := s.Pool.Exec(ctx, `DELETE FROM user_mmr WHERE user_id = $1 AND mode = $2`, userID, mode)
		return err
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO user_mmr (user_id, mode, mmr, updated_at) VALUES ($1,$2,$3, now())
		ON CONFLICT (user_id, mode) DO UPDATE SET mmr=EXCLUDED.mmr, updated_at=now()`,
		userID, mode, rule.Start+sum)
	return err
}

// ensureTeam гарантирует строку команды в team_mmr (состав и название).
func (s *Store) ensureTeam(ctx context.Context, key, memberA, memberB, name string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO team_mmr (team_key, member_a, member_b, mmr, name)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (team_key) DO UPDATE SET name = EXCLUDED.name WHERE EXCLUDED.name <> ''`,
		key, memberA, memberB, StartMmr, name)
	return err
}

// teamSeasonMmr - MMR команды в сезоне: стартовый MMR сезона плюс изменения за её матчи.
func (s *Store) teamSeasonMmr(ctx context.Context, key string, rule seasonRule) (int, error) {
	var sum int
	err := s.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(delta), 0) FROM team_mmr_history WHERE team_key = $1 AND season_key = $2`,
		key, rule.Key).Scan(&sum)
	return rule.Start + sum, err
}

// recomputeTeamMmr обновляет кэш team_mmr.mmr - MMR команды в текущем сезоне.
func (s *Store) recomputeTeamMmr(ctx context.Context, key string) error {
	rule := s.activeSeasonRule(ctx)
	mmr, err := s.teamSeasonMmr(ctx, key, rule)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE team_mmr SET mmr=$2, updated_at=now() WHERE team_key=$1`, key, mmr)
	return err
}

// RefreshMmrCaches пересобирает кэши MMR под текущий сезон (после смены сезона или его правил).
func (s *Store) RefreshMmrCaches(ctx context.Context) error {
	rule := s.activeSeasonRule(ctx)
	if _, err := s.Pool.Exec(ctx, `DELETE FROM user_mmr`); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO user_mmr (user_id, mode, mmr, updated_at)
		SELECT user_id, mode, $2::int + SUM(delta), now() FROM mmr_history
		WHERE season_key = $1 GROUP BY user_id, mode HAVING COUNT(tournament_id) > 0`, rule.Key, rule.Start); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `
		UPDATE team_mmr tm SET mmr = $2::int + COALESCE((
			SELECT SUM(h.delta) FROM team_mmr_history h WHERE h.team_key = tm.team_key AND h.season_key = $1
		), 0), updated_at = now()`, rule.Key, rule.Start); err != nil {
		return err
	}
	// Смена сезона или пересчёт могут поменять победителей прошлых сезонов.
	return s.SyncSeasonWinnerTags(ctx)
}

// ApplyTournamentMmr пересчитывает MMR по итогу турнира (турнир нового концепта = один матч,
// ровно две стороны). Идемпотентно: сначала откатывает прежние начисления этого турнира, затем
// начисляет заново (выдерживает повторный finished и смену победителя). Без победителя или если
// сторон не ровно две — MMR не двигается. 1×1 — рейтинг игроков (user_mmr); 2×2 — рейтинг КОМАНД
// (team_mmr по паре userId). Как считается жетон ×2 - см. eloWinMagnitude.
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
	rule, err := s.tournamentSeasonRule(ctx, tournamentID)
	if err != nil {
		return err
	}
	if t.Mode == "2x2" {
		return s.applyTeamMatch(ctx, tournamentID, winner, loser, mult, t.Games, rule)
	}
	return s.applyUserMatch(ctx, tournamentID, winner, loser, mult, t.Games, rule)
}

// applyUserMatch — начисление 1×1 (рейтинг игроков).
func (s *Store) applyUserMatch(ctx context.Context, tournamentID string, winner, loser models.Participant, mult, games int, rule seasonRule) error {
	if winner.UserID == nil || loser.UserID == nil || *winner.UserID == "" || *loser.UserID == "" {
		return nil
	}
	wu, lu := *winner.UserID, *loser.UserID
	rw, err := s.userSeasonMmr(ctx, wu, "1x1", rule)
	if err != nil {
		return err
	}
	rl, err := s.userSeasonMmr(ctx, lu, "1x1", rule)
	if err != nil {
		return err
	}
	mag := eloWinMagnitude(rw, rl, mult, games, rule.K)
	winDelta, loseDelta := mag, -mag
	// Матч перенесён из внешнего источника: его изменения MMR берутся как есть.
	var pinW, pinL *int
	if err := s.Pool.QueryRow(ctx, `
		SELECT (SELECT mmr_delta FROM participants WHERE id = $1), (SELECT mmr_delta FROM participants WHERE id = $2)`,
		winner.ID, loser.ID).Scan(&pinW, &pinL); err != nil {
		return err
	}
	if pinW != nil && pinL != nil {
		winDelta, loseDelta = *pinW, *pinL
	}
	if err := s.insertUserMmrHistory(ctx, tournamentID, wu, "1x1", winDelta, rw, rule.Key); err != nil {
		return err
	}
	if err := s.insertUserMmrHistory(ctx, tournamentID, lu, "1x1", loseDelta, rl, rule.Key); err != nil {
		return err
	}
	if err := s.recomputeUserMmr(ctx, wu, "1x1"); err != nil {
		return err
	}
	return s.recomputeUserMmr(ctx, lu, "1x1")
}

// История датируется временем матча - по ней строятся график и лента матчей.
func (s *Store) insertUserMmrHistory(ctx context.Context, tournamentID, userID, mode string, delta, before int, seasonKey string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO mmr_history (user_id, mode, tournament_id, delta, mmr_before, mmr_after, season_key, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7, (SELECT COALESCE(starts_at, created_at) FROM tournaments WHERE id = $3))
		ON CONFLICT (tournament_id, user_id, mode) DO NOTHING`,
		userID, mode, tournamentID, delta, before, before+delta, seasonKey)
	return err
}

// applyTeamMatch — начисление 2×2 (рейтинг КОМАНД). Неполные составы не рейтингуются.
func (s *Store) applyTeamMatch(ctx context.Context, tournamentID string, winner, loser models.Participant, mult, games int, rule seasonRule) error {
	wk, wa, wb, ok1 := teamKeyFromParticipant(winner)
	lk, la, lb, ok2 := teamKeyFromParticipant(loser)
	if !ok1 || !ok2 || wk == lk {
		return nil
	}
	if err := s.ensureTeam(ctx, wk, wa, wb, winner.Name); err != nil {
		return err
	}
	if err := s.ensureTeam(ctx, lk, la, lb, loser.Name); err != nil {
		return err
	}
	rw, err := s.teamSeasonMmr(ctx, wk, rule)
	if err != nil {
		return err
	}
	rl, err := s.teamSeasonMmr(ctx, lk, rule)
	if err != nil {
		return err
	}
	mag := eloWinMagnitude(rw, rl, mult, games, rule.K)
	if err := s.insertTeamMmrHistory(ctx, tournamentID, wk, mag, rw, rule.Key); err != nil {
		return err
	}
	if err := s.insertTeamMmrHistory(ctx, tournamentID, lk, -mag, rl, rule.Key); err != nil {
		return err
	}
	if err := s.recomputeTeamMmr(ctx, wk); err != nil {
		return err
	}
	return s.recomputeTeamMmr(ctx, lk)
}

func (s *Store) insertTeamMmrHistory(ctx context.Context, tournamentID, key string, delta, before int, seasonKey string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO team_mmr_history (team_key, tournament_id, delta, mmr_before, mmr_after, season_key, created_at)
		VALUES ($1,$2,$3,$4,$5,$6, (SELECT COALESCE(starts_at, created_at) FROM tournaments WHERE id = $2))
		ON CONFLICT (tournament_id, team_key) DO NOTHING`,
		key, tournamentID, delta, before, before+delta, seasonKey)
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

// MmrCorrection - сверка рейтинга игрока с официальными цифрами на дату.
type MmrCorrection struct {
	UserID   string
	SeasonID string
	Delta    int
	At       time.Time
	Note     string
}

// ReplaceMmrCorrections заменяет сверки из одного источника: повторный импорт не копит их.
func (s *Store) ReplaceMmrCorrections(ctx context.Context, source string, items []MmrCorrection) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM mmr_corrections WHERE source = $1`, source); err != nil {
		return err
	}
	for _, c := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO mmr_corrections (user_id, season_id, delta, at, source, note)
			VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6)`, c.UserID, c.SeasonID, c.Delta, c.At, source, c.Note); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) applyCorrection(ctx context.Context, userID, seasonID string, delta int, at time.Time) error {
	rule := seasonRule{Key: "", K: legacyK, Start: StartMmr}
	if seasonID != "" {
		if err := s.Pool.QueryRow(ctx, `SELECT id, k_factor, start_mmr FROM seasons WHERE id = $1`, seasonID).
			Scan(&rule.Key, &rule.K, &rule.Start); err != nil {
			return err
		}
	}
	before, err := s.userSeasonMmr(ctx, userID, "1x1", rule)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO mmr_history (user_id, mode, tournament_id, delta, mmr_before, mmr_after, season_key, created_at)
		VALUES ($1, '1x1', NULL, $2, $3, $4, $5, $6)`, userID, delta, before, before+delta, rule.Key, at)
	return err
}

// RecomputeAllMmr полностью пересчитывает MMR (игроков и команд) с нуля по ВСЕМ завершённым
// турнирам и сверкам в хронологическом порядке: каждый матч - по правилам своего сезона.
func (s *Store) RecomputeAllMmr(ctx context.Context) error {
	for _, q := range []string{`TRUNCATE mmr_history`, `TRUNCATE team_mmr_history`, `DELETE FROM user_mmr`, `DELETE FROM team_mmr`} {
		if _, err := s.Pool.Exec(ctx, q); err != nil {
			return err
		}
	}
	// Сверка в один момент с матчем идёт раньше него: она про рейтинг до этого матча.
	rows, err := s.Pool.Query(ctx, `
		SELECT id, '', '', 0, COALESCE(starts_at, created_at) AS at, 1 AS ord, created_at
		FROM tournaments WHERE status = 'finished'
		UNION ALL
		SELECT '', user_id, COALESCE(season_id, ''), delta, at, 0, at FROM mmr_corrections
		ORDER BY at, ord, created_at`)
	if err != nil {
		return err
	}
	type event struct {
		tournamentID, userID, seasonID string
		delta                          int
		at                             time.Time
	}
	var events []event
	for rows.Next() {
		var e event
		var ord int
		var created time.Time
		if err := rows.Scan(&e.tournamentID, &e.userID, &e.seasonID, &e.delta, &e.at, &ord, &created); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range events {
		var err error
		if e.tournamentID != "" {
			err = s.ApplyTournamentMmr(ctx, e.tournamentID)
		} else {
			err = s.applyCorrection(ctx, e.userID, e.seasonID, e.delta, e.at)
		}
		if err != nil {
			return err
		}
	}
	return s.RefreshMmrCaches(ctx)
}

// RunPendingRecompute пересчитывает MMR, если миграция поставила флаг (сменились правила подсчёта).
func (s *Store) RunPendingRecompute(ctx context.Context) (bool, error) {
	ct, err := s.Pool.Exec(ctx, `DELETE FROM app_flags WHERE key = 'recompute_mmr'`)
	if err != nil {
		return false, err
	}
	if ct.RowsAffected() == 0 {
		return false, nil
	}
	return true, s.RecomputeAllMmr(ctx)
}
