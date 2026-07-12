package store

import (
	"context"

	"github.com/battle-for-respect/backend/internal/models"
)

// Leaderboard агрегирует сезонный рейтинг 1×1 по игрокам. В рейтинг идут ТОЛЬКО завершённые
// турниры (status='finished'). MMR — сквозной по сезонам (старт 1000); сезонный фильтр влияет на
// состав игроков и агрегаты wins/games/points. wins и games учитывают жетон ×2 (матч = 2).
//
//	seasonID="" — за всё время; иначе только турниры этого сезона.
func (s *Store) Leaderboard(ctx context.Context, mode, seasonID string) ([]models.LeaderboardRow, error) {
	const q = `
		SELECT u.id, u.login, u.display_name, u.avatar_url,
		       COALESCE(um.mmr, 1000)::int AS mmr,
		       COALESCE(SUM(p.total_points), 0)::int AS points,
		       COALESCE(SUM(CASE WHEN t.winner_participant_id = p.id THEN t.rating_multiplier ELSE 0 END), 0)::int AS wins,
		       COALESCE(SUM(t.rating_multiplier), 0)::int AS games
		FROM participants p
		JOIN tournaments t ON t.id = p.tournament_id AND t.mode = '1x1' AND t.status = 'finished'
		   AND ($1 = '' OR t.season_id = $1)
		JOIN users u ON u.id = p.user_id
		LEFT JOIN user_mmr um ON um.user_id = u.id AND um.mode = '1x1'
		WHERE p.kind = 'player'
		GROUP BY u.id, u.login, u.display_name, u.avatar_url, um.mmr
		ORDER BY mmr DESC, wins DESC`

	rows, err := s.Pool.Query(ctx, q, seasonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.LeaderboardRow{}
	for rows.Next() {
		var r models.LeaderboardRow
		if err := rows.Scan(&r.UserID, &r.Login, &r.DisplayName, &r.AvatarURL, &r.Mmr, &r.Points, &r.Wins, &r.Tournaments); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TeamLeaderboard агрегирует сезонный рейтинг 2×2 по КОМАНДАМ (пара игроков = команда с одним
// MMR, ключ — пара userId). MMR — сквозной по сезонам (team_mmr, старт 1000). wins/losses/games
// учитывают жетон ×2 (матч = 2) и считаются по завершённым матчам (сезонный фильтр — по ним же).
// Показываются только команды, сыгравшие хотя бы один завершённый матч (в сезоне, если задан).
func (s *Store) TeamLeaderboard(ctx context.Context, seasonID string) ([]models.TeamLeaderboardRow, error) {
	const q = `
		SELECT tm.team_key, tm.mmr,
		       COALESCE(SUM(CASE WHEN h.delta > 0 THEN t.rating_multiplier ELSE 0 END), 0)::int AS wins,
		       COALESCE(SUM(CASE WHEN h.delta < 0 THEN t.rating_multiplier ELSE 0 END), 0)::int AS losses,
		       ua.id, ua.login, ua.display_name, ua.avatar_url,
		       ub.id, ub.login, ub.display_name, ub.avatar_url
		FROM team_mmr tm
		JOIN team_mmr_history h ON h.team_key = tm.team_key
		JOIN tournaments t ON t.id = h.tournament_id AND t.status = 'finished'
		   AND ($1 = '' OR t.season_id = $1)
		JOIN users ua ON ua.id = tm.member_a
		JOIN users ub ON ub.id = tm.member_b
		GROUP BY tm.team_key, tm.mmr, ua.id, ua.login, ua.display_name, ua.avatar_url,
		         ub.id, ub.login, ub.display_name, ub.avatar_url
		ORDER BY tm.mmr DESC, wins DESC`

	rows, err := s.Pool.Query(ctx, q, seasonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.TeamLeaderboardRow{}
	for rows.Next() {
		var r models.TeamLeaderboardRow
		var a, b models.TeamMember
		if err := rows.Scan(&r.TeamKey, &r.Mmr, &r.Wins, &r.Losses,
			&a.UserID, &a.Login, &a.DisplayName, &a.AvatarURL,
			&b.UserID, &b.Login, &b.DisplayName, &b.AvatarURL); err != nil {
			return nil, err
		}
		r.Games = r.Wins + r.Losses
		r.Members = []models.TeamMember{a, b}
		out = append(out, r)
	}
	return out, rows.Err()
}
