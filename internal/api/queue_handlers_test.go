package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/battle-for-respect/backend/internal/config"
	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/battle-for-respect/backend/internal/ws"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
)

// Очередь через HTTP: права, подмена игрока, ошибки, рассылка по WebSocket. Запуск - как у TestMMRIntegration.
type queueAPI struct {
	t      *testing.T
	st     *store.Store
	srv    *httptest.Server
	secret string
	uniq   int64
}

// lockQueueTests делает так, чтобы очередные тесты разных пакетов шли по одному: живая очередь формата в базе одна.
func lockQueueTests(t *testing.T, url string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("блокировка тестов: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(47001)`); err != nil {
		t.Fatalf("блокировка тестов: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock(47001)`)
		_ = conn.Close(ctx)
	})
}

func newQueueAPI(t *testing.T) *queueAPI {
	t.Helper()
	url := os.Getenv("RESPECT_TEST_DB")
	if url == "" {
		t.Skip("RESPECT_TEST_DB не задан")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	lockQueueTests(t, url)
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `UPDATE queue_sessions SET status = 'finished', finished_at = now() WHERE status = 'active' AND title LIKE 'itest %'`); err != nil {
		t.Fatal(err)
	}
	var foreign int
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM queue_sessions WHERE status = 'active'`).Scan(&foreign)
	if foreign > 0 {
		t.Skip("в базе есть чужая живая очередь")
	}
	cfg := config.Config{JWTSecret: "test-secret-0123456789-0123456789-ab", FrontendURL: "http://localhost:3100"}
	st := store.New(pool)
	srv := httptest.NewServer(New(cfg, st, ws.NewHub()).Router())
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE queue_sessions SET status = 'finished', finished_at = now() WHERE status = 'active' AND title LIKE 'itest %'`)
	})
	return &queueAPI{t: t, st: st, srv: srv, secret: cfg.JWTSecret, uniq: time.Now().UnixNano() % 1_000_000_000}
}

type actor struct {
	user  models.User
	token string
}

func (a *queueAPI) actor(tag string, role models.Role) actor {
	a.t.Helper()
	login := fmt.Sprintf("qa%d_%s", a.uniq, tag)
	u, err := a.st.CreateUser(context.Background(), login, "Ник "+tag, "x", role)
	if err != nil {
		a.t.Fatal(err)
	}
	tok, err := auth.IssueToken(a.secret, u.ID, string(role), time.Hour)
	if err != nil {
		a.t.Fatal(err)
	}
	return actor{user: u, token: tok}
}

