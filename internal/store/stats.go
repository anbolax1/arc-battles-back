package store

import (
	"context"
	"math"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
)

// ================== Общая агрегация статистики из ленты матчей ==================

// computeMmrStats считает сводку по хронологической (ASC) ленте матчей: первый матч, пик MMR,
// W/L (взвешенно по жетону ×2), винрейт, лучшие вин/луз-стрики и текущую серию, текущий MMR
// (по последней точке). Место (Place) проставляет вызывающий.
func computeMmrStats(points []models.MmrPoint) models.MmrStats {
	st := models.MmrStats{CurrentMmr: StartMmr, PeakMmr: StartMmr}
	if len(points) == 0 {
		return st
	}
	st.FirstMatch = points[0].Date
	peak := StartMmr
	curKind, curLen := "", 0
	for _, p := range points {
		w := p.Mult
		if w < 1 {
			w = 1
		}
		if p.Win {
			st.Wins += w
		} else {
			st.Losses += w
		}
		if p.Mmr > peak {
			peak = p.Mmr
		}
		kind := "loss"
		if p.Win {
			kind = "win"
		}
		if curKind == kind {
			curLen++
		} else {
			curKind, curLen = kind, 1
		}
		if kind == "win" && curLen > st.BestWinStreak {
			st.BestWinStreak = curLen
		}
		if kind == "loss" && curLen > st.BestLossStreak {
			st.BestLossStreak = curLen
		}
	}
	st.PeakMmr = peak
	st.Games = st.Wins + st.Losses
	if st.Games > 0 {
		st.Winrate = int(math.Round(float64(st.Wins) * 100 / float64(st.Games)))
	}
	st.CurrentStreakKind, st.CurrentStreakLen = curKind, curLen
	st.CurrentMmr = points[len(points)-1].Mmr
	return st
}

// ================== 1×1 (игроки) ==================

// Player1x1Timeline — динамика MMR игрока в 1×1 (ASC по времени): соперник, карта, дельта, исход.
func (s *Store) Player1x1Timeline(ctx context.Context, userID string) ([]models.MmrPoint, error) {
	const q = `
		SELECT h.tournament_id, t.title, h.created_at, h.delta, h.mmr_after, t.rating_multiplier,
		       COALESCE(opp.name, '') AS opp_name,
		       COALESCE(uu.login, '') AS opp_login,
		       COALESCE(NULLIF(r.map, ''), t.maps->>0, '') AS map
		FROM mmr_history h
		JOIN tournaments t ON t.id = h.tournament_id
		LEFT JOIN LATERAL (
		    SELECT p2.name, p2.user_id FROM participants p2
		    WHERE p2.tournament_id = t.id AND (p2.user_id IS NULL OR p2.user_id <> $1)
		    ORDER BY p2.seed LIMIT 1
		) opp ON true
		LEFT JOIN users uu ON uu.id = opp.user_id
		LEFT JOIN LATERAL (SELECT map FROM rounds WHERE tournament_id = t.id ORDER BY number LIMIT 1) r ON true
		WHERE h.user_id = $1 AND h.mode = '1x1'
		ORDER BY h.created_at ASC, t.created_at ASC`
	return s.scanTimeline(ctx, q, userID)
}

// Player1x1Maps — разбивка матчей 1×1 по картам (взвешенно по ×2).
func (s *Store) Player1x1Maps(ctx context.Context, userID string) ([]models.MapStat, error) {
	const q = `
		SELECT COALESCE(NULLIF(r.map, ''), t.maps->>0, '') AS mp,
		       COALESCE(SUM(t.rating_multiplier) FILTER (WHERE h.delta > 0), 0)::int AS wins,
		       COALESCE(SUM(t.rating_multiplier) FILTER (WHERE h.delta < 0), 0)::int AS losses,
		       COALESCE(SUM(t.rating_multiplier), 0)::int AS games
		FROM mmr_history h
		JOIN tournaments t ON t.id = h.tournament_id
		LEFT JOIN LATERAL (SELECT map FROM rounds WHERE tournament_id = t.id ORDER BY number LIMIT 1) r ON true
		WHERE h.user_id = $1 AND h.mode = '1x1'
		GROUP BY mp
		ORDER BY games DESC, mp`
	return s.scanMapStats(ctx, q, userID)
}

