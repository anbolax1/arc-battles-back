package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// joinMsg - попытка игрока записаться сам; возвращает текст отказа (пусто - записался).
func (e *queueEnv) joinMsg(sid string, u models.User) string {
	e.t.Helper()
	_, err := e.st.JoinQueue(e.ctx, sid, u.ID, store.QueueJoin{UserID: u.ID})
	if err == nil {
		return ""
	}
	var qe *store.QueueError
	if !errors.Is(err, store.ErrQueueState) || !errors.As(err, &qe) {
		e.t.Fatalf("неожиданная ошибка: %v", err)
	}
	return qe.Msg
}

// В очередь встаёт только тот, у кого подтверждены и часы, и ноки.
func TestQueueRequiresConfirmedStats(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("1x1")
	p := e.userNoStats("p")

	msg := e.joinMsg(sid, p)
	if !strings.Contains(msg, "заполните в профиле часы и ноки") || !strings.Contains(msg, "часы не заполнены") || !strings.Contains(msg, "ноки не заполнены") {
		t.Errorf("пустой профиль: %q", msg)
	}
	me := e.snap(sid, p.ID).Me
	if me.CanJoin || !me.NeedsStats || me.JoinBlocked != msg {
		t.Errorf("me без часов и ноков: %+v", me)
	}
	if len(e.snap(sid, "").Entries) != 0 {
		t.Fatal("отказ оставил запись")
	}

	// Заявка есть, но модератор ещё не проверил.
	e.claim(p, "hours", 120, "pending")
	e.claim(p, "knocks", 3000, "pending")
	if msg := e.joinMsg(sid, p); !strings.Contains(msg, "часы ждут проверки модератора") || !strings.Contains(msg, "ноки ждут проверки модератора") {
		t.Errorf("заявки на проверке: %q", msg)
	}

	// Часы подтверждены, ноки отклонены - всё равно нельзя.
	e.claim(p, "hours", 120, "approved")
	e.claim(p, "knocks", 3000, "rejected")
	msg = e.joinMsg(sid, p)
	if strings.Contains(msg, "часы не") || strings.Contains(msg, "часы ждут") || strings.Contains(msg, "часы откл") ||
		!strings.Contains(msg, "ноки отклонены модератором") {
		t.Errorf("часы есть, ноки отклонены: %q", msg)
	}

	// Подтверждённое число остаётся в силе, даже если игрок прислал новое на проверку.
	e.claim(p, "knocks", 3100, "approved")
	e.claim(p, "knocks", 3500, "pending")
	if msg := e.joinMsg(sid, p); msg != "" {
		t.Fatalf("всё подтверждено, а запись отказана: %q", msg)
	}
	if me := e.snap(sid, p.ID).Me; me.NeedsStats || me.Position != 1 {
		t.Errorf("me после записи: %+v", me)
	}
	// Ведущему видны подтверждённые числа, а не те, что ещё на проверке.
	m := e.snap(sid, "").Entries[0].Members[0]
	if m.Hours == nil || *m.Hours != 120 || m.Knocks == nil || *m.Knocks != 3100 {
		t.Errorf("часы и ноки в очереди: %+v / %+v", m.Hours, m.Knocks)
	}

	// Организатор добавляет и без часов и ноков: решение за ним, а у человека в списке пустые числа.
	q := e.userNoStats("q")
	if _, err := e.st.JoinQueue(e.ctx, sid, "", store.QueueJoin{UserID: q.ID, ByOrganizer: true}); err != nil {
		t.Fatalf("ручное добавление: %v", err)
	}
	last := e.snap(sid, "").Entries[1].Members[0]
	if last.Hours != nil || last.Knocks != nil {
		t.Errorf("у добавленного вручную нет подтверждённых чисел: %+v / %+v", last.Hours, last.Knocks)
	}
}

// В 2×2 часы и ноки нужны обоим игрокам команды; игрок видит, у кого не хватает.
func TestQueueRequiresConfirmedStatsInTeam(t *testing.T) {
	e := newQueueEnv(t)
	sid := e.session("2x2")
	a := e.user("a")
	b := e.userNoStats("b")
	team, err := e.st.CreateTeam(e.ctx, "Пара", a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.st.JoinQueue(e.ctx, sid, a.ID, store.QueueJoin{UserID: a.ID, TeamKey: team.TeamKey})
	var qe *store.QueueError
	if !errors.Is(err, store.ErrQueueState) || !errors.As(err, &qe) || !strings.Contains(qe.Msg, "У игрока Ник b") {
		t.Fatalf("напарник без чисел: %v", err)
	}
	me := e.snap(sid, a.ID).Me
	if me.CanJoin || me.NeedsStats || len(me.Teams) != 1 || !strings.Contains(me.Teams[0].Blocked, "Ник b") || me.JoinBlocked == "" {
		t.Errorf("me игрока, у которого всё в порядке: %+v", me)
	}
	// Сам напарник видит, что заполнить нужно ему.
	if me := e.snap(sid, b.ID).Me; !me.NeedsStats || me.CanJoin || !strings.Contains(me.JoinBlocked, "заполните в профиле") {
		t.Errorf("me напарника: %+v", me)
	}

	e.claim(b, "hours", 90, "approved")
	e.claim(b, "knocks", 2000, "approved")
	if _, err := e.st.JoinQueue(e.ctx, sid, a.ID, store.QueueJoin{UserID: a.ID, TeamKey: team.TeamKey}); err != nil {
		t.Fatalf("после подтверждения: %v", err)
	}
	for _, m := range e.snap(sid, "").Entries[0].Members {
		if m.Hours == nil || m.Knocks == nil {
			t.Errorf("числа игрока команды: %+v", m)
		}
	}
}
