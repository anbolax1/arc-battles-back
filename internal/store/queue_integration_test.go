package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Очередь выступлений на живой базе. Запуск - как у TestMMRIntegration (RESPECT_TEST_DB).
// Тесты закрывают свои сессии сами; чужую живую очередь того же формата не трогают и пропускаются.
type queueEnv struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	st   *store.Store
	uniq int64
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

func newQueueEnv(t *testing.T) *queueEnv {
	t.Helper()
	url := os.Getenv("RESPECT_TEST_DB")
	if url == "" {
		t.Skip("RESPECT_TEST_DB не задан")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	lockQueueTests(t, url)
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(pool.Close)
	e := &queueEnv{t: t, ctx: ctx, pool: pool, st: store.New(pool), uniq: time.Now().UnixNano()}
	// Хвосты упавших прогонов закрываем; живая очередь не наша - пропускаем тест.
	if _, err := pool.Exec(ctx, `UPDATE queue_sessions SET status = 'finished', finished_at = now() WHERE status = 'active' AND title LIKE 'itest %'`); err != nil {
		t.Fatal(err)
	}
	var foreign int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM queue_sessions WHERE status = 'active'`).Scan(&foreign)
	if foreign > 0 {
		t.Skip("в базе есть чужая живая очередь")
	}
	return e
}

func (e *queueEnv) user(tag string) models.User {
	e.t.Helper()
	login := fmt.Sprintf("q%d_%s", e.uniq%1_000_000_000, tag)
	u, err := e.st.CreateUser(e.ctx, login, "Ник "+tag, "x", models.RoleUser)
	if err != nil {
		e.t.Fatalf("CreateUser(%s): %v", tag, err)
	}
	return u
}

func (e *queueEnv) session(format string) string {
	e.t.Helper()
	res, err := e.st.CreateQueueSession(e.ctx, "", store.QueueSessionInput{Title: "itest " + format, Format: format})
	if err != nil {
		e.t.Fatalf("CreateQueueSession(%s): %v", format, err)
	}
	e.t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `UPDATE queue_sessions SET status = 'finished', finished_at = now() WHERE id = $1`, res.SessionID)
	})
	return res.SessionID
}

func (e *queueEnv) join(sid string, u models.User) store.QueueResult {
	e.t.Helper()
	res, err := e.st.JoinQueue(e.ctx, sid, u.ID, store.QueueJoin{UserID: u.ID})
	if err != nil {
		e.t.Fatalf("JoinQueue(%s): %v", u.Login, err)
	}
	return res
}

func (e *queueEnv) snap(sid, viewer string) *models.QueueSnapshot {
	e.t.Helper()
	s, err := e.st.QueueSnapshotFor(e.ctx, sid, viewer)
	if err != nil {
		e.t.Fatalf("QueueSnapshotFor: %v", err)
	}
	return s
}

// order - ники записей по позициям; заодно проверяет, что позиции идут 1..N без пропусков и дублей.
func (e *queueEnv) order(sid string) []string {
	e.t.Helper()
	s := e.snap(sid, "")
	out := make([]string, len(s.Entries))
	for i, en := range s.Entries {
		if en.Position != i+1 {
			e.t.Fatalf("позиции сбиты: у %q позиция %d, ожидалась %d", en.Name, en.Position, i+1)
		}
		out[i] = en.Name
	}
	return out
}

func (e *queueEnv) status(sid, name string) string {
	e.t.Helper()
	for _, en := range e.snap(sid, "").Entries {
		if en.Name == name {
			return en.Status
		}
	}
	return ""
}

func wantOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("порядок %v, want %v", got, want)
	}
}

func wantKind(t *testing.T, err error, kind error) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Errorf("ошибка %v, want %v", err, kind)
	}
}

func TestQueueJoinLeaveAndDuplicates(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	a, b, c := e.user("a"), e.user("b"), e.user("c")

	e.join(sid, a)
	e.join(sid, b)
	wantOrder(t, e.order(sid), "Ник a", "Ник b")

	// Повторная запись невозможна, очередь не меняется.
	_, err := e.st.JoinQueue(e.ctx, sid, a.ID, store.QueueJoin{UserID: a.ID})
	wantKind(t, err, store.ErrQueueState)
	wantOrder(t, e.order(sid), "Ник a", "Ник b")

	// Выход и повторная запись - в конец.
	if _, err := e.st.LeaveQueue(e.ctx, sid, a.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.st.LeaveQueue(e.ctx, sid, a.ID)
	wantKind(t, err, store.ErrNotFound)
	e.join(sid, c)
	e.join(sid, a)
	wantOrder(t, e.order(sid), "Ник b", "Ник c", "Ник a")

	// Неизвестный игрок и команда в очереди 1×1.
	_, err = e.st.JoinQueue(e.ctx, sid, "", store.QueueJoin{UserID: "no-such-user", ByOrganizer: true})
	wantKind(t, err, store.ErrNotFound)
	_, err = e.st.JoinQueue(e.ctx, sid, b.ID, store.QueueJoin{UserID: b.ID, TeamKey: "x|y"})
	wantKind(t, err, store.ErrQueueInvalid)

	// Закрытая запись и пауза закрывают игрока, но не организатора.
	d := e.user("d")
	if _, err := e.st.SetQueueMode(e.ctx, sid, "", "close"); err != nil {
		t.Fatal(err)
	}
	_, err = e.st.JoinQueue(e.ctx, sid, d.ID, store.QueueJoin{UserID: d.ID})
	wantKind(t, err, store.ErrQueueState)
	snap := e.snap(sid, d.ID)
	if snap.Me.CanJoin || snap.Me.JoinBlocked == "" {
		t.Errorf("закрытая запись: me=%+v", snap.Me)
	}
	if _, err := e.st.SetQueueMode(e.ctx, sid, "", "pause"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.JoinQueue(e.ctx, sid, "", store.QueueJoin{UserID: d.ID, ByOrganizer: true}); err != nil {
		t.Errorf("организатор добавляет при паузе: %v", err)
	}
	if got := e.snap(sid, d.ID).Session.State; got != "paused" {
		t.Errorf("состояние %q, want paused", got)
	}
	wantOrder(t, e.order(sid), "Ник b", "Ник c", "Ник a", "Ник d")
}

func TestQueueFlowFifoAndMoveToEnd(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	a, b, c, d := e.user("a"), e.user("b"), e.user("c"), e.user("d")
	for _, u := range []models.User{a, b, c, d} {
		e.join(sid, u)
	}

	// Вызов следующего: первый по очереди становится «Приглашён».
	res, err := e.st.CallEntry(e.ctx, sid, "", "")
	if err != nil {
		t.Fatal(err)
	}
	s := e.snap(sid, a.ID)
	if s.Entries[0].Status != "invited" || s.Entries[0].InviteExpiresAt == nil || !s.Entries[1].IsNext {
		t.Errorf("после вызова: %+v", s.Entries[:2])
	}
	if !s.Me.NeedsConfirm || s.Me.Position != 1 || s.Me.Ahead != 0 {
		t.Errorf("me вызванного: %+v", s.Me)
	}
	if left := time.Until(*s.Entries[0].InviteExpiresAt); left < 55*time.Second || left > 61*time.Second {
		t.Errorf("время на подтверждение %v, want ~60с", left)
	}
	if res.Rev <= 1 {
		t.Errorf("ревизия не выросла: %d", res.Rev)
	}

	// Начать нельзя: не собрана вторая сторона и нет подтверждения.
	_, err = e.st.StartQueueMatch(e.ctx, sid, "", false)
	wantKind(t, err, store.ErrQueueState)
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	// Третьего вызвать нельзя - матч собран.
	_, err = e.st.CallEntry(e.ctx, sid, "", "")
	wantKind(t, err, store.ErrQueueState)
	_, err = e.st.StartQueueMatch(e.ctx, sid, "", false)
	wantKind(t, err, store.ErrQueueState)

	// Подтверждение: чужой игрок не вызывался, вызванные подтверждают сами.
	_, err = e.st.ConfirmReady(e.ctx, sid, c.ID)
	wantKind(t, err, store.ErrQueueState)
	if _, err := e.st.ConfirmReady(e.ctx, sid, a.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.status(sid, "Ник a"); got != "ready" {
		t.Errorf("после подтверждения %q, want ready", got)
	}
	if _, err := e.st.ConfirmReady(e.ctx, sid, a.ID); err != nil {
		t.Errorf("повторное подтверждение: %v", err)
	}
	_, err = e.st.StartQueueMatch(e.ctx, sid, "", false)
	wantKind(t, err, store.ErrQueueState) // b не подтвердил
	if _, err := e.st.ConfirmReady(e.ctx, sid, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.StartQueueMatch(e.ctx, sid, "", false); err != nil {
		t.Fatal(err)
	}
	s = e.snap(sid, "")
	if s.Match == nil || s.Match.Status != "live" || len(s.Match.Sides) != 2 || s.Entries[0].Status != "performing" {
		t.Fatalf("матч в эфире: %+v", s.Match)
	}
	// Выступающий не может выйти, и вызывать при матче в эфире нельзя.
	_, err = e.st.LeaveQueue(e.ctx, sid, a.ID)
	wantKind(t, err, store.ErrQueueState)
	_, err = e.st.CallEntry(e.ctx, sid, "", "")
	wantKind(t, err, store.ErrQueueState)

	// Новичок во время эфира встаёт за ждущими, но перед теми, кто уже выступил.
	if _, err := e.st.FinishQueueMatch(e.ctx, sid, ""); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Ник c", "Ник d", "Ник a", "Ник b")
	f := e.user("f")
	e.join(sid, f)
	wantOrder(t, e.order(sid), "Ник c", "Ник d", "Ник f", "Ник a", "Ник b")

	s = e.snap(sid, "")
	for _, en := range s.Entries {
		if (en.Name == "Ник a" || en.Name == "Ник b") && (en.PerformedCount != 1 || en.Status != "waiting") {
			t.Errorf("%s после выступления: %+v", en.Name, en)
		}
	}
	if s.Last == nil || s.Last.Status != "finished" || s.Match != nil {
		t.Errorf("последний матч: %+v / текущий %+v", s.Last, s.Match)
	}
	hist, err := e.st.QueueMatchHistory(e.ctx, sid)
	if err != nil || len(hist) != 1 || len(hist[0].Sides) != 2 || hist[0].Sides[0].Name != "Ник a" {
		t.Errorf("история матчей: %+v %v", hist, err)
	}

	// Дальше вызываем следующих по очереди: c и d, потом f.
	for range 2 {
		if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.status(sid, "Ник f"); got != "waiting" {
		t.Errorf("f %q, want waiting", got)
	}
	if _, err := e.st.CancelQueueMatch(e.ctx, sid, ""); err != nil {
		t.Fatal(err)
	}
	// Отмена возвращает вызванных в ожидание без «выступления».
	for _, en := range e.snap(sid, "").Entries {
		if en.Status != "waiting" {
			t.Errorf("после отмены %s: %s", en.Name, en.Status)
		}
		if (en.Name == "Ник c" || en.Name == "Ник d") && en.PerformedCount != 0 {
			t.Errorf("отмена засчитала выступление: %+v", en)
		}
	}
	wantOrder(t, e.order(sid), "Ник c", "Ник d", "Ник f", "Ник a", "Ник b")
}

func TestQueueSkipNoShowAndReturn(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	a, b, c, d := e.user("a"), e.user("b"), e.user("c"), e.user("d")
	for _, u := range []models.User{a, b, c, d} {
		e.join(sid, u)
	}
	call, err := e.st.CallEntry(e.ctx, sid, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Не ответивший вызванный: организатор пропускает - фиксируется неявка, запись уходит в конец.
	if _, err := e.st.SkipEntry(e.ctx, sid, "", call.EntryID, "end"); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Ник b", "Ник c", "Ник d", "Ник a")
	s := e.snap(sid, "")
	last := s.Entries[3]
	if last.Status != "skipped" || last.SkippedCount != 1 || s.Match != nil {
		t.Errorf("пропущенный: %+v, матч %+v", last, s.Match)
	}
	events, err := e.st.QueueEvents(e.ctx, sid, 50)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	if !slices.Contains(kinds, "no_show") || !slices.Contains(kinds, "invite") || !slices.Contains(kinds, "join") {
		t.Errorf("журнал без вызова/неявки: %v", kinds)
	}

	// Пропущенного можно вернуть: он становится следующим.
	if _, err := e.st.ReturnEntry(e.ctx, sid, "", last.ID, 0); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Ник a", "Ник b", "Ник c", "Ник d")
	if got := e.status(sid, "Ник a"); got != "waiting" {
		t.Errorf("возвращённый %q", got)
	}
	_, err = e.st.ReturnEntry(e.ctx, sid, "", last.ID, 0)
	wantKind(t, err, store.ErrQueueState) // уже не пропущен

	// Пропуск ждущего и исключение из сессии.
	if _, err := e.st.SkipEntry(e.ctx, sid, "", last.ID, "end"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SkipEntry(e.ctx, sid, "", last.ID, "remove"); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Ник b", "Ник c", "Ник d")
	_, err = e.st.SkipEntry(e.ctx, sid, "", last.ID, "end")
	wantKind(t, err, store.ErrNotFound) // убранной записи больше нет в очереди
	_, err = e.st.SkipEntry(e.ctx, sid, "", "no-such", "end")
	wantKind(t, err, store.ErrNotFound)
	_, err = e.st.SkipEntry(e.ctx, sid, "", last.ID, "teleport")
	wantKind(t, err, store.ErrQueueInvalid)

	// Исключённый может записаться заново - новой записью в конец.
	e.join(sid, a)
	wantOrder(t, e.order(sid), "Ник b", "Ник c", "Ник d", "Ник a")

	// Пропуск выступающего запрещён.
	call, _ = e.st.CallEntry(e.ctx, sid, "", "")
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.StartQueueMatch(e.ctx, sid, "", true); err != nil { // принудительный старт без подтверждений
		t.Fatal(err)
	}
	_, err = e.st.SkipEntry(e.ctx, sid, "", call.EntryID, "end")
	wantKind(t, err, store.ErrQueueState)
	_, err = e.st.RemoveEntry(e.ctx, sid, "", call.EntryID)
	wantKind(t, err, store.ErrQueueState)
}

func TestQueueManualMoveAndStaleRevision(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	var us []models.User
	for _, tag := range []string{"a", "b", "c", "d"} {
		u := e.user(tag)
		us = append(us, u)
		e.join(sid, u)
	}
	ids := map[string]string{}
	for _, en := range e.snap(sid, "").Entries {
		ids[en.Name] = en.ID
	}

	if _, err := e.st.MoveEntry(e.ctx, sid, "", ids["Ник d"], 1, 0); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Ник d", "Ник a", "Ник b", "Ник c")
	if _, err := e.st.MoveEntry(e.ctx, sid, "", ids["Ник d"], 4, 0); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Ник a", "Ник b", "Ник c", "Ник d")

	// Устаревшая ревизия - отказ и порядок не меняется.
	rev := e.snap(sid, "").Session.Rev
	if _, err := e.st.MoveEntry(e.ctx, sid, "", ids["Ник c"], 1, rev); err != nil {
		t.Fatal(err)
	}
	_, err := e.st.MoveEntry(e.ctx, sid, "", ids["Ник d"], 1, rev)
	wantKind(t, err, store.ErrQueueStale)
	wantOrder(t, e.order(sid), "Ник c", "Ник a", "Ник b", "Ник d")

	// Границы.
	for _, pos := range []int{0, -1, 5, 100} {
		_, err := e.st.MoveEntry(e.ctx, sid, "", ids["Ник a"], pos, 0)
		wantKind(t, err, store.ErrQueueInvalid)
	}
	_, err = e.st.MoveEntry(e.ctx, sid, "", "no-such", 1, 0)
	wantKind(t, err, store.ErrNotFound)

	// Вызванного на матч переставлять нельзя, а ждущий не встаёт перед ним.
	if _, err := e.st.CallEntry(e.ctx, sid, "", ids["Ник b"]); err != nil { // вызов из середины
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Ник b", "Ник c", "Ник a", "Ник d")
	_, err = e.st.MoveEntry(e.ctx, sid, "", ids["Ник b"], 3, 0)
	wantKind(t, err, store.ErrQueueState)
	if _, err := e.st.MoveEntry(e.ctx, sid, "", ids["Ник d"], 1, 0); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Ник b", "Ник d", "Ник c", "Ник a")

	// Удаление организатором.
	if _, err := e.st.RemoveEntry(e.ctx, sid, "", ids["Ник c"]); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Ник b", "Ник d", "Ник a")
	// Удаление вызванного снимает его с матча.
	if _, err := e.st.RemoveEntry(e.ctx, sid, "", ids["Ник b"]); err != nil {
		t.Fatal(err)
	}
	if s := e.snap(sid, ""); s.Match != nil {
		t.Errorf("пустой матч остался: %+v", s.Match)
	}
	_ = us
}

func TestQueueTeams2x2(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("2x2")
	a, b, c, d, x := e.user("a"), e.user("b"), e.user("c"), e.user("d"), e.user("x")
	alpha, err := e.st.CreateTeam(e.ctx, "Альфа", a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := e.st.CreateTeam(e.ctx, "Бета", c.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	gamma, err := e.st.CreateTeam(e.ctx, "Гамма", b.ID, x.ID) // у b вторая команда
	if err != nil {
		t.Fatal(err)
	}

	// Чужую команду записать нельзя; без команды нельзя; игрок с командами видит их в me.
	_, err = e.st.JoinQueue(e.ctx, sid, x.ID, store.QueueJoin{UserID: x.ID, TeamKey: alpha.TeamKey})
	wantKind(t, err, store.ErrQueueForbidden)
	_, err = e.st.JoinQueue(e.ctx, sid, a.ID, store.QueueJoin{UserID: a.ID})
	wantKind(t, err, store.ErrQueueInvalid)
	_, err = e.st.JoinQueue(e.ctx, sid, a.ID, store.QueueJoin{UserID: a.ID, TeamKey: "no|such"})
	wantKind(t, err, store.ErrNotFound)
	if me := e.snap(sid, b.ID).Me; len(me.Teams) != 2 || !me.CanJoin {
		t.Errorf("команды игрока: %+v", me)
	}

	if _, err := e.st.JoinQueue(e.ctx, sid, a.ID, store.QueueJoin{UserID: a.ID, TeamKey: alpha.TeamKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.JoinQueue(e.ctx, sid, c.ID, store.QueueJoin{UserID: c.ID, TeamKey: beta.TeamKey}); err != nil {
		t.Fatal(err)
	}
	// Одна команда - одна запись: партнёр не записывает её повторно, и игрок не встаёт в другую команду.
	_, err = e.st.JoinQueue(e.ctx, sid, b.ID, store.QueueJoin{UserID: b.ID, TeamKey: alpha.TeamKey})
	wantKind(t, err, store.ErrQueueState)
	_, err = e.st.JoinQueue(e.ctx, sid, b.ID, store.QueueJoin{UserID: b.ID, TeamKey: gamma.TeamKey})
	wantKind(t, err, store.ErrQueueState)
	wantOrder(t, e.order(sid), "Альфа", "Бета")

	s := e.snap(sid, b.ID) // партнёр видит место команды
	if s.Me.Position != 1 || len(s.Entries[0].Members) != 2 || s.Entries[0].TeamKey != alpha.TeamKey {
		t.Errorf("запись команды: me=%+v entry=%+v", s.Me, s.Entries[0])
	}
	if me := e.snap(sid, x.ID).Me; len(me.Teams) != 1 || me.Teams[0].Blocked == "" || me.CanJoin {
		t.Errorf("команда с занятым напарником должна быть заблокирована: %+v", me)
	}

	// Вызывается команда целиком, готовность - от обоих игроков.
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.ConfirmReady(e.ctx, sid, a.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.status(sid, "Альфа"); got != "invited" {
		t.Errorf("подтвердил один из двух: %q, want invited", got)
	}
	s = e.snap(sid, a.ID)
	if s.Me.NeedsConfirm || !s.Me.Confirmed {
		t.Errorf("a подтвердил, ждём партнёра: %+v", s.Me)
	}
	if me := e.snap(sid, b.ID).Me; !me.NeedsConfirm {
		t.Errorf("b ещё не подтвердил: %+v", me)
	}
	if _, err := e.st.ConfirmReady(e.ctx, sid, b.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.status(sid, "Альфа"); got != "ready" {
		t.Errorf("подтвердили оба: %q, want ready", got)
	}

	// Вторая команда: организатор подтверждает за игроков и запускает матч.
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	var betaEntry string
	for _, en := range e.snap(sid, "").Entries {
		if en.Name == "Бета" {
			betaEntry = en.ID
		}
	}
	if _, err := e.st.ConfirmEntry(e.ctx, sid, "", betaEntry); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.StartQueueMatch(e.ctx, sid, "", false); err != nil {
		t.Fatal(err)
	}
	// После матча команды уходят в конец целиком: напарники не расходятся по позициям.
	if _, err := e.st.FinishQueueMatch(e.ctx, sid, ""); err != nil {
		t.Fatal(err)
	}
	s = e.snap(sid, "")
	for _, en := range s.Entries {
		if len(en.Members) != 2 || en.PerformedCount != 1 || en.Status != "waiting" {
			t.Errorf("команда после матча: %+v", en)
		}
	}
	if s.Last == nil || len(s.Last.Sides) != 2 || len(s.Last.Sides[0].Members) != 2 {
		t.Errorf("история матча 2×2: %+v", s.Last)
	}

	// Напарник выходит - уходит вся команда, на её месте никого не остаётся.
	if _, err := e.st.LeaveQueue(e.ctx, sid, b.ID); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Бета")
	if me := e.snap(sid, a.ID).Me; me.EntryID != "" || !me.CanJoin {
		t.Errorf("после выхода напарника a снова может записаться: %+v", me)
	}
	// Команда может записаться заново, но выступавшая встаёт в конец: обойти очередь выходом нельзя.
	if _, err := e.st.JoinQueue(e.ctx, sid, b.ID, store.QueueJoin{UserID: b.ID, TeamKey: alpha.TeamKey}); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Бета", "Альфа")
}

func TestQueueSessionLifecycle(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	a, b := e.user("a"), e.user("b")
	e.join(sid, a)
	e.join(sid, b)

	// Вторая живая очередь того же формата не открывается, другого формата - можно.
	_, err := e.st.CreateQueueSession(e.ctx, "", store.QueueSessionInput{Format: "1x1"})
	wantKind(t, err, store.ErrQueueState)
	_, err = e.st.CreateQueueSession(e.ctx, "", store.QueueSessionInput{Format: "3x3"})
	wantKind(t, err, store.ErrQueueInvalid)
	_, err = e.st.CreateQueueSession(e.ctx, "", store.QueueSessionInput{Format: "1x1", ConfirmTimeoutSec: 5})
	wantKind(t, err, store.ErrQueueInvalid)
	sid2 := e.session("2x2")
	if got := e.snap(sid, "").Active; len(got) != 2 {
		t.Errorf("живые очереди: %+v", got)
	}

	// Формат меняется только у пустой очереди.
	f := "2x2"
	_, err = e.st.UpdateQueueSession(e.ctx, sid, "", store.QueueSessionPatch{Format: &f})
	wantKind(t, err, store.ErrQueueState) // в очереди есть записи
	one := "1x1"
	_, err = e.st.UpdateQueueSession(e.ctx, sid2, "", store.QueueSessionPatch{Format: &one})
	wantKind(t, err, store.ErrQueueState) // 1×1 уже занят
	timeout, title := 120, "itest переименована"
	if _, err := e.st.UpdateQueueSession(e.ctx, sid, "", store.QueueSessionPatch{ConfirmTimeoutSec: &timeout, Title: &title}); err != nil {
		t.Fatal(err)
	}
	if s := e.snap(sid, "").Session; s.ConfirmTimeoutSec != 120 || s.Title != title {
		t.Errorf("настройки не применились: %+v", s)
	}
	bad := 9
	_, err = e.st.UpdateQueueSession(e.ctx, sid, "", store.QueueSessionPatch{ConfirmTimeoutSec: &bad})
	wantKind(t, err, store.ErrQueueInvalid)

	// Идущий матч не даёт закончить сессию.
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	if s := e.snap(sid, ""); s.Session.ConfirmTimeoutSec != 120 || s.Entries[0].InviteExpiresAt == nil ||
		time.Until(*s.Entries[0].InviteExpiresAt) < 100*time.Second {
		t.Errorf("своё время на подтверждение не применилось: %+v", s.Entries[0])
	}
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.StartQueueMatch(e.ctx, sid, "", true); err != nil {
		t.Fatal(err)
	}
	_, err = e.st.FinishQueueSession(e.ctx, sid, "")
	wantKind(t, err, store.ErrQueueState)
	if _, err := e.st.FinishQueueMatch(e.ctx, sid, ""); err != nil {
		t.Fatal(err)
	}

	// Подготовка матча при завершении сессии отменяется.
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.FinishQueueSession(e.ctx, sid, ""); err != nil {
		t.Fatal(err)
	}

	// Завершённая сессия закрыта для любых изменений, но остаётся доступной для просмотра.
	c := e.user("c")
	_, err = e.st.JoinQueue(e.ctx, sid, "", store.QueueJoin{UserID: c.ID, ByOrganizer: true})
	wantKind(t, err, store.ErrQueueState)
	_, err = e.st.CallEntry(e.ctx, sid, "", "")
	wantKind(t, err, store.ErrQueueState)
	_, err = e.st.SetQueueMode(e.ctx, sid, "", "resume")
	wantKind(t, err, store.ErrQueueState)
	_, err = e.st.LeaveQueue(e.ctx, sid, a.ID)
	wantKind(t, err, store.ErrQueueState)
	s := e.snap(sid, "")
	if s.Session.State != "finished" || s.Session.FinishedAt == nil || len(s.Entries) != 2 || len(s.History) != 2 || s.Match != nil {
		t.Errorf("завершённая сессия: state=%s entries=%d history=%d match=%+v", s.Session.State, len(s.Entries), len(s.History), s.Match)
	}
	if s.Me.CanJoin {
		t.Errorf("в завершённую сессию записаться нельзя: %+v", s.Me)
	}
	list, err := e.st.ListQueueSessions(e.ctx, 10)
	if err != nil || len(list) == 0 {
		t.Fatalf("список сессий: %v", err)
	}
	if _, err := e.st.QueueEvents(e.ctx, sid, 10); err != nil {
		t.Fatal(err)
	}

	// Новая сессия начинается с пустой очереди, прежняя история на месте.
	sid3 := e.session("1x1")
	if s := e.snap(sid3, ""); len(s.Entries) != 0 || s.Session.Waiting != 0 {
		t.Errorf("новая сессия не пуста: %+v", s.Entries)
	}
	e.join(sid3, a) // игрок прежней сессии записывается в новую
	if got := e.snap(sid, "").Entries; len(got) != 2 {
		t.Errorf("история прежней сессии изменилась: %+v", got)
	}
	if def := store.QueueDefaultTitle(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)); def != "Очередь на выступления | 30.10.2026" {
		t.Errorf("название по умолчанию %q", def)
	}
}

// Ограничения БД держатся и без прикладного кода: прямой SQL не создаёт дубль и общую позицию.
func TestQueueDatabaseConstraints(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	a, b := e.user("a"), e.user("b")
	e.join(sid, a)
	e.join(sid, b)
	var entryA, entryB string
	_ = e.pool.QueryRow(e.ctx, `SELECT id FROM queue_entries WHERE session_id = $1 AND position = 1`, sid).Scan(&entryA)
	_ = e.pool.QueryRow(e.ctx, `SELECT id FROM queue_entries WHERE session_id = $1 AND position = 2`, sid).Scan(&entryB)

	// Две записи на одной позиции - отказ в конце транзакции.
	_, err := e.pool.Exec(e.ctx, `UPDATE queue_entries SET position = 1 WHERE id = $1`, entryB)
	if err == nil {
		t.Error("две записи на одной позиции допущены")
	}
	// Тот же игрок второй раз в той же сессии.
	var third string
	if err := e.pool.QueryRow(e.ctx, `
		INSERT INTO queue_entries (session_id, position) VALUES ($1, 99) RETURNING id`, sid).Scan(&third); err != nil {
		t.Fatal(err)
	}
	_, err = e.pool.Exec(e.ctx, `INSERT INTO queue_entry_members (entry_id, session_id, user_id) VALUES ($1, $2, $3)`, third, sid, a.ID)
	if err == nil {
		t.Error("игрок записан в сессию дважды")
	}
	// Позиция у вышедшей записи запрещена, а у стоящей в очереди обязательна.
	if _, err := e.pool.Exec(e.ctx, `UPDATE queue_entries SET status = 'left' WHERE id = $1`, entryA); err == nil {
		t.Error("вышедшая запись сохранила позицию")
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE queue_entries SET position = NULL WHERE id = $1`, entryA); err == nil {
		t.Error("запись в очереди без позиции")
	}
	// Закрытая запись освобождает игрока (триггер) - с тем же игроком можно записаться снова.
	if _, err := e.pool.Exec(e.ctx, `UPDATE queue_entries SET status = 'left', position = NULL WHERE id = $1`, entryA); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO queue_entry_members (entry_id, session_id, user_id) VALUES ($1, $2, $3)`, third, sid, a.ID); err != nil {
		t.Errorf("игрок после выхода должен записываться заново: %v", err)
	}
	// Некорректные значения.
	if _, err := e.pool.Exec(e.ctx, `UPDATE queue_sessions SET confirm_timeout_sec = 1 WHERE id = $1`, sid); err == nil {
		t.Error("время подтверждения 1 с допущено")
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE queue_entries SET status = 'weird' WHERE id = $1`, entryB); err == nil {
		t.Error("неизвестный статус допущен")
	}
}

