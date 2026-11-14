package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Кулдаун после матча, порядок по последнему матчу, новичок и 2×2 на живой базе (RESPECT_TEST_DB).

var mskZone = time.FixedZone("MSK", 3*3600)

func (e *queueEnv) cooldown(minutes int) { setTestCooldown(e.t, e.url, minutes) }

// todayAt - момент сегодняшнего дня по Москве: доля frac времени, прошедшего с полуночи. Так матчи остаются
// «сегодняшними», в какое бы время ни шёл тест.
func todayAt(frac float64) time.Time {
	now := time.Now()
	y, m, d := now.In(mskZone).Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, mskZone)
	return start.Add(time.Duration(float64(now.Sub(start)) * frac))
}

// playedAt - завершённый матч сайта между a и b, будто он завершился в момент at.
func (e *queueEnv) playedAt(a, b models.User, at time.Time) string {
	e.t.Helper()
	tm, err := e.st.CreateMatch(e.ctx, store.NewMatch{Mode: "1x1", PlayerType: "pvp", Format: store.FormatMatch, Rounds: 2,
		Sides: [2]store.MatchSide{{UserID: a.ID}, {UserID: b.ID}}})
	if err != nil {
		e.t.Fatalf("CreateMatch: %v", err)
	}
	e.t.Cleanup(func() { _ = e.st.DeleteTournament(e.ctx, tm.ID) })
	if _, err := e.st.FinishMatch(e.ctx, tm.ID); err != nil {
		e.t.Fatalf("FinishMatch: %v", err)
	}
	if _, err := e.pool.Exec(e.ctx, `
		UPDATE tournaments SET finished_at = $2::timestamptz, starts_at = $2::timestamptz - interval '30 minutes',
		       created_at = $2::timestamptz - interval '30 minutes'
		WHERE id = $1`, tm.ID, at); err != nil {
		e.t.Fatal(err)
	}
	return tm.ID
}

// played - матч, завершившийся только что; очереди узнают о нём так же, как после пульта.
func (e *queueEnv) played(a, b models.User) (string, time.Time) {
	e.t.Helper()
	tid := e.playedAt(a, b, time.Now())
	var at time.Time
	if err := e.pool.QueryRow(e.ctx, `SELECT finished_at FROM tournaments WHERE id = $1`, tid).Scan(&at); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.QueueAfterMatch(e.ctx, tid, ""); err != nil {
		e.t.Fatalf("QueueAfterMatch: %v", err)
	}
	return tid, at
}

func (e *queueEnv) entry(sid, name string) models.QueueEntry {
	e.t.Helper()
	for _, en := range e.snap(sid, "").Entries {
		if en.Name == name {
			return en
		}
	}
	e.t.Fatalf("записи %q нет в очереди", name)
	return models.QueueEntry{}
}

