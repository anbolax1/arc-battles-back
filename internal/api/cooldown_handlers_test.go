package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/coder/websocket"
)

// Кулдаун через HTTP: права, прямой запрос записи в обход, снятие модератором, журнал и рассылка по WebSocket.
func TestCooldownAPI(t *testing.T) {
	a := newQueueAPI(t)
	setTestCooldown(t, 180)
	boss := a.actor("boss", models.RoleSuperadmin)
	mod := a.actor("mod", models.RoleModerator)
	p, r := a.actor("p", models.RoleUser), a.actor("r", models.RoleUser)
	ctx := context.Background()
	var live int
	_ = a.st.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM tournaments WHERE status = 'live'`).Scan(&live)
	if live > 0 {
		t.Skip("в базе идёт чужой матч")
	}

	// Раздел кулдаунов - модератору и организатору; общую длительность меняет только организатор.
	a.want(actor{}, "GET", "/cooldowns", nil, 401)
	a.want(p, "GET", "/cooldowns", nil, 403)
	a.want(p, "POST", "/cooldowns/players/"+r.user.ID, map[string]any{"action": "clear"}, 403)
	a.want(mod, "GET", "/cooldowns", nil, 200)
	a.want(mod, "PUT", "/cooldowns/default", map[string]any{"minutes": 60}, 403)
	a.want(boss, "PUT", "/cooldowns/default", map[string]any{"minutes": -5}, 400)
	if ov := a.want(boss, "PUT", "/cooldowns/default", map[string]any{"minutes": 180}, 200); ov["defaultMinutes"] != float64(180) {
		t.Errorf("общая длительность: %v", ov["defaultMinutes"])
	}

	// Матч завершён в консоли - кулдаун начался у обоих игроков.
	tm, err := a.st.CreateMatch(ctx, store.NewMatch{Mode: "1x1", PlayerType: "pvp", Format: store.FormatMatch, Rounds: 1,
		Sides: [2]store.MatchSide{{UserID: p.user.ID}, {UserID: r.user.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.st.DeleteTournament(ctx, tm.ID) })
	a.want(boss, "POST", "/tournaments/"+tm.ID+"/finish", nil, 200)

	snap := a.want(boss, "POST", "/queue/sessions", map[string]any{"format": "1x1", "title": "itest кулдаун"}, 200)
	sid := sessionID(snap)

	// Прямой запрос записи в обход интерфейса - отказ с временем разблокировки.
	out := a.want(p, "POST", "/queue/sessions/"+sid+"/join", nil, 409)
	if msg, _ := out["error"].(string); !strings.Contains(msg, "Вы уже выступали") || !strings.Contains(msg, "МСК") {
		t.Errorf("отказ по кулдауну: %v", out)
	}
	me := a.want(p, "GET", "/queue?format=1x1", nil, 200)["me"].(map[string]any)
	if me["canJoin"] != false || me["cooldownUntil"] == nil {
		t.Errorf("me на кулдауне: %v", me)
	}
	if snap := a.want(actor{}, "GET", "/queue?format=1x1", nil, 200); snap["cooldownMinutes"] != float64(180) {
		t.Errorf("общий кулдаун на публичной странице: %v", snap["cooldownMinutes"])
	}

	// Подписка на изменения очереди: правка кулдауна оповещает открытые страницы.
	wsURL := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/api/ws/overlay"
	wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(wctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Свой кулдаун модератор не снимает, неизвестное действие - отказ.
	a.want(mod, "POST", "/cooldowns/players/"+mod.user.ID, map[string]any{"action": "clear"}, 403)
	a.want(mod, "POST", "/cooldowns/players/"+p.user.ID, map[string]any{"action": "forever"}, 400)
	a.want(mod, "POST", "/cooldowns/players/"+p.user.ID, map[string]any{"action": "set", "minutes": 0}, 400)
	a.want(mod, "POST", "/cooldowns/players/нет-такого", map[string]any{"action": "clear"}, 404)

	cp := a.want(mod, "POST", "/cooldowns/players/"+p.user.ID, map[string]any{"action": "clear"}, 200)
	if cp["until"] != nil || cp["todayMatches"] != float64(1) {
		t.Errorf("после снятия: %v", cp)
	}
	for {
		_, data, err := conn.Read(wctx)
		if err != nil {
			t.Fatalf("нет сообщения очереди после правки кулдауна: %v", err)
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) == nil && msg["type"] == "queue" && msg["sessionId"] == sid {
			break
		}
	}
	a.want(p, "POST", "/queue/sessions/"+sid+"/join", nil, 200)
	// У второго игрока того же матча кулдаун остался.
	a.want(r, "POST", "/queue/sessions/"+sid+"/join", nil, 409)

	// Поиск по нику и журнал правок.
	ov := a.want(mod, "GET", "/cooldowns?q="+r.user.Login, nil, 200)
	if players, _ := ov["players"].([]any); len(players) != 1 || players[0].(map[string]any)["until"] == nil {
		t.Errorf("поиск: %v", ov["players"])
	}
	var log []map[string]any
	code, raw := a.raw(mod, "GET", "/cooldowns/log?userId="+p.user.ID)
	if code != 200 || json.Unmarshal(raw, &log) != nil || len(log) == 0 || log[0]["action"] != "clear" || log[0]["actor"] != "Ник mod" {
		t.Errorf("журнал: %d %s", code, raw)
	}
}

// raw - запрос с ответом как есть: журнал отдаётся списком, а не объектом.
func (a *queueAPI) raw(who actor, method, path string) (int, []byte) {
	a.t.Helper()
	req, err := http.NewRequest(method, a.srv.URL+"/api"+path, nil)
	if err != nil {
		a.t.Fatal(err)
	}
	if who.token != "" {
		req.Header.Set("Authorization", "Bearer "+who.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}