// Удалённый аккаунт не оставляет в очереди пустого места.
func TestQueueGhostEntryIsSwept(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	a, b, c := e.user("a"), e.user("b"), e.user("c")
	for _, u := range []models.User{a, b, c} {
		e.join(sid, u)
	}
	if _, err := e.pool.Exec(e.ctx, `DELETE FROM users WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	// Любое следующее действие подбирает мусор, порядок остаётся без дыр.
	d := e.user("d")
	e.join(sid, d)
	wantOrder(t, e.order(sid), "Ник a", "Ник c", "Ник d")
}

// Одновременные записи: никто не теряется, не двоится, позиции подряд.
func TestQueueConcurrentJoins(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	const n = 40
	users := make([]models.User, n)
	for i := range users {
		users[i] = e.user(fmt.Sprintf("u%02d", i))
	}

	var wg sync.WaitGroup
	var failed atomic.Int32
	for _, u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.st.JoinQueue(e.ctx, sid, u.ID, store.QueueJoin{UserID: u.ID}); err != nil {
				failed.Add(1)
				t.Errorf("запись %s: %v", u.Login, err)
			}
		}()
	}
	wg.Wait()
	if failed.Load() != 0 {
		return
	}
	got := e.order(sid) // проверяет и позиции 1..N
	if len(got) != n {
		t.Fatalf("в очереди %d записей, want %d", len(got), n)
	}
	seen := map[string]bool{}
	for _, name := range got {
		if seen[name] {
			t.Errorf("дубль %s", name)
		}
		seen[name] = true
	}

	// Один и тот же игрок записывается одновременно много раз - получится ровно одна запись.
	x := e.user("x")
	var ok, dup atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.st.JoinQueue(e.ctx, sid, x.ID, store.QueueJoin{UserID: x.ID})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, store.ErrQueueState):
				dup.Add(1)
			default:
				t.Errorf("повторная запись: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || dup.Load() != 11 {
		t.Errorf("повторные записи: успешных %d, отказов %d, want 1 и 11", ok.Load(), dup.Load())
	}
	if len(e.order(sid)) != n+1 {
		t.Errorf("в очереди %d записей, want %d", len(e.order(sid)), n+1)
	}
}

// Организатор переставляет, вызывает и убирает одновременно с записями игроков: порядок всегда целый.
func TestQueueConcurrentMixedOperations(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	const base = 12
	for i := range base {
		e.join(sid, e.user(fmt.Sprintf("b%02d", i)))
	}
	late := make([]models.User, 20)
	for i := range late {
		late[i] = e.user(fmt.Sprintf("l%02d", i))
	}

	entryIDs := func() []string {
		var out []string
		for _, en := range e.snap(sid, "").Entries {
			out = append(out, en.ID)
		}
		return out
	}
	start := entryIDs()

	var wg sync.WaitGroup
	for _, u := range late {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.st.JoinQueue(e.ctx, sid, u.ID, store.QueueJoin{UserID: u.ID}); err != nil {
				t.Errorf("запись: %v", err)
			}
		}()
	}
	for i := range 10 {
		wg.Add(1)
		go func() { // перестановки; отказ допустим только если запись уже вызвана на матч
			defer wg.Done()
			_, err := e.st.MoveEntry(e.ctx, sid, "", start[(i*5)%base], 1+i%base, 0)
			if err != nil && !errors.Is(err, store.ErrQueueState) && !errors.Is(err, store.ErrNotFound) {
				t.Errorf("перестановка: %v", err)
			}
		}()
	}
	for range 3 {
		wg.Add(1)
		go func() { // одновременные вызовы: вызванными станут ровно двое
			defer wg.Done()
			_, err := e.st.CallEntry(e.ctx, sid, "", "")
			if err != nil && !errors.Is(err, store.ErrQueueState) {
				t.Errorf("вызов: %v", err)
			}
		}()
	}
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.st.RemoveEntry(e.ctx, sid, "", start[base-1-i])
			if err != nil && !errors.Is(err, store.ErrQueueState) && !errors.Is(err, store.ErrNotFound) {
				t.Errorf("удаление: %v", err)
			}
		}()
	}
	wg.Wait()

	s := e.snap(sid, "")
	order := e.order(sid) // позиции 1..N без дыр и дублей
	seen := map[string]bool{}
	for _, n := range order {
		if seen[n] {
			t.Errorf("дубль %s", n)
		}
		seen[n] = true
	}
	invited := 0
	for _, en := range s.Entries {
		if en.Status == "invited" {
			invited++
		}
	}
	if invited > 2 {
		t.Errorf("вызвано %d записей при матче из двух", invited)
	}
	for i, en := range s.Entries {
		if invited > 0 && i < invited && en.Status != "invited" {
			t.Errorf("вызванные не в начале списка: %+v", s.Entries[:invited+1])
			break
		}
	}
	var open int
	_ = e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM queue_matches WHERE session_id = $1 AND status IN ('preparing','live')`, sid).Scan(&open)
	if open > 1 {
		t.Errorf("открытых матчей %d", open)
	}
	// Ревизия выросла на каждое удавшееся изменение и не повторялась (иначе клиенты пропустят обновление).
	if s.Session.Rev < int64(len(late)) {
		t.Errorf("ревизия %d меньше числа изменений", s.Session.Rev)
	}
}

