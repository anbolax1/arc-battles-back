package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Чтобы встать в очередь, игрок заполняет в профиле часы и ноки со скриншотом, а модератор их подтверждает:
// ведущий видит эти числа рядом с игроком, и по ним понятно, с кем он играет.
var queueStatKinds = []struct{ Kind, Label string }{
	{StatHours, "часы"},
	{StatKnocks, "ноки"},
}

// queueStatGaps - чего не хватает игрокам: для каждого список вида «ноки не заполнены». Пусто - всё подтверждено.
// Подтверждённое число остаётся в силе, даже если игрок прислал новое на проверку.
func queueStatGaps(ctx context.Context, db queryer, userIDs []string) (map[string][]string, error) {
	rows, err := db.Query(ctx, `
		SELECT user_id, kind, bool_or(status = 'approved'), (array_agg(status ORDER BY created_at DESC))[1]
		FROM stat_claims WHERE user_id = ANY($1) AND kind IN ('hours', 'knocks')
		GROUP BY user_id, kind`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ user, kind string }
	approved := map[key]bool{}
	latest := map[key]string{}
	for rows.Next() {
		var k key
		var ok bool
		var last string
		if err := rows.Scan(&k.user, &k.kind, &ok, &last); err != nil {
			return nil, err
		}
		approved[k], latest[k] = ok, last
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, u := range userIDs {
		for _, sk := range queueStatKinds {
			k := key{u, sk.Kind}
			if approved[k] {
				continue
			}
			switch latest[k] {
			case "pending":
				out[u] = append(out[u], sk.Label+" ждут проверки модератора")
			case "rejected":
				out[u] = append(out[u], sk.Label+" отклонены модератором")
			default:
				out[u] = append(out[u], sk.Label+" не заполнены")
			}
		}
	}
	return out, rows.Err()
}

// queueStatsBlock - почему этим игрокам нельзя встать в очередь: текст для того, кто записывается (viewer).
// Пусто - у всех подтверждены и часы, и ноки.
func queueStatsBlock(ctx context.Context, db queryer, viewer string, members []string) (string, error) {
	gaps, err := queueStatGaps(ctx, db, members)
	if err != nil {
		return "", err
	}
	for _, u := range members {
		g := gaps[u]
		if len(g) == 0 {
			continue
		}
		if u == viewer {
			return "Чтобы встать в очередь, сначала заполните в профиле часы и ноки со скриншотом и дождитесь подтверждения модератора: " +
				strings.Join(g, ", ") + ".", nil
		}
		var name string
		if err := db.QueryRow(ctx, `SELECT COALESCE(NULLIF(display_name, ''), login) FROM users WHERE id = $1`, u).Scan(&name); err != nil {
			return "", err
		}
		return fmt.Sprintf("У игрока %s не подтверждены часы и ноки (%s): пусть заполнит их в профиле.", name, strings.Join(g, ", ")), nil
	}
	return "", nil
}

// queueTodayMatches - сколько завершённых матчей сайта игрок сыграл сегодня (день по Москве). Считаются все матчи,
// не только из очереди, и любые форматы: игрок, который уже выходил в эфир сегодня, пропускает вперёд остальных.
func queueTodayMatches(ctx context.Context, db queryer, userIDs []string) (map[string]int, error) {
	out := map[string]int{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `
		SELECT u.id, COUNT(DISTINCT t.id)
		FROM users u
		JOIN participants p ON p.user_id = u.id OR p.members @> jsonb_build_array(jsonb_build_object('userId', u.id))
		JOIN tournaments t ON t.id = p.tournament_id
		WHERE u.id = ANY($1) AND t.status = 'finished' AND COALESCE(t.starts_at, t.created_at) >= $2
		GROUP BY u.id`, userIDs, mskDay(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// maxToday - у записи столько матчей за сегодня, сколько у самого игравшего из её игроков.
func maxToday(today map[string]int, users []string) int {
	best := 0
	for _, u := range users {
		if today[u] > best {
			best = today[u]
		}
	}
	return best
}