// do шлёт запрос от имени актёра (zero actor - аноним) и разбирает ответ.
func (a *queueAPI) do(who actor, method, path string, body any) (int, map[string]any) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		default:
			raw, _ := json.Marshal(b)
			rd = bytes.NewReader(raw)
		}
	}
	req, err := http.NewRequest(method, a.srv.URL+"/api"+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if who.token != "" {
		req.Header.Set("Authorization", "Bearer "+who.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (a *queueAPI) want(who actor, method, path string, body any, status int) map[string]any {
	a.t.Helper()
	got, out := a.do(who, method, path, body)
	if got != status {
		a.t.Fatalf("%s %s: статус %d, want %d (%v)", method, path, got, status, out)
	}
	return out
}

func entryNames(snap map[string]any) []string {
	var out []string
	if list, ok := snap["entries"].([]any); ok {
		for _, e := range list {
			out = append(out, e.(map[string]any)["name"].(string))
		}
	}
	return out
}

func sessionID(snap map[string]any) string {
	return snap["session"].(map[string]any)["id"].(string)
}

func TestQueueAPIPermissions(t *testing.T) {
	a := newQueueAPI(t)
	boss := a.actor("boss", models.RoleSuperadmin)
	mod := a.actor("mod", models.RoleModerator)
	sub := a.actor("sub", models.RoleSubscriber)
	player := a.actor("player", models.RoleUser)
	victim := a.actor("victim", models.RoleUser)
	var anon actor

	// Очереди нет: чтение открыто всем и не падает.
	snap := a.want(anon, "GET", "/queue", nil, 200)
	if snap["session"] != nil {
		t.Fatalf("живая очередь без сессии: %v", snap["session"])
	}
	if me := snap["me"].(map[string]any); me["canJoin"] == true {
		t.Errorf("записаться в несуществующую очередь: %v", me)
	}

	// Создаёт организатор и модератор: аноним - 401, игрок и подписчик - 403.
	create := map[string]any{"title": "itest очередь", "format": "1x1"}
	a.want(anon, "POST", "/queue/sessions", create, 401)
	for _, who := range []actor{player, sub} {
		a.want(who, "POST", "/queue/sessions", create, 403)
	}
	snap = a.want(boss, "POST", "/queue/sessions", create, 200)
	sid := sessionID(snap)

	// Любое действие управления недоступно игроку и подписчику - не только создание.
	admin := []struct{ method, path string }{
		{"PATCH", "/queue/sessions/" + sid},
		{"POST", "/queue/sessions/" + sid + "/mode"},
		{"POST", "/queue/sessions/" + sid + "/finish"},
		{"POST", "/queue/sessions/" + sid + "/call"},
		{"POST", "/queue/sessions/" + sid + "/entries"},
		{"POST", "/queue/sessions/" + sid + "/entries/x/confirm"},
		{"POST", "/queue/sessions/" + sid + "/entries/x/skip"},
		{"POST", "/queue/sessions/" + sid + "/entries/x/return"},
		{"POST", "/queue/sessions/" + sid + "/entries/x/move"},
		{"DELETE", "/queue/sessions/" + sid + "/entries/x"},
		{"POST", "/queue/sessions/" + sid + "/match/start"},
		{"POST", "/queue/sessions/" + sid + "/match/finish"},
		{"POST", "/queue/sessions/" + sid + "/match/cancel"},
		{"GET", "/queue/sessions/" + sid + "/events"},
	}
	for _, r := range admin {
		a.want(anon, r.method, r.path, "{}", 401)
		for _, who := range []actor{player, sub} {
			a.want(who, r.method, r.path, "{}", 403)
		}
	}
	// Списки для ручного добавления: игроку закрыты, модератору открыты.
	for _, path := range []string{"/match-players", "/match-teams"} {
		a.want(anon, "GET", path, nil, 401)
		a.want(player, "GET", path, nil, 403)
		a.want(mod, "GET", path, nil, 200)
	}

	// Запись требует входа.
	a.want(anon, "POST", "/queue/sessions/"+sid+"/join", nil, 401)
	a.want(anon, "POST", "/queue/sessions/"+sid+"/leave", nil, 401)
	a.want(anon, "POST", "/queue/sessions/"+sid+"/confirm", nil, 401)

	// Подмена игрока: id в теле игнорируется, записывается тот, чей токен.
	snap = a.want(player, "POST", "/queue/sessions/"+sid+"/join", map[string]any{"userId": victim.user.ID, "teamKey": ""}, 200)
	if got := entryNames(snap); len(got) != 1 || got[0] != "Ник player" {
		t.Errorf("запись с подменой id: %v", got)
	}
	if me := snap["me"].(map[string]any); me["position"].(float64) != 1 {
		t.Errorf("me игрока: %v", me)
	}
	snap = a.want(victim, "GET", "/queue", nil, 200)
	if me := snap["me"].(map[string]any); me["entryId"] != nil {
		t.Errorf("жертва подмены оказалась в очереди: %v", me)
	}
	// Повторная запись - конфликт; выход чужим токеном невозможен (выходит только своя запись).
	a.want(player, "POST", "/queue/sessions/"+sid+"/join", nil, 409)
	a.want(victim, "POST", "/queue/sessions/"+sid+"/leave", nil, 404)

	// Модератор управляет очередью наравне с организатором.
	a.want(mod, "POST", "/queue/sessions/"+sid+"/mode", map[string]any{"action": "close"}, 200)
	a.want(mod, "POST", "/queue/sessions/"+sid+"/mode", map[string]any{"action": "open"}, 200)
	a.want(mod, "GET", "/queue/sessions/"+sid+"/events", nil, 200)
	a.want(mod, "POST", "/queue/sessions/"+sid+"/call", nil, 200)
	a.want(mod, "POST", "/queue/sessions/"+sid+"/match/cancel", nil, 200)
	a.want(mod, "POST", "/queue/sessions/"+sid+"/entries", map[string]any{"userId": sub.user.ID}, 200)
	a.want(mod, "POST", "/queue/sessions/"+sid+"/finish", nil, 200)
	snap = a.want(mod, "POST", "/queue/sessions", map[string]any{"title": "itest от модератора", "format": "1x1"}, 200)
	sid = sessionID(snap)
	a.want(player, "POST", "/queue/sessions/"+sid+"/join", nil, 200)

	// Организатор видит журнал и может всё то, что запрещено остальным.
	a.want(boss, "GET", "/queue/sessions/"+sid+"/events", nil, 200)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/entries", map[string]any{"userId": victim.user.ID}, 200)
	snap = a.want(anon, "GET", "/queue", nil, 200)
	if got := entryNames(snap); len(got) != 2 || got[1] != "Ник victim" {
		t.Errorf("добавление организатором: %v", got)
	}
}

func TestQueueAPIValidationAndErrors(t *testing.T) {
	a := newQueueAPI(t)
	boss := a.actor("boss", models.RoleSuperadmin)
	p1 := a.actor("p1", models.RoleUser)

	a.want(boss, "POST", "/queue/sessions", map[string]any{"format": "3x3"}, 400)
	a.want(boss, "POST", "/queue/sessions", "{нет", 400)
	a.want(boss, "POST", "/queue/sessions", map[string]any{"format": "1x1", "confirmTimeoutSec": 3}, 400)
	a.want(boss, "POST", "/queue/sessions", map[string]any{"format": "1x1", "title": strings.Repeat("я", 101)}, 400)
	snap := a.want(boss, "POST", "/queue/sessions", map[string]any{"format": "1x1", "title": "  itest   пробелы  "}, 200)
	sid := sessionID(snap)
	if title := snap["session"].(map[string]any)["title"]; title != "itest пробелы" {
		t.Errorf("название не нормализовано: %q", title)
	}
	a.want(boss, "POST", "/queue/sessions", map[string]any{"format": "1x1"}, 409) // вторая живая очередь

	a.want(p1, "GET", "/queue?format=5x5", nil, 400)
	a.want(p1, "GET", "/queue/sessions/нет-такой", nil, 404)
	a.want(p1, "POST", "/queue/sessions/нет-такой/join", nil, 404)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/mode", map[string]any{"action": "explode"}, 400)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/call", nil, 409) // звать некого
	a.want(boss, "POST", "/queue/sessions/"+sid+"/match/start", nil, 409)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/entries/нет/skip", nil, 404)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/entries", map[string]any{"userId": "нет-такого"}, 404)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/entries", map[string]any{"userId": p1.user.ID, "teamKey": "a|b"}, 400)

	// Запись закрыта - игроку отказ с понятным текстом, организатору можно.
	a.want(boss, "POST", "/queue/sessions/"+sid+"/mode", map[string]any{"action": "close"}, 200)
	_, out := a.do(p1, "POST", "/queue/sessions/"+sid+"/join", nil)
	if msg, _ := out["error"].(string); !strings.Contains(msg, "закрыта") {
		t.Errorf("текст отказа: %q", msg)
	}
	snap = a.want(p1, "GET", "/queue", nil, 200)
	if me := snap["me"].(map[string]any); me["canJoin"] == true || me["joinBlocked"] == "" {
		t.Errorf("кнопка записи при закрытой очереди: %v", me)
	}

	// Перестановка с неверной позицией и устаревшей ревизией.
	a.want(boss, "POST", "/queue/sessions/"+sid+"/mode", map[string]any{"action": "open"}, 200)
	snap = a.want(p1, "POST", "/queue/sessions/"+sid+"/join", nil, 200)
	entry := snap["entries"].([]any)[0].(map[string]any)["id"].(string)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/entries/"+entry+"/move", map[string]any{"position": 7}, 400)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/entries/"+entry+"/move", map[string]any{"position": 1, "rev": 1}, 409)

	// Частые действия одного игрока ограничиваются.
	limited := 0
	for range queueSelfLimit + 10 {
		if code, _ := a.do(p1, "POST", "/queue/sessions/"+sid+"/confirm", nil); code == 429 {
			limited++
		}
	}
	if limited == 0 {
		t.Error("спам подтверждением не ограничен")
	}

	// Завершённая сессия остаётся читаемой для всех, но не изменяется.
	other := a.actor("p2", models.RoleUser)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/finish", nil, 200)
	snap = a.want(other, "GET", "/queue/sessions/"+sid, nil, 200)
	if snap["session"].(map[string]any)["state"] != "finished" || len(entryNames(snap)) != 1 {
		t.Errorf("завершённая сессия: %v", snap["session"])
	}
	a.want(other, "POST", "/queue/sessions/"+sid+"/join", nil, 409)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/call", nil, 409)
	snap = a.want(other, "GET", "/queue", nil, 200)
	if snap["session"] != nil {
		t.Errorf("завершённая сессия показана как живая: %v", snap["session"])
	}
	list := a.want(other, "GET", "/queue/sessions", nil, 200)
	_ = list
}

