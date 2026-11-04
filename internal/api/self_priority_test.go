package api

import (
	"context"
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
)

// Игрок сам активирует «Без очереди»: встаёт в живой очереди выше остальных, повторно и без жетона - нельзя,
// отменить жетон сам не может (иначе поднялся бы и забрал жетон обратно).
func TestSelfActivatePriorityAPI(t *testing.T) {
	a := newQueueAPI(t)
	boss := a.actor("boss", models.RoleSuperadmin)
	p1, p2, p3 := a.actor("p1", models.RoleUser), a.actor("p2", models.RoleUser), a.actor("p3", models.RoleUser)
	var nobody actor

	snap := a.want(boss, "POST", "/queue/sessions", map[string]any{"format": "1x1", "title": "itest без очереди"}, 200)
	sid := sessionID(snap)
	for _, p := range []actor{p1, p2, p3} {
		a.want(p, "POST", "/queue/sessions/"+sid+"/join", nil, 200)
	}
	if got := entryNames(a.want(p3, "GET", "/queue", nil, 200)); got[0] != "Ник p1" || got[2] != "Ник p3" {
		t.Fatalf("исходный порядок: %v", got)
	}

	a.want(nobody, "POST", "/me/priority", nil, 401)
	a.want(p3, "POST", "/me/priority", nil, 409) // жетонов нет

	if _, err := a.st.Pool.Exec(context.Background(),
		`INSERT INTO token_ledger (user_id, kind, action, delta) VALUES ($1, 'priority', 'grant', 2)`, p3.user.ID); err != nil {
		t.Fatal(err)
	}
	wallet := a.want(p3, "POST", "/me/priority", nil, 200)
	if wallet["priorityUntil"] == nil || wallet["priority"] != float64(1) {
		t.Errorf("кошелёк после активации: %v", wallet)
	}
	if got := entryNames(a.want(p3, "GET", "/queue", nil, 200)); got[0] != "Ник p3" {
		t.Errorf("порядок после активации: %v", got)
	}
	a.want(p3, "POST", "/me/priority", nil, 409) // раз в сутки, хотя жетон ещё есть

	// Отменить сам не может; модератору и организатору это доступно.
	a.want(p3, "DELETE", "/users/"+p3.user.ID+"/tokens/priority", nil, 403)
	a.want(boss, "DELETE", "/users/"+p3.user.ID+"/tokens/priority", nil, 200)

	evs, err := a.st.QueueEvents(context.Background(), sid, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Kind == "priority" && ev.Label == "Ник p3" && ev.Actor == "Ник p3" {
			found = true
		}
	}
	if !found {
		t.Errorf("в журнале нет события «Без очереди» от самого игрока: %v", evs)
	}
	a.want(boss, "POST", "/queue/sessions/"+sid+"/finish", nil, 200)
}