// После перезапуска сервера очередь читается из PostgreSQL такой же: порядок, статусы, таймеры, матч.
func TestQueueRestoredAfterRestart(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	var us []models.User
	for _, tag := range []string{"a", "b", "c", "d"} {
		u := e.user(tag)
		us = append(us, u)
		e.join(sid, u)
	}
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.ConfirmReady(e.ctx, sid, us[0].ID); err != nil {
		t.Fatal(err)
	}
	before := e.snap(sid, us[1].ID)

	// «Перезапуск»: закрываем пул и поднимаем новый Store над той же базой.
	e.pool.Close()
	pool, err := db.Connect(e.ctx, os.Getenv("RESPECT_TEST_DB"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.pool, e.st = pool, store.New(pool)
	id, err := e.st.QueueActiveID(e.ctx, "1x1")
	if err != nil || id != sid {
		t.Fatalf("живая очередь после перезапуска: %q %v, want %q", id, err, sid)
	}
	after := e.snap(sid, us[1].ID)

	if after.Session.Rev != before.Session.Rev || len(after.Entries) != len(before.Entries) {
		t.Fatalf("ревизия/состав: %d/%d -> %d/%d", before.Session.Rev, len(before.Entries), after.Session.Rev, len(after.Entries))
	}
	for i := range before.Entries {
		b, a := before.Entries[i], after.Entries[i]
		sameTimer := (a.InviteExpiresAt == nil) == (b.InviteExpiresAt == nil) &&
			(b.InviteExpiresAt == nil || a.InviteExpiresAt.Equal(*b.InviteExpiresAt))
		if a.ID != b.ID || a.Status != b.Status || a.Position != b.Position || !sameTimer {
			t.Errorf("запись %d изменилась: %+v -> %+v", i, b, a)
		}
	}
	if after.Match == nil || after.Match.ID != before.Match.ID || after.Match.Status != "preparing" || len(after.Match.Sides) != 2 {
		t.Errorf("матч после перезапуска: %+v", after.Match)
	}
	if after.Me.Status != "invited" || !after.Me.NeedsConfirm {
		t.Errorf("me после перезапуска: %+v", after.Me)
	}
	// Работа продолжается с того же места.
	if _, err := e.st.ConfirmReady(e.ctx, sid, us[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.StartQueueMatch(e.ctx, sid, "", false); err != nil {
		t.Fatal(err)
	}
}

// Сведения о MMR: у не игравшего - старт сезона, у игравшего - со всеми изменениями; жетон «Без очереди» виден.
func TestQueueEntryMmrAndPriority(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	a, b := e.user("a"), e.user("b")
	e.join(sid, a)
	e.join(sid, b)
	start := e.st.ActiveSeasonStart(e.ctx)
	var seasonKey string
	_ = e.pool.QueryRow(e.ctx, `SELECT COALESCE((SELECT id FROM seasons WHERE status = 'active' LIMIT 1), '')`).Scan(&seasonKey)
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO mmr_history (user_id, mode, delta, mmr_before, mmr_after, season_key) VALUES ($1, '1x1', 37, $2, $2 + 37, $3)`,
		b.ID, start, seasonKey); err != nil {
		t.Skipf("схема истории MMR другая: %v", err)
	}
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO token_ledger (user_id, kind, action, delta) VALUES ($1, 'priority', 'use', -1)`, b.ID); err != nil {
		t.Fatal(err)
	}
	s := e.snap(sid, "")
	if s.Entries[0].Mmr != start || s.Entries[0].Priority {
		t.Errorf("не игравший: %+v, старт %d", s.Entries[0], start)
	}
	if s.Entries[1].Mmr != start+37 || !s.Entries[1].Priority {
		t.Errorf("игравший с жетоном: %+v", s.Entries[1])
	}
}