// Изменения очереди приходят всем подключённым по WebSocket: ревизия растёт, порядок собирается из GET.
func TestQueueAPIRealtimeAndRestart(t *testing.T) {
	a := newQueueAPI(t)
	boss := a.actor("boss", models.RoleSuperadmin)
	p1, p2 := a.actor("p1", models.RoleUser), a.actor("p2", models.RoleUser)

	wsURL := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/api/ws/overlay"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// next ждёт следующее сообщение очереди; кадры оверлея пропускает.
	next := func() map[string]any {
		t.Helper()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				t.Fatalf("WebSocket: %v", err)
			}
			var msg map[string]any
			if json.Unmarshal(data, &msg) == nil && msg["type"] == "queue" {
				return msg
			}
		}
	}

	snap := a.want(boss, "POST", "/queue/sessions", map[string]any{"format": "1x1", "title": "itest realtime"}, 200)
	sid := sessionID(snap)
	m := next()
	if m["sessionId"] != sid || m["format"] != "1x1" {
		t.Errorf("сообщение о создании: %v", m)
	}
	last := m["rev"].(float64)

	steps := []func(){
		func() { a.want(p1, "POST", "/queue/sessions/"+sid+"/join", nil, 200) },
		func() { a.want(p2, "POST", "/queue/sessions/"+sid+"/join", nil, 200) },
		func() { a.want(boss, "POST", "/queue/sessions/"+sid+"/call", nil, 200) },
		func() { a.want(p1, "POST", "/queue/sessions/"+sid+"/confirm", nil, 200) },
	}
	for i, step := range steps {
		step()
		m := next()
		if rev := m["rev"].(float64); rev <= last {
			t.Errorf("шаг %d: ревизия %v не выросла после %v", i, rev, last)
		} else {
			last = rev
		}
		// Все видят одинаковое состояние: аноним и игроки получают тот же порядок.
		viewers := []actor{{}, p1, p2}
		var first []string
		for j, v := range viewers {
			names := entryNames(a.want(v, "GET", "/queue", nil, 200))
			if j == 0 {
				first = names
			} else if strings.Join(names, ",") != strings.Join(first, ",") {
				t.Errorf("зрители видят разный порядок: %v и %v", first, names)
			}
		}
	}
	// Неудачное действие ничего не рассылает: следующим сообщением придёт только удавшееся.
	a.want(p1, "POST", "/queue/sessions/"+sid+"/join", nil, 409)
	a.want(boss, "POST", "/queue/sessions/"+sid+"/mode", map[string]any{"action": "pause"}, 200)
	if m := next(); m["rev"].(float64) != last+1 {
		t.Errorf("после отказа пришла ревизия %v, want %v", m["rev"], last+1)
	}

	// «Перезапуск» сервера: новый экземпляр над той же базой отдаёт то же состояние.
	before := a.want(p2, "GET", "/queue", nil, 200)
	srv2 := httptest.NewServer(New(config.Config{JWTSecret: a.secret, FrontendURL: "http://localhost:3100"}, a.st, ws.NewHub()).Router())
	defer srv2.Close()
	req, _ := http.NewRequest("GET", srv2.URL+"/api/queue", nil)
	req.Header.Set("Authorization", "Bearer "+p2.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var after map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&after)
	if sessionID(after) != sid || strings.Join(entryNames(after), ",") != strings.Join(entryNames(before), ",") {
		t.Errorf("состояние после перезапуска: %v / %v", entryNames(before), entryNames(after))
	}
	if after["session"].(map[string]any)["state"] != "paused" || after["match"] == nil {
		t.Errorf("пауза и матч после перезапуска: %v", after["session"])
	}
	me := after["me"].(map[string]any)
	if me["status"] != "waiting" || me["ahead"] != float64(1) || me["before"].(map[string]any)["status"] != "ready" {
		t.Errorf("me после перезапуска: %v", me)
	}
}