// refusal - текст отказа очереди (действие не подходит состоянию); пусто - получилось.
func refusal(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var qe *store.QueueError
	if !errors.Is(err, store.ErrQueueState) || !errors.As(err, &qe) {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	return qe.Msg
}

func sameTime(a *time.Time, b time.Time) bool { return a != nil && a.Sub(b).Abs() < time.Second }

// Сыгравший не записывается снова до конца кулдауна - ни в другую сессию, ни в 2×2, ни руками организатора.
func TestQueueCooldownBlocksRejoin(t *testing.T) {
	e := newQueueEnv(t)
	e.cooldown(180)
	sid := e.session("1x1")
	a, b, c := e.user("a"), e.user("b"), e.user("c")
	for _, u := range []models.User{a, b, c} {
		e.join(sid, u)
	}
	e.callAndConfirm(sid, a, b)
	if _, err := e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{}); err != nil {
		t.Fatal(err)
	}
	e.finishMatch(sid)
	var finished time.Time
	if err := e.pool.QueryRow(e.ctx, `
		SELECT t.finished_at FROM tournaments t JOIN queue_matches m ON m.tournament_id = t.id WHERE m.session_id = $1`, sid).
		Scan(&finished); err != nil {
		t.Fatal(err)
	}
	until := finished.Add(180 * time.Minute)

	// Кулдаун идёт от завершения матча: прямой запрос записи отказан, время разблокировки известно.
	msg := e.joinMsg(sid, a)
	if !strings.Contains(msg, "Вы уже выступали") || !strings.Contains(msg, until.In(mskZone).Format("15:04")+" МСК") {
		t.Errorf("отказ сыгравшему: %q", msg)
	}
	if me := e.snap(sid, a.ID).Me; me.CanJoin || !sameTime(me.CooldownUntil, until) || me.JoinBlocked != msg {
		t.Errorf("me сыгравшего: %+v", me)
	}
	if me := e.snap(sid, c.ID).Me; me.CooldownUntil != nil {
		t.Errorf("у не игравшего кулдаун: %+v", me)
	}

	// Организатор тоже не добавляет: кулдаун снимают отдельно, и это видно в журнале.
	_, err := e.st.JoinQueue(e.ctx, sid, "", store.QueueJoin{UserID: a.ID, ByOrganizer: true})
	if msg := refusal(t, err); !strings.Contains(msg, "кулдаун до") || !strings.Contains(msg, "«Кулдауны»") {
		t.Errorf("ручное добавление на кулдауне: %q", msg)
	}

	// 2×2 не обходит кулдаун: ни сам игрок, ни напарник не записывают команду.
	sid2 := e.session("2x2")
	x := e.user("x")
	team, err := e.st.CreateTeam(e.ctx, "Обход", a.ID, x.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.st.JoinQueue(e.ctx, sid2, a.ID, store.QueueJoin{UserID: a.ID, TeamKey: team.TeamKey})
	if msg := refusal(t, err); !strings.Contains(msg, "Вы уже выступали") {
		t.Errorf("сам игрок в 2×2: %q", msg)
	}
	_, err = e.st.JoinQueue(e.ctx, sid2, x.ID, store.QueueJoin{UserID: x.ID, TeamKey: team.TeamKey})
	if msg := refusal(t, err); !strings.Contains(msg, "У игрока Ник a кулдаун до") {
		t.Errorf("напарник в 2×2: %q", msg)
	}
	if me := e.snap(sid2, x.ID).Me; len(me.Teams) != 1 || !strings.Contains(me.Teams[0].Blocked, "кулдаун") ||
		!sameTime(me.Teams[0].CooldownUntil, until) || me.CanJoin {
		t.Errorf("команда напарника: %+v", me)
	}

	// Новая сессия кулдаун не сбрасывает.
	if _, err := e.st.FinishQueueSession(e.ctx, sid, ""); err != nil {
		t.Fatal(err)
	}
	sid3 := e.session("1x1")
	if msg := e.joinMsg(sid3, a); !strings.Contains(msg, "Вы уже выступали") {
		t.Errorf("в новой сессии: %q", msg)
	}

	// «Перезапуск сервера»: новый пул и Store видят тот же кулдаун - он в базе, а не в памяти.
	pool2, err := db.Connect(e.ctx, e.url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool2.Close()
	snap, err := store.New(pool2).QueueSnapshotFor(e.ctx, sid3, a.ID)
	if err != nil || !sameTime(snap.Me.CooldownUntil, until) || snap.CooldownMinutes != 180 {
		t.Errorf("после перезапуска: %+v %v", snap.Me, err)
	}

	// Кулдаун кончился - запись открывается сама, без правок организатора.
	if _, err := e.pool.Exec(e.ctx, `
		UPDATE tournaments SET finished_at = finished_at - interval '181 minutes'
		WHERE id IN (SELECT tournament_id FROM queue_matches WHERE session_id = $1)`, sid); err != nil {
		t.Fatal(err)
	}
	if me := e.snap(sid3, a.ID).Me; !me.CanJoin || me.CooldownUntil != nil {
		t.Errorf("кулдаун кончился: %+v", me)
	}
	e.join(sid3, a)
}

// Матч вне очереди начинает кулдаун и у тех, кто уже стоит в очереди: запись встаёт к сыгравшим и не вызывается,
// а выход из очереди и повторная запись кулдаун не обходят.
func TestQueueCooldownAfterMatchElsewhere(t *testing.T) {
	e := newQueueEnv(t)
	e.cooldown(180)
	sid := e.session("1x1")
	d, f, g := e.user("d"), e.user("f"), e.user("g")
	for _, u := range []models.User{d, f, g} {
		e.join(sid, u)
	}
	wantOrder(t, e.order(sid), "Ник d", "Ник f", "Ник g")

	_, at := e.played(d, e.user("m"))
	wantOrder(t, e.order(sid), "Ник f", "Ник g", "Ник d")
	en := e.entry(sid, "Ник d")
	if en.TodayMatches != 1 || !sameTime(en.CooldownUntil, at.Add(180*time.Minute)) || en.IsNext || !sameTime(en.LastMatchAt, at) {
		t.Errorf("запись сыгравшего вне очереди: %+v", en)
	}
	var moved int
	if err := e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM queue_events WHERE session_id = $1 AND kind = 'played_elsewhere'`, sid).Scan(&moved); err != nil || moved != 1 {
		t.Errorf("журнал: %d %v", moved, err)
	}

	// «Вызвать» конкретно его нельзя, следующим идёт тот, у кого кулдауна нет.
	_, err := e.st.CallEntry(e.ctx, sid, "", en.ID)
	if msg := refusal(t, err); !strings.Contains(msg, "кулдаун до") {
		t.Errorf("вызов с кулдауном: %q", msg)
	}
	if next := e.snap(sid, "").Entries[0]; next.Name != "Ник f" || !next.IsNext {
		t.Errorf("следующий: %+v", next)
	}

	// Вышел и записывается снова - кулдаун никуда не делся.
	if _, err := e.st.LeaveQueue(e.ctx, sid, d.ID); err != nil {
		t.Fatal(err)
	}
	if msg := e.joinMsg(sid, d); !strings.Contains(msg, "Вы уже выступали") {
		t.Errorf("после выхода: %q", msg)
	}

	// Все ждущие на кулдауне - вызывать некого, и это сказано прямо.
	e.played(f, g)
	_, err = e.st.CallEntry(e.ctx, sid, "", "")
	if msg := refusal(t, err); !strings.Contains(msg, "на кулдауне") {
		t.Errorf("все на кулдауне: %q", msg)
	}
}

// Общая и своя длительность, снять, поставить вручную, отключить - и всё это в журнале.
func TestQueueCooldownRules(t *testing.T) {
	e := newQueueEnv(t)
	e.cooldown(60)
	sid := e.session("1x1")
	boss, err := e.st.CreateUser(e.ctx, "qcd"+e.user("boss").Login, "Организатор", "x", models.RoleSuperadmin)
	if err != nil {
		t.Fatal(err)
	}
	p1, p2, p3, p4 := e.user("p1"), e.user("p2"), e.user("p3"), e.user("p4")
	_, at12 := e.played(p1, p2)
	_, at34 := e.played(p3, p4)
	until := func(u models.User) *time.Time { e.t.Helper(); return e.snap(sid, u.ID).Me.CooldownUntil }

	// Общая длительность действует сразу, в том числе на уже идущий кулдаун.
	if got := until(p1); !sameTime(got, at12.Add(60*time.Minute)) {
		t.Errorf("общий час: %v", got)
	}
	if err := e.st.SetCooldownDefault(e.ctx, boss.ID, 120); err != nil {
		t.Fatal(err)
	}
	if got := until(p1); !sameTime(got, at12.Add(120*time.Minute)) {
		t.Errorf("общий 2 часа: %v", got)
	}

	// Своя длительность важнее общей.
	cp, err := e.st.ChangePlayerCooldown(e.ctx, boss.ID, p2.ID, store.CooldownDuration, 30)
	if err != nil || cp.Minutes == nil || *cp.Minutes != 30 || !sameTime(cp.Until, at12.Add(30*time.Minute)) {
		t.Fatalf("своя длительность: %+v %v", cp, err)
	}
	if got := until(p2); !sameTime(got, at12.Add(30*time.Minute)) {
		t.Errorf("своя в очереди: %v", got)
	}

	// Отключён - записывается без ограничения, но порядок по-прежнему знает, что он сегодня играл.
	e.join(sid, e.user("fresh"))
	if _, err := e.st.ChangePlayerCooldown(e.ctx, boss.ID, p3.ID, store.CooldownDisable, 0); err != nil {
		t.Fatal(err)
	}
	e.join(sid, p3)
	wantOrder(t, e.order(sid), "Ник fresh", "Ник p3")
	if en := e.entry(sid, "Ник p3"); en.TodayMatches != 1 || en.CooldownUntil != nil || !sameTime(en.LastMatchAt, at34) {
		t.Errorf("отключённый кулдаун: %+v", en)
	}
	// Включили обратно - стоит в очереди, но не вызывается, пока кулдаун не кончится.
	if _, err := e.st.ChangePlayerCooldown(e.ctx, boss.ID, p3.ID, store.CooldownEnable, 0); err != nil {
		t.Fatal(err)
	}
	if en := e.entry(sid, "Ник p3"); en.CooldownUntil == nil {
		t.Errorf("кулдаун после включения: %+v", en)
	}

	// Снять: записаться можно сразу; следующий матч начнёт новый кулдаун.
	if cp, err := e.st.ChangePlayerCooldown(e.ctx, boss.ID, p4.ID, store.CooldownClear, 0); err != nil || cp.Until != nil {
		t.Fatalf("снять: %+v %v", cp, err)
	}
	e.join(sid, p4)
	if _, err := e.st.LeaveQueue(e.ctx, sid, p4.ID); err != nil {
		t.Fatal(err)
	}
	if _, at := e.played(p4, e.user("m4")); !sameTime(until(p4), at.Add(120*time.Minute)) {
		t.Errorf("новый матч после снятия: %v", until(p4))
	}

	// Поставить вручную: ровно на столько от текущего момента, даже при выключенном общем.
	if err := e.st.SetCooldownDefault(e.ctx, boss.ID, 0); err != nil {
		t.Fatal(err)
	}
	if got := until(p1); got != nil {
		t.Errorf("общий выключен: %v", got)
	}
	if got := until(p2); !sameTime(got, at12.Add(30*time.Minute)) {
		t.Errorf("своя при выключенном общем: %v", got)
	}
	cp, err = e.st.ChangePlayerCooldown(e.ctx, boss.ID, p1.ID, store.CooldownSet, 90)
	if err != nil || cp.Until == nil || cp.Until.Sub(time.Now().Add(90*time.Minute)).Abs() > 5*time.Second || cp.ManualUntil == nil {
		t.Fatalf("поставить вручную: %+v %v", cp, err)
	}
	if msg := e.joinMsg(sid, p1); !strings.Contains(msg, "Вы уже выступали") {
		t.Errorf("ручной кулдаун: %q", msg)
	}

	// Неверная длительность и неизвестное действие - отказ без правок.
	for _, c := range []struct {
		action  string
		minutes int
	}{{store.CooldownDuration, 0}, {store.CooldownSet, store.CooldownMaxMinutes + 1}, {"forever", 10}} {
		if _, err := e.st.ChangePlayerCooldown(e.ctx, boss.ID, p1.ID, c.action, c.minutes); !errors.Is(err, store.ErrCooldownInvalid) {
			t.Errorf("%s %d: %v", c.action, c.minutes, err)
		}
	}
	if err := e.st.SetCooldownDefault(e.ctx, boss.ID, -1); !errors.Is(err, store.ErrCooldownInvalid) {
		t.Errorf("отрицательная общая: %v", err)
	}
	if _, err := e.st.ChangePlayerCooldown(e.ctx, boss.ID, "нет-такого", store.CooldownClear, 0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("нет игрока: %v", err)
	}

	// Журнал помнит каждую правку: кто, кому, что.
	logs, err := e.st.CooldownLog(e.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range logs {
		if l.Actor == "Организатор" {
			seen[l.Action+":"+l.Label] = true
		}
	}
	for _, want := range []string{"default:", "duration:Ник p2", "disable:Ник p3", "enable:Ник p3", "clear:Ник p4", "set:Ник p1"} {
		if !seen[want] {
			t.Errorf("в журнале нет %q: %v", want, seen)
		}
	}

	// Раздел кабинета: без поиска - кто на кулдауне и у кого свои правила; поиск - по нику.
	ov, err := e.st.CooldownOverview(e.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]models.CooldownPlayer{}
	for _, p := range ov.Players {
		found[p.UserID] = p
	}
	if p, ok := found[p1.ID]; !ok || p.Until == nil || p.ManualUntil == nil {
		t.Errorf("p1 с ручным кулдауном: %+v", p)
	}
	if p, ok := found[p2.ID]; !ok || p.Minutes == nil {
		t.Errorf("p2 со своей длительностью: %+v", p)
	}
	if ov.DefaultMinutes != 0 {
		t.Errorf("общая длительность: %d", ov.DefaultMinutes)
	}
	ov, err = e.st.CooldownOverview(e.ctx, strings.ToUpper(p4.Login))
	if err != nil || len(ov.Players) != 1 || ov.Players[0].UserID != p4.ID || ov.Players[0].TotalMatches != 2 || ov.Players[0].TodayMatches != 2 {
		t.Errorf("поиск по нику: %+v %v", ov.Players, err)
	}
}

// Кто сегодня не играл - выше, между собой по времени записи; сыгравшие - по времени последнего матча.
// Отменённый матч и пропуск вызова выступлением не считаются.
func TestQueueOrderByLastMatch(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	u := map[string]models.User{}
	for _, tag := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		u[tag] = e.user(tag)
	}
	e.playedAt(u["A"], e.user("rA"), todayAt(0.1))
	e.playedAt(u["B"], e.user("rB"), todayAt(0.3))
	e.playedAt(u["F"], u["G"], todayAt(0.4)) // один матч - одно время
	e.playedAt(u["C"], e.user("rC"), todayAt(0.6))
	e.playedAt(u["E"], e.user("rE"), time.Now().Add(-72*time.Hour)) // играл, но не сегодня
	// Отменённый матч удаляется целиком: у H нет ни матча, ни кулдауна.
	cancelled, err := e.st.CreateMatch(e.ctx, store.NewMatch{Mode: "1x1", PlayerType: "pvp", Format: store.FormatMatch, Rounds: 2,
		Sides: [2]store.MatchSide{{UserID: u["H"].ID}, {UserID: e.user("rH").ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteTournament(e.ctx, cancelled.ID); err != nil {
		t.Fatal(err)
	}

	for _, tag := range []string{"C", "A", "D", "B", "E", "G", "F", "H"} {
		e.join(sid, u[tag])
	}
	wantOrder(t, e.order(sid), "Ник D", "Ник E", "Ник H", "Ник A", "Ник B", "Ник G", "Ник F", "Ник C")
	for name, want := range map[string]int{"Ник D": 0, "Ник E": 0, "Ник H": 0, "Ник A": 1, "Ник F": 1} {
		if en := e.entry(sid, name); en.TodayMatches != want {
			t.Errorf("%s: матчей сегодня %d, want %d", name, en.TodayMatches, want)
		}
	}
	if en := e.entry(sid, "Ник H"); !en.Newbie || en.LastMatchAt != nil {
		t.Errorf("H после отменённого матча: %+v", en)
	}

	// Пропуск вызова - не выступление: D остаётся среди не игравших, без матча и без кулдауна.
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SkipEntry(e.ctx, sid, "", e.entry(sid, "Ник D").ID, "end"); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, e.order(sid), "Ник E", "Ник H", "Ник D", "Ник A", "Ник B", "Ник G", "Ник F", "Ник C")
	e.cooldown(24 * 60) // сутки: все сегодняшние матчи ещё дают кулдаун, в какое бы время ни шёл тест
	if en := e.entry(sid, "Ник D"); en.TodayMatches != 0 || en.CooldownUntil != nil || !en.Newbie {
		t.Errorf("пропущенный: %+v", en)
	}
	// С кулдауном сыгравшие стоят на местах, но следующим идёт тот, кто сегодня не играл.
	if en := e.entry(sid, "Ник A"); en.CooldownUntil == nil || en.IsNext {
		t.Errorf("сыгравший при кулдауне: %+v", en)
	}
	if en := e.entry(sid, "Ник E"); !en.IsNext {
		t.Errorf("следующий не E: %+v", en)
	}
}

// Новичок - только тот, у кого нет ни одного завершённого матча; после первого матча метка уходит.
func TestQueueNewbieAndLastMatch(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	n, old, twice, lost, fixed := e.user("n"), e.user("old"), e.user("twice"), e.user("lost"), e.user("fixed")
	e.playedAt(old, e.user("r1"), time.Now().Add(-7*24*time.Hour))
	e.playedAt(twice, e.user("r2"), todayAt(0.2))
	lastToday := todayAt(0.5)
	e.playedAt(twice, e.user("r3"), lastToday)

	// lost играл под этим ником, но матч не привязан к аккаунту; у fixed - сверка рейтинга по таблице.
	other := e.user("other")
	tid := e.playedAt(other, e.user("r4"), time.Now().Add(-30*24*time.Hour))
	if _, err := e.pool.Exec(e.ctx, `UPDATE participants SET user_id = NULL, name = $2 WHERE tournament_id = $1 AND user_id = $3`,
		tid, strings.ToUpper(lost.Login), other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO mmr_corrections (user_id, delta, at, source) VALUES ($1, 5, now(), 'itest')`, fixed.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(e.ctx, `DELETE FROM mmr_corrections WHERE source = 'itest'`) })

	for _, x := range []models.User{n, old, twice, lost, fixed} {
		e.join(sid, x)
	}
	check := func(name string, newbie, unknown bool, today, total int, last *time.Time) {
		t.Helper()
		en := e.entry(sid, name)
		m := en.Members[0]
		okLast := (last == nil) == (en.LastMatchAt == nil) && (last == nil || sameTime(en.LastMatchAt, *last))
		if en.Newbie != newbie || en.HistoryUnknown != unknown || en.TodayMatches != today || m.TotalMatches != total || !okLast ||
			m.Newbie != newbie || m.HistoryUnknown != unknown {
			t.Errorf("%s: newbie=%v unknown=%v сегодня=%d всего=%d последний=%v", name, en.Newbie, en.HistoryUnknown, en.TodayMatches, m.TotalMatches, en.LastMatchAt)
		}
	}
	week := time.Now().Add(-7 * 24 * time.Hour)
	check("Ник n", true, false, 0, 0, nil)
	check("Ник old", false, false, 0, 1, &week) // играл неделю назад - не новичок
	check("Ник twice", false, false, 2, 2, &lastToday)
	check("Ник lost", false, true, 0, 0, nil) // матчи есть, но не на аккаунте - не новичок
	check("Ник fixed", false, true, 0, 0, nil)

	// Первый матч - метка «новичок» уходит, видны дата и время этого матча.
	_, at := e.played(n, e.user("r5"))
	check("Ник n", false, false, 1, 1, &at)
}

