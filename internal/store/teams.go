package store

import (
	"context"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrTeamExists - у этой пары игроков уже есть команда: MMR считается по паре, две команды делили бы один рейтинг.
	ErrTeamExists = errors.New("такая команда уже есть")
	// ErrTeamPlayed - у команды есть матчи, хотя бы запланированный: её состав и она сама остаются такими, как в них.
	ErrTeamPlayed = errors.New("у команды уже есть матчи")
)

// teamPair - ключ команды 2×2 и её игроки по порядку ключа: команда - это пара, порядок выбора не важен.
func teamPair(a, b string) (key, first, second string) {
	if b < a {
		a, b = b, a
	}
	return a + "|" + b, a, b
}

// TeamKey - ключ команды 2×2 по её игрокам, как в рейтинге команд.
func TeamKey(a, b string) string {
	key, _, _ := teamPair(a, b)
	return key
}

// teamInMatch - сторона матча 2×2 с обоими игроками команды tt.
const teamInMatch = `p.members @> jsonb_build_array(jsonb_build_object('userId', tt.member_a), jsonb_build_object('userId', tt.member_b))`

// ListMatchTeams - команды для выбора сторон: недавно заведённые и игравшие сверху.
func (s *Store) ListMatchTeams(ctx context.Context) ([]models.MatchTeam, error) {
	return s.matchTeams(ctx, "")
}

// GetMatchTeam - команда из списка организатора.
func (s *Store) GetMatchTeam(ctx context.Context, key string) (models.MatchTeam, error) {
	teams, err := s.matchTeams(ctx, key)
	if err != nil {
		return models.MatchTeam{}, err
	}
	if len(teams) == 0 {
		return models.MatchTeam{}, ErrNotFound
	}
	return teams[0], nil
}

// matchTeams - команды с MMR и счётом текущего сезона; key - одна команда, пусто - все.
func (s *Store) matchTeams(ctx context.Context, key string) ([]models.MatchTeam, error) {
	rule := s.activeSeasonRule(ctx)
	rows, err := s.Pool.Query(ctx, `
		WITH season AS (
			SELECT h.team_key, SUM(h.delta) AS total,
			       SUM(CASE WHEN h.delta > 0 AND NOT t.voided THEN t.games ELSE 0 END) AS wins,
			       SUM(CASE WHEN h.delta < 0 AND NOT t.voided THEN t.games ELSE 0 END) AS losses
			FROM team_mmr_history h LEFT JOIN tournaments t ON t.id = h.tournament_id
			WHERE h.season_key = $1
			GROUP BY h.team_key
		), prev AS (
			SELECT team_key, COUNT(DISTINCT tournament_id) AS games
			FROM team_mmr_history WHERE season_key <> $1
			GROUP BY team_key
		), played AS (
			SELECT tt.team_key, COUNT(*) AS matches, MAX(COALESCE(t.starts_at, t.created_at)) AS last_at
			FROM teams tt
			JOIN participants p ON `+teamInMatch+`
			JOIN tournaments t ON t.id = p.tournament_id AND t.mode = '2x2'
			GROUP BY tt.team_key
		)
		SELECT tt.team_key, tt.name, tt.created_at,
		       ($2::int + COALESCE(season.total, 0))::int, COALESCE(season.wins, 0)::int, COALESCE(season.losses, 0)::int,
		       season.team_key IS NULL, COALESCE(prev.games, 0)::int, COALESCE(played.matches, 0)::int, played.last_at,
		       ua.id, ua.login, ua.display_name, ua.avatar_url, ub.id, ub.login, ub.display_name, ub.avatar_url
		FROM teams tt
		JOIN users ua ON ua.id = tt.member_a
		JOIN users ub ON ub.id = tt.member_b
		LEFT JOIN season ON season.team_key = tt.team_key
		LEFT JOIN prev ON prev.team_key = tt.team_key
		LEFT JOIN played ON played.team_key = tt.team_key
		WHERE $3 = '' OR tt.team_key = $3
		ORDER BY GREATEST(tt.created_at, COALESCE(played.last_at, tt.created_at)) DESC, lower(tt.name)`,
		rule.Key, rule.Start, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.MatchTeam{}
	for rows.Next() {
		var t models.MatchTeam
		var a, b models.TeamMember
		if err := rows.Scan(&t.TeamKey, &t.Name, &t.CreatedAt, &t.Mmr, &t.Wins, &t.Losses, &t.IsNew, &t.PrevGames,
			&t.Matches, &t.LastMatchAt,
			&a.UserID, &a.Login, &a.DisplayName, &a.AvatarURL, &b.UserID, &b.Login, &b.DisplayName, &b.AvatarURL); err != nil {
			return nil, err
		}
		t.Members = []models.TeamMember{a, b}
		out = append(out, t)
	}
	return out, rows.Err()
}

// teamPlayed - есть ли у пары матч 2×2, в том числе идущий или запланированный.
func teamPlayed(ctx context.Context, tx pgx.Tx, a, b string) (bool, error) {
	var played bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM participants p JOIN tournaments t ON t.id = p.tournament_id
		               WHERE t.mode = '2x2'
		                 AND p.members @> jsonb_build_array(jsonb_build_object('userId', $1::text), jsonb_build_object('userId', $2::text)))`,
		a, b).Scan(&played)
	return played, err
}

func (s *Store) checkTeamPlayers(ctx context.Context, a, b string) error {
	if a == "" || b == "" || a == b {
		return ErrConflict
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id IN ($1, $2)`, a, b).Scan(&n); err != nil {
		return err
	}
	if n != 2 {
		return ErrNotFound
	}
	return nil
}

