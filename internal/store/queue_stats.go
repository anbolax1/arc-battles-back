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

// entrySummary - запись очереди по её игрокам: у команды матчей сегодня и последний матч - как у самого
// игравшего из двух, а кулдаун идёт, пока не кончится у обоих.
type entrySummary struct {
	Today    int
	Last     time.Time
	Cooldown time.Time // нулевое - ни у кого кулдауна нет
}

func summarize(players map[string]queuePlayer, users []string) entrySummary {
	var s entrySummary
	for _, u := range users {
		p := players[u]
		s.Today = max(s.Today, p.History.Today)
		if p.History.Last.After(s.Last) {
			s.Last = p.History.Last
		}
		if p.Cooldown.After(s.Cooldown) {
			s.Cooldown = p.Cooldown
		}
	}
	return s
}

// playersInMatch - кто из игроков сейчас в матче: играет в идущем матче сайта или вызван на матч в другой
// живой очереди. Такой записывается в очередь только после матча.
func playersInMatch(ctx context.Context, db queryer, userIDs []string, exceptSession string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `
		SELECT u.id FROM unnest($1::text[]) AS u(id)
		WHERE EXISTS (
		        SELECT 1 FROM participants p JOIN tournaments t ON t.id = p.tournament_id AND t.status = 'live'
		        WHERE p.user_id = u.id OR p.members @> jsonb_build_array(jsonb_build_object('userId', u.id)))
		   OR EXISTS (
		        SELECT 1 FROM queue_entry_members m
		        JOIN queue_entries e ON e.id = m.entry_id AND e.status IN ('invited', 'ready', 'performing')
		        JOIN queue_sessions s ON s.id = m.session_id AND s.status = 'active'
		        WHERE m.user_id = u.id AND m.active AND m.session_id <> $2)`, userIDs, exceptSession)
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

// inMatchBlock - почему игроков нельзя записать, пока они в матче; пусто - никто не в матче.
func inMatchBlock(inMatch map[string]bool, names map[string]string, members []string, viewer string, organizer bool) string {
	for _, u := range members {
		if !inMatch[u] {
			continue
		}
		if u == viewer && !organizer {
			return "Вы сейчас в матче - встать в очередь можно после него"
		}
		return fmt.Sprintf("Игрок %s сейчас в матче - записать можно после него", names[u])
	}
	return ""
}

// userLabels - ники игроков для сообщений.
func userLabels(ctx context.Context, db queryer, userIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `SELECT id, COALESCE(NULLIF(display_name, ''), login) FROM users WHERE id = ANY($1)`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