// 2×2: команда стоит по самому недавнему матчу из двух игроков, кулдаун любого из них не даёт её вызвать.
func TestQueueTeamsOrderAndCooldown(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("2x2")
	a, b, c, d, f, g := e.user("a"), e.user("b"), e.user("c"), e.user("d"), e.user("f"), e.user("g")
	dAt := todayAt(0.3)
	e.playedAt(b, e.user("rb"), todayAt(0.6))
	e.playedAt(d, e.user("rd"), dAt)
	e.playedAt(c, e.user("rc"), todayAt(0.1)) // у «Ранних» последний матч - у d, а не у c
	for _, p := range []struct {
		name string
		x, y models.User
	}{{"Поздние", a, b}, {"Ранние", c, d}, {"Свежие", f, g}} {
		tm, err := e.st.CreateTeam(e.ctx, p.name, p.x.ID, p.y.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.JoinQueue(e.ctx, sid, p.x.ID, store.QueueJoin{UserID: p.x.ID, TeamKey: tm.TeamKey}); err != nil {
			t.Fatal(err)
		}
	}
	wantOrder(t, e.order(sid), "Свежие", "Ранние", "Поздние")
	if en := e.entry(sid, "Ранние"); en.TodayMatches != 1 || !sameTime(en.LastMatchAt, dAt) {
		t.Errorf("команда по самому недавнему: %+v", en)
	}

	// Кулдаун у одного игрока - команду не вызывают, следующей идёт та, у кого кулдауна нет.
	e.cooldown(24 * 60)
	if en := e.entry(sid, "Поздние"); en.CooldownUntil == nil || en.IsNext {
		t.Errorf("команда с кулдауном: %+v", en)
	}
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	if en := e.entry(sid, "Свежие"); en.Status != store.QEntryInvited {
		t.Errorf("вызвали не свежих: %+v", en)
	}
	_, err := e.st.CallEntry(e.ctx, sid, "", e.entry(sid, "Ранние").ID)
	if msg := refusal(t, err); !strings.Contains(msg, "кулдаун до") {
		t.Errorf("вызов команды с кулдауном: %q", msg)
	}
}

// Игрок в идущем матче не встаёт в очередь, пока матч не кончится.
func TestQueueInMatchBlocksJoin(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	a, b := e.user("a"), e.user("b")
	live, err := e.st.CreateMatch(e.ctx, store.NewMatch{Mode: "1x1", PlayerType: "pvp", Format: store.FormatMatch, Rounds: 2,
		Sides: [2]store.MatchSide{{UserID: a.ID}, {UserID: b.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.st.DeleteTournament(e.ctx, live.ID) })
	if msg := e.joinMsg(sid, a); !strings.Contains(msg, "Вы сейчас в матче") {
		t.Errorf("игрок в матче: %q", msg)
	}
	if me := e.snap(sid, a.ID).Me; !me.InMatch || me.CanJoin {
		t.Errorf("me игрока в матче: %+v", me)
	}
	_, err = e.st.JoinQueue(e.ctx, sid, "", store.QueueJoin{UserID: b.ID, ByOrganizer: true})
	if msg := refusal(t, err); !strings.Contains(msg, "Игрок Ник b сейчас в матче") {
		t.Errorf("организатор добавляет игрока в матче: %q", msg)
	}
	// Отменённый матч ни кулдауна, ни истории не оставляет.
	if err := e.st.DeleteTournament(e.ctx, live.ID); err != nil {
		t.Fatal(err)
	}
	e.cooldown(180)
	e.join(sid, a)
	if en := e.entry(sid, "Ник a"); !en.Newbie || en.CooldownUntil != nil {
		t.Errorf("после отмены: %+v", en)
	}
}
