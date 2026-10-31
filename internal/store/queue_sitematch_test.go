package store_test

import (
	"errors"
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// openQueueMatch - матч очереди и привязанный к нему матч сайта.
func (e *queueEnv) openQueueMatch(sid string) (matchID, tournamentID, status string) {
	e.t.Helper()
	var tid *string
	if err := e.pool.QueryRow(e.ctx, `
		SELECT id, tournament_id, status FROM queue_matches WHERE session_id = $1 AND status IN ('preparing', 'live')`, sid).
		Scan(&matchID, &tid, &status); err != nil {
		e.t.Fatalf("матч очереди: %v", err)
	}
	if tid != nil {
		tournamentID = *tid
	}
	return
}

func (e *queueEnv) callAndConfirm(sid string, users ...models.User) {
	e.t.Helper()
	for _, u := range users {
		if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
			e.t.Fatal(err)
		}
		if _, err := e.st.ConfirmReady(e.ctx, sid, u.ID); err != nil {
			e.t.Fatal(err)
		}
	}
}

// Начало матча очереди создаёт настоящий матч сайта на двоих; он же держит очередь, пока идёт.
func TestQueueStartCreatesSiteMatch(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	a, b, c := e.user("a"), e.user("b"), e.user("c")
	for _, u := range []models.User{a, b, c} {
		e.join(sid, u)
	}

	// Без второго участника и без подтверждения матча не будет, настоящий матч не появляется.
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	_, err := e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{Force: true})
	wantKind(t, err, store.ErrQueueState)
	if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
		t.Fatal(err)
	}
	_, err = e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{})
	wantKind(t, err, store.ErrQueueState) // никто не подтвердил
	_, err = e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{Rounds: 4})
	wantKind(t, err, store.ErrQueueInvalid)
	_, err = e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{PlayerType: "solo"})
	wantKind(t, err, store.ErrQueueInvalid)
	var tournaments int
	_ = e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM tournaments WHERE status = 'live'`).Scan(&tournaments)
	if tournaments != 0 {
		t.Fatalf("отказ создал матч сайта: %d", tournaments)
	}

	for _, u := range []models.User{a, b} {
		if _, err := e.st.ConfirmReady(e.ctx, sid, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	res, err := e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{Rounds: 3, PlayerType: "pve"})
	if err != nil || res.MatchID == "" {
		t.Fatalf("StartQueueMatch: %+v %v", res, err)
	}

	// Матч сайта: текущий, 1×1, три раунда, двое выбранных игроков.
	tm, err := e.st.GetTournament(e.ctx, res.MatchID)
	if err != nil {
		t.Fatal(err)
	}
	if tm.Status != "live" || tm.Mode != "1x1" || tm.TotalRounds != 3 || tm.PlayerType != "pve" || len(tm.Participants) != 2 {
		t.Fatalf("матч сайта: %+v", tm)
	}
	got := map[string]bool{}
	for _, p := range tm.Participants {
		for _, id := range []string{a.ID, b.ID} {
			if p.UserID != nil && *p.UserID == id {
				got[id] = true
			}
		}
	}
	if !got[a.ID] || !got[b.ID] {
		t.Errorf("в матче не те игроки: %+v", tm.Participants)
	}
	s := e.snap(sid, "")
	if s.Match == nil || s.Match.Status != "live" || s.Match.TournamentID != res.MatchID || s.Entries[0].Status != "performing" {
		t.Errorf("очередь после старта: %+v", s.Match)
	}

	// Пока матч сайта идёт, вручную очередь не двигают, новых не вызывают.
	_, err = e.st.FinishQueueMatch(e.ctx, sid, "")
	wantKind(t, err, store.ErrQueueState)
	_, err = e.st.CancelQueueMatch(e.ctx, sid, "")
	wantKind(t, err, store.ErrQueueState)
	_, err = e.st.CallEntry(e.ctx, sid, "", "")
	wantKind(t, err, store.ErrQueueState)

	// Матч сайта завершён - очередь продвигается: двое в конец, счёт выступлений растёт.
	ref, ok := e.st.QueueMatchOfTournament(e.ctx, res.MatchID)
	if !ok || ref.SessionID != sid {
		t.Fatalf("матч очереди по матчу сайта: %+v %v", ref, ok)
	}
	if _, err := e.st.FinishMatch(e.ctx, res.MatchID); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := e.st.SettleQueueMatch(e.ctx, ref, "", true); err != nil || !changed {
		t.Fatalf("SettleQueueMatch: %v %v", changed, err)
	}
	wantOrder(t, e.order(sid), "Ник c", "Ник a", "Ник b")
	if _, changed, err := e.st.SettleQueueMatch(e.ctx, ref, "", true); err != nil || changed {
		t.Errorf("повторное продвижение изменило очередь: %v %v", changed, err)
	}
	s = e.snap(sid, "")
	if s.Last == nil || s.Last.TournamentID != res.MatchID || s.Entries[1].PerformedCount != 1 {
		t.Errorf("после матча: %+v", s.Last)
	}

	// Отмена матча сайта возвращает вызванных без «выступления».
	e.callAndConfirm(sid, c, a)
	res2, err := e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{})
	if err != nil {
		t.Fatal(err)
	}
	ref2, _ := e.st.QueueMatchOfTournament(e.ctx, res2.MatchID)
	if err := e.st.DeleteTournament(e.ctx, res2.MatchID); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := e.st.SettleQueueMatch(e.ctx, ref2, "", false); err != nil || !changed {
		t.Fatalf("SettleQueueMatch(cancel): %v %v", changed, err)
	}
	for _, en := range e.snap(sid, "").Entries {
		if en.Status != "waiting" {
			t.Errorf("после отмены %s: %s", en.Name, en.Status)
		}
		if en.Name == "Ник c" && en.PerformedCount != 0 {
			t.Errorf("отмена засчитала выступление: %+v", en)
		}
	}
	if _, _, st := func() (string, string, string) { return e.latestMatch(sid) }(); st != "cancelled" {
		t.Errorf("матч очереди после отмены: %s", st)
	}
}

func (e *queueEnv) latestMatch(sid string) (id, tid, status string) {
	e.t.Helper()
	var tp *string
	if err := e.pool.QueryRow(e.ctx, `SELECT id, tournament_id, status FROM queue_matches WHERE session_id = $1 ORDER BY number DESC LIMIT 1`, sid).Scan(&id, &tp, &status); err != nil {
		e.t.Fatal(err)
	}
	if tp != nil {
		tid = *tp
	}
	return
}

// Если матч сайта создать нельзя (идёт другой), очередь остаётся в подготовке и мусора не оставляет.
func TestQueueStartRefusedWhenAnotherMatchLive(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	a, b, x, y := e.user("a"), e.user("b"), e.user("x"), e.user("y")
	e.join(sid, a)
	e.join(sid, b)
	e.callAndConfirm(sid, a, b)

	other, err := e.st.CreateMatch(e.ctx, store.NewMatch{Mode: "1x1", PlayerType: "pvp", Format: store.FormatMatch, Rounds: 2,
		Sides: [2]store.MatchSide{{UserID: x.ID}, {UserID: y.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.st.DeleteTournament(e.ctx, other.ID) })

	_, err = e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{})
	wantKind(t, err, store.ErrQueueState)
	var qe *store.QueueError
	if !errors.As(err, &qe) || qe.Msg == "" {
		t.Errorf("нужен понятный текст отказа: %v", err)
	}
	if _, tid, st := e.latestMatch(sid); st != "preparing" || tid != "" {
		t.Errorf("матч очереди после отказа: %s %q", st, tid)
	}
	var n int
	_ = e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM tournaments WHERE status = 'live'`).Scan(&n)
	if n != 1 {
		t.Errorf("живых матчей сайта %d, want 1 (чужой)", n)
	}
	if got := e.status(sid, "Ник a"); got != "ready" {
		t.Errorf("статус после отказа %q, want ready", got)
	}

	// Чужой матч закончился - тот же старт проходит.
	if _, err := e.st.FinishMatch(e.ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{}); err != nil {
		t.Fatalf("после завершения чужого матча: %v", err)
	}
}

