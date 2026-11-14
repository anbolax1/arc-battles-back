package store

import (
	"context"
	"time"
)

// matchMomentSQL - когда матч сыгран: время завершения, у старых матчей без него - время начала.
// Ждёт в запросе алиас t (турнир).
const matchMomentSQL = `COALESCE(t.finished_at, t.starts_at, t.created_at)`

// playerHistory - выступления игрока по завершённым матчам сайта, любых форматов и сессий.
type playerHistory struct {
	Total int       // матчей за всё время
	Today int       // из них сегодня, день по Москве
	Last  time.Time // когда сыгран последний; нулевое - матчей нет
	// Unknown - своих матчей нет, но игрок в лиге уже был: матч под его ником без привязки к аккаунту
	// или сверка рейтинга по официальной таблице. Такого нельзя показывать новичком.
	Unknown bool
}

// Newbie - ни одного завершённого матча и никаких следов игры в лиге.
func (h playerHistory) Newbie() bool { return h.Total == 0 && !h.Unknown }

// LastPtr - время последнего матча для ответа API; нет матчей - пусто.
func (h playerHistory) LastPtr() *time.Time {
	if h.Last.IsZero() {
		return nil
	}
	t := h.Last
	return &t
}

// playerHistories - история выступлений игроков одним запросом на всех. withUnknown - проверить ещё и
// следы игры у тех, у кого своих матчей нет: это нужно только для показа «Новичок».
func playerHistories(ctx context.Context, db queryer, userIDs []string, now time.Time, withUnknown bool) (map[string]playerHistory, error) {
	out := make(map[string]playerHistory, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	// В 1×1 игрок записан в user_id, в 2×2 - в составе members: две ветки, чтобы работали оба индекса.
	rows, err := db.Query(ctx, `
		WITH u AS (SELECT DISTINCT unnest($1::text[]) AS id),
		played AS (
			SELECT u.id AS uid, t.id AS tid, `+matchMomentSQL+` AS at
			FROM u JOIN participants p ON p.user_id = u.id
			JOIN tournaments t ON t.id = p.tournament_id AND t.status = 'finished'
			UNION
			SELECT u.id, t.id, `+matchMomentSQL+`
			FROM u JOIN participants p ON p.members @> jsonb_build_array(jsonb_build_object('userId', u.id))
			JOIN tournaments t ON t.id = p.tournament_id AND t.status = 'finished'
		)
		SELECT uid, COUNT(*)::int, (COUNT(*) FILTER (WHERE at >= $2))::int, MAX(at)
		FROM played GROUP BY uid`, userIDs, mskDay(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var h playerHistory
		if err := rows.Scan(&id, &h.Total, &h.Today, &h.Last); err != nil {
			return nil, err
		}
		out[id] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close() // дальше запрос на том же соединении
	if !withUnknown {
		return out, nil
	}
	var none []string
	for _, id := range userIDs {
		if out[id].Total == 0 {
			none = append(none, id)
		}
	}
	unknown, err := playedUnlinked(ctx, db, none)
	if err != nil {
		return nil, err
	}
	for id := range unknown {
		h := out[id]
		h.Unknown = true
		out[id] = h
	}
	return out, nil
}

// playedUnlinked - у кого из игроков без своих матчей есть следы игры в лиге: завершённый матч под его ником
// (нынешним, прежним или именем на сайте), не привязанный ни к какому аккаунту, или сверка рейтинга.
func playedUnlinked(ctx context.Context, db queryer, userIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `
		WITH names AS (
			SELECT id AS uid, lower(login) AS name FROM users WHERE id = ANY($1)
			UNION SELECT id, lower(display_name) FROM users WHERE id = ANY($1) AND display_name <> ''
			UNION SELECT user_id, lower(alias) FROM user_login_aliases WHERE user_id = ANY($1)
		)
		SELECT DISTINCT n.uid FROM names n
		WHERE EXISTS (
		        SELECT 1 FROM participants p JOIN tournaments t ON t.id = p.tournament_id
		        WHERE t.status = 'finished' AND t.mode = '1x1' AND p.user_id IS NULL AND lower(p.name) = n.name)
		   OR EXISTS (
		        SELECT 1 FROM participants p JOIN tournaments t ON t.id = p.tournament_id,
		             jsonb_array_elements(CASE WHEN jsonb_typeof(p.members) = 'array' THEN p.members ELSE '[]'::jsonb END) m
		        WHERE t.status = 'finished' AND COALESCE(m ->> 'userId', '') = '' AND lower(m ->> 'name') = n.name)
		   OR EXISTS (SELECT 1 FROM mmr_corrections c WHERE c.user_id = n.uid)`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
