package api

import (
	"context"
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
)

// Матч очереди через HTTP: «создать матч» делает настоящий матч сайта, а завершение или отмена матча в консоли
// сами продвигают очередь.
func TestQueueAPIStartCreatesRealMatch(t *testing.T) {
	a := newQueueAPI(t)
	boss := a.actor("boss", models.RoleSuperadmin)
	p1, p2, p3 := a.actor("p1", models.RoleUser), a.actor("p2", models.RoleUser), a.actor("p3", models.RoleUser)
	pool := a.st.Pool
	var live int
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM tournaments WHERE status = 'live'`).Scan(&live)
	if live > 0 {
		t.Skip("в базе идёт чужой матч")
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tournaments WHERE id IN (
			SELECT m.tournament_id FROM queue_matches m JOIN queue_sessions s ON s.id = m.session_id WHERE s.title LIKE 'itest %')`)
	})

	snap := a.want(boss, "POST", "/queue/sessions", map[string]any{"format": "1x1", "title": "itest настоящий матч"}, 200)
	sid := sessionID(snap)
	for _, p := range []actor{p1, p2, p3} {
		a.want(p, "POST", "/queue/sessions/"+sid+"/join", nil, 200)
	}

	// Размера матча больше нет: всегда двое.
	a.want(boss, "POST", "/queue/sessions", map[string]any{"format": "2x2", "matchSize": 1, "title": "itest лишнее поле"}, 200)

	// Один вызванный - матч не собрать.
	a.want(boss, "POST", "/queue/sessions/"+sid+"/call", nil, 200)
	a.want(p1, "POST", "/queue/sessions/"+sid+"/confirm", nil, 200)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/match/start", map[string]any{"force": true}, 409)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/match/start", map[string]any{"rounds": 7}, 400)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/call", nil, 200)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/call", nil, 409) // двое уже вызваны
	a.want(boss, "POST", "/queue/sessions/"+sid+"/match/start", nil, 409)
	a.want(p2, "POST", "/queue/sessions/"+sid+"/confirm", nil, 200)

	snap = a.want(boss, "POST", "/queue/sessions/"+sid+"/match/start", map[string]any{"rounds": 3, "playerType": "pvp"}, 200)
	match := snap["match"].(map[string]any)
	tid, _ := match["tournamentId"].(string)
	if match["status"] != "live" || tid == "" {
		t.Fatalf("матч очереди после создания: %v", match)
	}

	// Настоящий матч сайта: текущий, на двоих выбранных игроков.
	cur := a.want(boss, "GET", "/matches/current", nil, 200)
	if got, _ := cur["current"].(map[string]any); got == nil || got["tournament"].(map[string]any)["id"] != tid {
		t.Fatalf("текущий матч сайта: %v", cur)
	}
	site := a.want(boss, "GET", "/tournaments/"+tid, nil, 200)
	if parts, _ := site["participants"].([]any); len(parts) != 2 || site["totalRounds"] != float64(3) || site["mode"] != "1x1" {
		t.Errorf("матч сайта: %v", site)
	}
	// Руками очередь не двигают, пока матч идёт; ещё один матч создать нельзя.
	a.want(boss, "POST", "/queue/sessions/"+sid+"/match/finish", nil, 409)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/match/cancel", nil, 409)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/match/start", nil, 409)
	call := a.want(p1, "GET", "/queue/me", nil, 200)["calls"].([]any)
	if len(call) != 1 || call[0].(map[string]any)["status"] != "performing" {
		t.Errorf("игрок в эфире: %v", call)
	}

	// Матч завершён в консоли - очередь продвинулась сама, без вызова очереди.
	a.want(boss, "POST", "/tournaments/"+tid+"/finish", nil, 200)
	snap = a.want(p3, "GET", "/queue", nil, 200)
	if got := entryNames(snap); len(got) != 3 || got[0] != "Ник p3" {
		t.Errorf("порядок после матча: %v", got)
	}
	if snap["match"] != nil || snap["last"].(map[string]any)["tournamentId"] != tid {
		t.Errorf("матч очереди после завершения: match=%v last=%v", snap["match"], snap["last"])
	}
	if me := snap["me"].(map[string]any); me["position"] != float64(1) || me["ahead"] != float64(0) {
		t.Errorf("p3 первый на вызов: %v", me)
	}
	if call := a.want(p1, "GET", "/queue/me", nil, 200)["calls"].([]any); len(call) != 0 {
		t.Errorf("вызовов у p1 больше нет: %v", call)
	}

	// Отмена матча в консоли возвращает вызванных в ожидание без «выступления».
	a.want(boss, "POST", "/queue/sessions/"+sid+"/call", nil, 200)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/call", nil, 200)
	snap = a.want(boss, "POST", "/queue/sessions/"+sid+"/match/start", map[string]any{"force": true}, 200)
	tid2 := snap["match"].(map[string]any)["tournamentId"].(string)
	a.want(boss, "POST", "/tournaments/"+tid2+"/cancel", nil, 204)
	snap = a.want(boss, "GET", "/queue", nil, 200)
	if snap["match"] != nil {
		t.Errorf("матч очереди остался после отмены: %v", snap["match"])
	}
	for _, e := range snap["entries"].([]any) {
		if e.(map[string]any)["status"] != "waiting" {
			t.Errorf("после отмены: %v", e)
		}
	}
	// Отменённый матч не засчитан: p3 всё ещё не выступал, а p1 и p2 выступили один раз.
	for _, e := range snap["entries"].([]any) {
		m := e.(map[string]any)
		want := float64(1)
		if m["name"] == "Ник p3" {
			want = 0
		}
		if m["performedCount"] != want {
			t.Errorf("%v выступлений %v, want %v", m["name"], m["performedCount"], want)
		}
	}
}