// В очереди 2×2 матч сайта создаётся для двух команд.
func TestQueueStartCreatesTeamMatch(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("2x2")
	a, b, c, d := e.user("a"), e.user("b"), e.user("c"), e.user("d")
	alpha, err := e.st.CreateTeam(e.ctx, "Альфа", a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := e.st.CreateTeam(e.ctx, "Бета", c.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range []struct {
		u models.User
		k string
	}{{a, alpha.TeamKey}, {c, beta.TeamKey}} {
		if _, err := e.st.JoinQueue(e.ctx, sid, j.u.ID, store.QueueJoin{UserID: j.u.ID, TeamKey: j.k}); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []models.User{a, c} {
		if _, err := e.st.CallEntry(e.ctx, sid, "", ""); err != nil {
			t.Fatal(err)
		}
		for _, m := range []models.User{a, b, c, d} {
			_, _ = e.st.ConfirmReady(e.ctx, sid, m.ID) // подтверждают оба игрока вызванной команды
		}
		_ = u
	}
	res, err := e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{})
	if err != nil {
		t.Fatal(err)
	}
	tm, err := e.st.GetTournament(e.ctx, res.MatchID)
	if err != nil {
		t.Fatal(err)
	}
	if tm.Mode != "2x2" || len(tm.Participants) != 2 {
		t.Fatalf("матч 2×2: %+v", tm)
	}
	names := map[string]bool{}
	for _, p := range tm.Participants {
		names[p.Name] = true
	}
	if !names["Альфа"] || !names["Бета"] {
		t.Errorf("в матче не те команды: %v", names)
	}
}
