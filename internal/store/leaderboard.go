package store

import (
	"context"

	"github.com/battle-for-respect/backend/internal/models"
)

// Leaderboard агрегирует сезонный рейтинг по игрокам для режима 1x1 или 2x2.
// В рейтинг идут ТОЛЬКО завершённые турниры (status='finished'): баллы турнира
// засчитываются в сезон после его окончания, а не после каждого раунда.
//   - 1x1: участники kind='player', привязанные к пользователю.
//   - 2x2: баллы команды распределяются на участников состава (members[].userId).
//
// seasonID="" — рейтинг за всё время (без фильтра); иначе только турниры этого сезона.
func (s *Store) Leaderboard(ctx context.Context, mode, seasonID string) ([]models.LeaderboardRow, error) {
	var q string
	if mode == "2x2" {
		q = `
			SELECT u.id, u.login, u.display_name, u.avatar_url,
			       COALESCE(SUM(p.total_points), 0)::int AS points,
			       COUNT(DISTINCT CASE WHEN t.winner_participant_id = p.id THEN t.id END)::int AS wins,
			       COUNT(DISTINCT p.tournament_id)::int AS tournaments
			FROM participants p
			JOIN tournaments t ON t.id = p.tournament_id AND t.mode = '2x2' AND t.status = 'finished'
			   AND ($1 = '' OR t.season_id = $1)
			JOIN LATERAL jsonb_array_elements(p.members) m ON true
			JOIN users u ON u.id = (m->>'userId')
			GROUP BY u.id, u.login, u.display_name, u.avatar_url
			ORDER BY points DESC, wins DESC`
	} else {
		q = `
			SELECT u.id, u.login, u.display_name, u.avatar_url,
			       COALESCE(SUM(p.total_points), 0)::int AS points,
			       COUNT(DISTINCT CASE WHEN t.winner_participant_id = p.id THEN t.id END)::int AS wins,
			       COUNT(DISTINCT p.tournament_id)::int AS tournaments
			FROM participants p
			JOIN tournaments t ON t.id = p.tournament_id AND t.mode = '1x1' AND t.status = 'finished'
			   AND ($1 = '' OR t.season_id = $1)
			JOIN users u ON u.id = p.user_id
			WHERE p.kind = 'player'
			GROUP BY u.id, u.login, u.display_name, u.avatar_url
			ORDER BY points DESC, wins DESC`
	}

	rows, err := s.Pool.Query(ctx, q, seasonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.LeaderboardRow{}
	for rows.Next() {
		var r models.LeaderboardRow
		if err := rows.Scan(&r.UserID, &r.Login, &r.DisplayName, &r.AvatarURL, &r.Points, &r.Wins, &r.Tournaments); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
