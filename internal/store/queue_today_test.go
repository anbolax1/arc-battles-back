package store_test

import (
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// playedMatch - обычный матч сайта между двумя игроками, уже завершённый. daysAgo > 0 сдвигает его в прошлое.
func (e *queueEnv) playedMatch(a, b models.User, daysAgo int) {
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
	if daysAgo > 0 {
		if _, err := e.pool.Exec(e.ctx, `UPDATE tournaments SET created_at = now() - make_interval(days => $2),
				starts_at = CASE WHEN starts_at IS NULL THEN NULL ELSE now() - make_interval(days => $2) END WHERE id = $1`, tm.ID, daysAgo); err != nil {
			e.t.Fatal(err)
		}
	}
}

// Новичок встаёт перед всеми, кто сегодня уже играл, где бы ни играл; вчерашние матчи не считаются.
func TestQueueNewcomerGoesBeforeTodaysPlayers(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	x, y, w, v := e.user("x"), e.user("y"), e.user("w"), e.user("v")
	played, old1, old2 := e.user("p"), e.user("o1"), e.user("o2")

	// «p» сегодня уже сыграл матч вне очереди, «o1» и «o2» играли только вчера.
	e.playedMatch(played, e.user("m1"), 0)
	e.playedMatch(old1, old2, 1)

	e.join(sid, x)
	e.join(sid, played)
	e.join(sid, y)
	wantOrder(t, e.order(sid), "Ник x", "Ник y", "Ник p") // сыгравший сегодня встал за теми, кто нет
	e.join(sid, w)
	wantOrder(t, e.order(sid), "Ник x", "Ник y", "Ник w", "Ник p") // новичок - перед сыгравшим
	e.join(sid, old1)
	wantOrder(t, e.order(sid), "Ник x", "Ник y", "Ник w", "Ник o1", "Ник p") // вчерашний матч не в счёт

	// В списке видно, сколько матчей человек сыграл сегодня.
	got := map[string]int{}
	for _, en := range e.snap(sid, "").Entries {
		got[en.Name] = en.TodayMatches
		if en.Members[0].Today != en.TodayMatches {
			t.Errorf("%s: у игрока %d, у записи %d", en.Name, en.Members[0].Today, en.TodayMatches)
		}
	}
	if got["Ник p"] != 1 || got["Ник x"] != 0 || got["Ник o1"] != 0 {
		t.Errorf("матчей за сегодня: %v", got)
	}

	// Сыграл матч из очереди - счёт растёт, и новичок снова обходит его.
	e.callAndConfirm(sid, x, y)
	if _, err := e.st.StartQueueMatch(e.ctx, sid, "", store.QueueMatchStart{}); err != nil {
		t.Fatal(err)
	}
	e.finishMatch(sid)
	wantOrder(t, e.order(sid), "Ник w", "Ник o1", "Ник p", "Ник x", "Ник y")
	e.join(sid, v)
	wantOrder(t, e.order(sid), "Ник w", "Ник o1", "Ник v", "Ник p", "Ник x", "Ник y")
	for _, en := range e.snap(sid, "").Entries {
		got[en.Name] = en.TodayMatches
	}
	if got["Ник x"] != 1 || got["Ник y"] != 1 || got["Ник p"] != 1 {
		t.Errorf("после матча: %v", got)
	}

	// Вышел и записался заново - всё равно позади тех, кто сегодня не играл.
	if _, err := e.st.LeaveQueue(e.ctx, sid, x.ID); err != nil {
		t.Fatal(err)
	}
	e.join(sid, x)
	wantOrder(t, e.order(sid), "Ник w", "Ник o1", "Ник v", "Ник p", "Ник y", "Ник x")
}

// В 2×2 команда считается по самому игравшему из двух: напарник, игравший сегодня, ставит команду позади.
func TestQueueTeamUsesMostPlayedMember(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("2x2")
	a, b, c, d := e.user("a"), e.user("b"), e.user("c"), e.user("d")
	e.playedMatch(b, e.user("m"), 0)
	one, err := e.st.CreateTeam(e.ctx, "Игравшие", a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	two, err := e.st.CreateTeam(e.ctx, "Свежие", c.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range []struct {
		u models.User
		k string
	}{{a, one.TeamKey}, {c, two.TeamKey}} {
		if _, err := e.st.JoinQueue(e.ctx, sid, j.u.ID, store.QueueJoin{UserID: j.u.ID, TeamKey: j.k}); err != nil {
			t.Fatal(err)
		}
	}
	wantOrder(t, e.order(sid), "Свежие", "Игравшие")
	for _, en := range e.snap(sid, "").Entries {
		want := 0
		if en.Name == "Игравшие" {
			want = 1
		}
		if en.TodayMatches != want {
			t.Errorf("%s: матчей сегодня %d, want %d", en.Name, en.TodayMatches, want)
		}
	}
}
