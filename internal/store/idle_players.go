package store

import (
	"context"

	"github.com/battle-for-respect/backend/internal/models"
)

// IdlePlayers - игроки, которые давно не играли: сайт заносит снегом их ники и профили.
// Считаются завершённые матчи любого формата (в 2×2 - по составу); игрок без матчей не попадает.
func (s *Store) IdlePlayers(ctx context.Context, minDays int) ([]models.IdlePlayer, error) {
	const q = `
		WITH played AS (
		    SELECT p.user_id AS uid, COALESCE(t.starts_at, t.created_at) AS at
		      FROM participants p
		      JOIN tournaments t ON t.id = p.tournament_id AND t.status = 'finished'
		     WHERE p.user_id IS NOT NULL
		    UNION ALL
		    SELECT m->>'userId', COALESCE(t.starts_at, t.created_at)
		      FROM participants p
		      JOIN tournaments t ON t.id = p.tournament_id AND t.status = 'finished'
		      CROSS JOIN LATERAL jsonb_array_elements(
		          CASE WHEN jsonb_typeof(p.members) = 'array' THEN p.members ELSE '[]'::jsonb END) m
		)
		SELECT u.login, FLOOR(EXTRACT(EPOCH FROM now() - MAX(played.at)) / 86400)::int AS days
		  FROM played
		  JOIN users u ON u.id = played.uid
		 GROUP BY u.login
		HAVING MAX(played.at) <= now() - make_interval(days => $1)
		 ORDER BY days DESC`
	rows, err := s.Pool.Query(ctx, q, minDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.IdlePlayer{}
	for rows.Next() {
		var p models.IdlePlayer
		if err := rows.Scan(&p.Login, &p.Days); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