// CreateTeam заводит команду из двух игроков.
func (s *Store) CreateTeam(ctx context.Context, name, a, b string) (models.MatchTeam, error) {
	if err := s.checkTeamPlayers(ctx, a, b); err != nil {
		return models.MatchTeam{}, err
	}
	key, first, second := teamPair(a, b)
	ct, err := s.Pool.Exec(ctx, `
		INSERT INTO teams (team_key, member_a, member_b, name) VALUES ($1, $2, $3, $4)
		ON CONFLICT (team_key) DO NOTHING`, key, first, second, name)
	if err != nil {
		return models.MatchTeam{}, err
	}
	if ct.RowsAffected() == 0 {
		return models.MatchTeam{}, ErrTeamExists
	}
	// Пара могла играть и до списка команд: рейтинг и страница команды показывают название из списка.
	if _, err := s.Pool.Exec(ctx, `UPDATE team_mmr SET name = $2 WHERE team_key = $1`, key, name); err != nil {
		return models.MatchTeam{}, err
	}
	return s.GetMatchTeam(ctx, key)
}

// UpdateTeam переименовывает команду и меняет её состав, пока у неё нет матчей. Ещё не сыгранные матчи команды
// идут под новым названием, сыгранные остаются с тем, под которым играли.
func (s *Store) UpdateTeam(ctx context.Context, key, name string, members []string) (team models.MatchTeam, renamed []string, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return team, nil, err
	}
	defer tx.Rollback(ctx)

	var a, b, was string
	err = tx.QueryRow(ctx, `SELECT member_a, member_b, name FROM teams WHERE team_key = $1 FOR UPDATE`, key).Scan(&a, &b, &was)
	if errors.Is(err, pgx.ErrNoRows) {
		return team, nil, ErrNotFound
	}
	if err != nil {
		return team, nil, err
	}
	if len(members) == 2 {
		if err := s.checkTeamPlayers(ctx, members[0], members[1]); err != nil {
			return team, nil, err
		}
		if next, first, second := teamPair(members[0], members[1]); next != key {
			if played, err := teamPlayed(ctx, tx, a, b); err != nil {
				return team, nil, err
			} else if played {
				return team, nil, ErrTeamPlayed
			}
			ct, err := tx.Exec(ctx, `
				UPDATE teams SET team_key = $2, member_a = $3, member_b = $4, updated_at = now()
				WHERE team_key = $1 AND NOT EXISTS (SELECT 1 FROM teams WHERE team_key = $2)`, key, next, first, second)
			if err != nil {
				return team, nil, err
			}
			if ct.RowsAffected() == 0 {
				return team, nil, ErrTeamExists
			}
			key, a, b = next, first, second
		}
	}
	if name != was {
		if _, err := tx.Exec(ctx, `UPDATE teams SET name = $2, updated_at = now() WHERE team_key = $1`, key, name); err != nil {
			return team, nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE team_mmr SET name = $2 WHERE team_key = $1`, key, name); err != nil {
			return team, nil, err
		}
		if renamed, err = renameComingMatches(ctx, tx, a, b, name); err != nil {
			return team, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return team, nil, err
	}
	team, err = s.GetMatchTeam(ctx, key)
	return team, renamed, err
}

// renameComingMatches переименовывает сторону пары в идущем и запланированных матчах. Название матча собрано
// из названий сторон - его собираем заново, написанное вручную не трогаем.
func renameComingMatches(ctx context.Context, tx pgx.Tx, a, b, name string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.tournament_id FROM participants p JOIN tournaments t ON t.id = p.tournament_id
		WHERE t.mode = '2x2' AND t.status IN ('upcoming', 'live')
		  AND p.members @> jsonb_build_array(jsonb_build_object('userId', $1::text), jsonb_build_object('userId', $2::text))`, a, b)
	if err != nil {
		return nil, err
	}
	type side struct{ participant, tournament string }
	var sides []side
	for rows.Next() {
		var sd side
		if err := rows.Scan(&sd.participant, &sd.tournament); err != nil {
			rows.Close()
			return nil, err
		}
		sides = append(sides, sd)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(sides))
	for _, sd := range sides {
		before, err := sideNamesVs(ctx, tx, sd.tournament)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE participants SET name = $2 WHERE id = $1`, sd.participant, name); err != nil {
			return nil, err
		}
		after, err := sideNamesVs(ctx, tx, sd.tournament)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE tournaments SET title = CASE WHEN title = $2 THEN $3 ELSE title END, updated_at = now() WHERE id = $1`,
			sd.tournament, before, after); err != nil {
			return nil, err
		}
		ids = append(ids, sd.tournament)
	}
	return ids, nil
}

// sideNamesVs - название матча из названий сторон по порядку: «A vs B».
func sideNamesVs(ctx context.Context, tx pgx.Tx, tournamentID string) (string, error) {
	var title string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(string_agg(name, ' vs ' ORDER BY seed, id), '') FROM participants WHERE tournament_id = $1`,
		tournamentID).Scan(&title)
	return title, err
}

// DeleteTeam убирает команду из списка, пока у неё нет матчей.
func (s *Store) DeleteTeam(ctx context.Context, key string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var a, b string
	err = tx.QueryRow(ctx, `SELECT member_a, member_b FROM teams WHERE team_key = $1 FOR UPDATE`, key).Scan(&a, &b)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if played, err := teamPlayed(ctx, tx, a, b); err != nil {
		return err
	} else if played {
		return ErrTeamPlayed
	}
	if _, err := tx.Exec(ctx, `DELETE FROM teams WHERE team_key = $1`, key); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