// Player1x1Opponents — head-to-head игрока в 1×1 (взвешенно по ×2), по убыванию числа матчей.
func (s *Store) Player1x1Opponents(ctx context.Context, userID string) ([]models.OpponentStat, error) {
	const q = `
		SELECT COALESCE(uu.login, '') AS opp_login, COALESCE(opp.name, '') AS opp_name,
		       COALESCE(SUM(t.rating_multiplier) FILTER (WHERE h.delta > 0), 0)::int AS wins,
		       COALESCE(SUM(t.rating_multiplier) FILTER (WHERE h.delta < 0), 0)::int AS losses,
		       COALESCE(SUM(t.rating_multiplier), 0)::int AS games
		FROM mmr_history h
		JOIN tournaments t ON t.id = h.tournament_id
		LEFT JOIN LATERAL (
		    SELECT p2.name, p2.user_id FROM participants p2
		    WHERE p2.tournament_id = t.id AND (p2.user_id IS NULL OR p2.user_id <> $1)
		    ORDER BY p2.seed LIMIT 1
		) opp ON true
		LEFT JOIN users uu ON uu.id = opp.user_id
		WHERE h.user_id = $1 AND h.mode = '1x1'
		GROUP BY opp_login, opp_name
		ORDER BY games DESC, opp_name`
	rows, err := s.Pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.OpponentStat{}
	for rows.Next() {
		var o models.OpponentStat
		if err := rows.Scan(&o.Login, &o.Name, &o.Wins, &o.Losses, &o.Games); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Player1x1Place — место игрока в таблице 1×1 (0 — если ещё не играл 1×1).
func (s *Store) Player1x1Place(ctx context.Context, userID string) (int, error) {
	var myMmr *int
	if err := s.Pool.QueryRow(ctx,
		`SELECT mmr FROM user_mmr WHERE user_id=$1 AND mode='1x1'`, userID).Scan(&myMmr); err != nil {
		// нет строки → не играл
		return 0, nil
	}
	if myMmr == nil {
		return 0, nil
	}
	var above int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM user_mmr WHERE mode='1x1' AND mmr > $1`, *myMmr).Scan(&above); err != nil {
		return 0, err
	}
	return above + 1, nil
}

// ================== 2×2 (команды) ==================

// TeamsForUser — список команд игрока (краткие карточки с MMR и W/L), лучшие сверху.
func (s *Store) TeamsForUser(ctx context.Context, userID string) ([]models.TeamSummary, error) {
	const q = `
		SELECT tm.team_key, tm.mmr,
		       ua.id, ua.login, ua.display_name, ua.avatar_url,
		       ub.id, ub.login, ub.display_name, ub.avatar_url,
		       COALESCE(SUM(t.rating_multiplier) FILTER (WHERE h.delta > 0), 0)::int AS wins,
		       COALESCE(SUM(t.rating_multiplier) FILTER (WHERE h.delta < 0), 0)::int AS losses
		FROM team_mmr tm
		JOIN users ua ON ua.id = tm.member_a
		JOIN users ub ON ub.id = tm.member_b
		LEFT JOIN team_mmr_history h ON h.team_key = tm.team_key
		LEFT JOIN tournaments t ON t.id = h.tournament_id AND t.status = 'finished'
		WHERE tm.member_a = $1 OR tm.member_b = $1
		GROUP BY tm.team_key, tm.mmr, ua.id, ua.login, ua.display_name, ua.avatar_url,
		         ub.id, ub.login, ub.display_name, ub.avatar_url
		ORDER BY tm.mmr DESC`
	rows, err := s.Pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.TeamSummary{}
	for rows.Next() {
		var ts models.TeamSummary
		var a, b models.TeamMember
		if err := rows.Scan(&ts.TeamKey, &ts.Mmr,
			&a.UserID, &a.Login, &a.DisplayName, &a.AvatarURL,
			&b.UserID, &b.Login, &b.DisplayName, &b.AvatarURL,
			&ts.Wins, &ts.Losses); err != nil {
			return nil, err
		}
		ts.Games = ts.Wins + ts.Losses
		ts.Members = []models.TeamMember{a, b}
		out = append(out, ts)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if p, err := s.TeamPlace(ctx, out[i].Mmr); err == nil {
			out[i].Place = p
		}
	}
	return out, nil
}

// TeamMembers возвращает состав команды и её текущий MMR; ok=false, если команды нет.
func (s *Store) TeamMembers(ctx context.Context, teamKey string) ([]models.TeamMember, int, bool, error) {
	var a, b models.TeamMember
	var mmr int
	err := s.Pool.QueryRow(ctx, `
		SELECT tm.mmr, ua.id, ua.login, ua.display_name, ua.avatar_url,
		       ub.id, ub.login, ub.display_name, ub.avatar_url
		FROM team_mmr tm
		JOIN users ua ON ua.id = tm.member_a
		JOIN users ub ON ub.id = tm.member_b
		WHERE tm.team_key = $1`, teamKey).Scan(&mmr,
		&a.UserID, &a.Login, &a.DisplayName, &a.AvatarURL,
		&b.UserID, &b.Login, &b.DisplayName, &b.AvatarURL)
	if err != nil {
		return nil, 0, false, nil
	}
	return []models.TeamMember{a, b}, mmr, true, nil
}

// TeamPlace — место команды по MMR среди всех команд.
func (s *Store) TeamPlace(ctx context.Context, teamMmr int) (int, error) {
	var above int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM team_mmr WHERE mmr > $1`, teamMmr).Scan(&above); err != nil {
		return 0, err
	}
	return above + 1, nil
}

// TeamTimeline — динамика MMR команды (ASC по времени): соперник (команда), карта, дельта, исход.
func (s *Store) TeamTimeline(ctx context.Context, teamKey string) ([]models.MmrPoint, error) {
	const q = `
		SELECT h.tournament_id, t.title, h.created_at, h.delta, h.mmr_after, t.rating_multiplier,
		       COALESCE(opp.team_key, '') AS opp_key,
		       COALESCE(ua.login, '') AS opp_a, COALESCE(ub.login, '') AS opp_b,
		       COALESCE(NULLIF(r.map, ''), t.maps->>0, '') AS map
		FROM team_mmr_history h
		JOIN tournaments t ON t.id = h.tournament_id
		LEFT JOIN LATERAL (
		    SELECT h2.team_key FROM team_mmr_history h2
		    WHERE h2.tournament_id = t.id AND h2.team_key <> $1 LIMIT 1
		) opp ON true
		LEFT JOIN team_mmr tmo ON tmo.team_key = opp.team_key
		LEFT JOIN users ua ON ua.id = tmo.member_a
		LEFT JOIN users ub ON ub.id = tmo.member_b
		LEFT JOIN LATERAL (SELECT map FROM rounds WHERE tournament_id = t.id ORDER BY number LIMIT 1) r ON true
		WHERE h.team_key = $1
		ORDER BY h.created_at ASC, t.created_at ASC`
	rows, err := s.Pool.Query(ctx, q, teamKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.MmrPoint{}
	for rows.Next() {
		var p models.MmrPoint
		var oppA, oppB string
		if err := rows.Scan(&p.TournamentID, &p.Title, &p.Date, &p.Delta, &p.Mmr, &p.Mult,
			&p.OpponentKey, &oppA, &oppB, &p.Map); err != nil {
			return nil, err
		}
		p.Opponent = joinTeamName(oppA, oppB)
		p.Win = p.Delta > 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// TeamMaps — разбивка матчей команды по картам (взвешенно по ×2).
func (s *Store) TeamMaps(ctx context.Context, teamKey string) ([]models.MapStat, error) {
	const q = `
		SELECT COALESCE(NULLIF(r.map, ''), t.maps->>0, '') AS mp,
		       COALESCE(SUM(t.rating_multiplier) FILTER (WHERE h.delta > 0), 0)::int AS wins,
		       COALESCE(SUM(t.rating_multiplier) FILTER (WHERE h.delta < 0), 0)::int AS losses,
		       COALESCE(SUM(t.rating_multiplier), 0)::int AS games
		FROM team_mmr_history h
		JOIN tournaments t ON t.id = h.tournament_id
		LEFT JOIN LATERAL (SELECT map FROM rounds WHERE tournament_id = t.id ORDER BY number LIMIT 1) r ON true
		WHERE h.team_key = $1
		GROUP BY mp
		ORDER BY games DESC, mp`
	return s.scanMapStats(ctx, q, teamKey)
}

// TeamOpponents — head-to-head команды против других команд (взвешенно по ×2).
func (s *Store) TeamOpponents(ctx context.Context, teamKey string) ([]models.OpponentStat, error) {
	const q = `
		SELECT COALESCE(opp.team_key, '') AS opp_key,
		       COALESCE(ua.login, '') AS opp_a, COALESCE(ub.login, '') AS opp_b,
		       COALESCE(SUM(t.rating_multiplier) FILTER (WHERE h.delta > 0), 0)::int AS wins,
		       COALESCE(SUM(t.rating_multiplier) FILTER (WHERE h.delta < 0), 0)::int AS losses,
		       COALESCE(SUM(t.rating_multiplier), 0)::int AS games
		FROM team_mmr_history h
		JOIN tournaments t ON t.id = h.tournament_id
		LEFT JOIN LATERAL (
		    SELECT h2.team_key FROM team_mmr_history h2
		    WHERE h2.tournament_id = t.id AND h2.team_key <> $1 LIMIT 1
		) opp ON true
		LEFT JOIN team_mmr tmo ON tmo.team_key = opp.team_key
		LEFT JOIN users ua ON ua.id = tmo.member_a
		LEFT JOIN users ub ON ub.id = tmo.member_b
		WHERE h.team_key = $1
		GROUP BY opp_key, opp_a, opp_b
		ORDER BY games DESC`
	rows, err := s.Pool.Query(ctx, q, teamKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.OpponentStat{}
	for rows.Next() {
		var o models.OpponentStat
		var oppA, oppB string
		if err := rows.Scan(&o.TeamKey, &oppA, &oppB, &o.Wins, &o.Losses, &o.Games); err != nil {
			return nil, err
		}
		o.Name = joinTeamName(oppA, oppB)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ================== общие помощники ==================

func (s *Store) scanTimeline(ctx context.Context, q, arg string) ([]models.MmrPoint, error) {
	rows, err := s.Pool.Query(ctx, q, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.MmrPoint{}
	for rows.Next() {
		var p models.MmrPoint
		if err := rows.Scan(&p.TournamentID, &p.Title, &p.Date, &p.Delta, &p.Mmr, &p.Mult,
			&p.Opponent, &p.OpponentKey, &p.Map); err != nil {
			return nil, err
		}
		p.Win = p.Delta > 0
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) scanMapStats(ctx context.Context, q, arg string) ([]models.MapStat, error) {
	rows, err := s.Pool.Query(ctx, q, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.MapStat{}
	for rows.Next() {
		var m models.MapStat
		if err := rows.Scan(&m.Map, &m.Wins, &m.Losses, &m.Games); err != nil {
			return nil, err
		}
		if m.Map == "" {
			m.Map = "Без карты"
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// joinTeamName собирает отображаемое имя команды из логинов состава.
func joinTeamName(a, b string) string {
	parts := []string{}
	if a != "" {
		parts = append(parts, a)
	}
	if b != "" {
		parts = append(parts, b)
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " & ")
}

// PlayerStatsBundle собирает расширенную статистику 1×1 игрока (лента, сводка, карты, соперники).
func (s *Store) PlayerStatsBundle(ctx context.Context, userID string) (models.MmrStats, []models.MmrPoint, []models.MapStat, []models.OpponentStat, error) {
	timeline, err := s.Player1x1Timeline(ctx, userID)
	if err != nil {
		return models.MmrStats{}, nil, nil, nil, err
	}
	maps, err := s.Player1x1Maps(ctx, userID)
	if err != nil {
		return models.MmrStats{}, nil, nil, nil, err
	}
	opps, err := s.Player1x1Opponents(ctx, userID)
	if err != nil {
		return models.MmrStats{}, nil, nil, nil, err
	}
	stats := computeMmrStats(timeline)
	if place, err := s.Player1x1Place(ctx, userID); err == nil {
		stats.Place = place
	}
	return stats, timeline, maps, opps, nil
}

// TeamProfile собирает полную статистику команды 2×2. ok=false — команды нет.
func (s *Store) TeamProfile(ctx context.Context, teamKey string) (models.TeamProfile, bool, error) {
	members, mmr, ok, err := s.TeamMembers(ctx, teamKey)
	if err != nil || !ok {
		return models.TeamProfile{}, ok, err
	}
	timeline, err := s.TeamTimeline(ctx, teamKey)
	if err != nil {
		return models.TeamProfile{}, false, err
	}
	maps, err := s.TeamMaps(ctx, teamKey)
	if err != nil {
		return models.TeamProfile{}, false, err
	}
	opps, err := s.TeamOpponents(ctx, teamKey)
	if err != nil {
		return models.TeamProfile{}, false, err
	}
	stats := computeMmrStats(timeline)
	stats.CurrentMmr = mmr
	if place, err := s.TeamPlace(ctx, mmr); err == nil {
		stats.Place = place
	}
	return models.TeamProfile{
		TeamKey:   teamKey,
		Members:   members,
		Stats:     stats,
		Timeline:  timeline,
		Maps:      maps,
		Opponents: opps,
	}, true, nil
}
