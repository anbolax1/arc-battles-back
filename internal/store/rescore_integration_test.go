package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Исправление счёта завершённого матча против живой Postgres. Запуск - как у TestMMRIntegration.
func TestRescoreFinishedMatchIntegration(t *testing.T) {
	url := os.Getenv("RESPECT_TEST_DB")
	if url == "" {
		t.Skip("RESPECT_TEST_DB не задан")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer pool.Close()
	st := store.New(pool)

	uniq := time.Now().UnixNano()
	mkUser := func(tag string) models.User {
		login := fmt.Sprintf("rs_%d_%s", uniq, tag)
		u, err := st.CreateUser(ctx, login, login, "x", models.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
		return u
	}
	knock := func(roundID, participantID string) {
		t.Helper()
		if _, err := st.AdjustKnocks(ctx, roundID, participantID, 1); err != nil {
			t.Fatalf("AdjustKnocks: %v", err)
		}
	}
	type match struct{ id, round, pa, pb string }
	// play - матч 1×1 из одного раунда, сыгранный через пульт: ноки сторон и завершение.
	play := func(a, b models.User, knocksA, knocksB int) match {
		tour, err := st.CreateTournament(ctx, models.Tournament{Title: "rs-1x1", Mode: "1x1", RatingMultiplier: 1})
		if err != nil {
			t.Fatalf("CreateTournament: %v", err)
		}
		rd, err := st.CreateRound(ctx, models.Round{TournamentID: tour.ID, Number: 1, Status: "live"})
		if err != nil {
			t.Fatalf("CreateRound: %v", err)
		}
		pa, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "player", UserID: &a.ID, Name: a.Login, Seed: 1})
		if err != nil {
			t.Fatalf("AddParticipant a: %v", err)
		}
		pb, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "player", UserID: &b.ID, Name: b.Login, Seed: 2})
		if err != nil {
			t.Fatalf("AddParticipant b: %v", err)
		}
		m := match{tour.ID, rd.ID, pa.ID, pb.ID}
		for range knocksA {
			knock(m.round, m.pa)
		}
		for range knocksB {
			knock(m.round, m.pb)
		}
		if _, err := st.FinishMatch(ctx, tour.ID); err != nil {
			t.Fatalf("FinishMatch: %v", err)
		}
		return m
	}
	winner := func(m match) string {
		t.Helper()
		var w *string
		if err := pool.QueryRow(ctx, `SELECT winner_participant_id FROM tournaments WHERE id = $1`, m.id).Scan(&w); err != nil {
			t.Fatalf("победитель: %v", err)
		}
		if w == nil {
			return ""
		}
		return *w
	}
	mmr := func(m match, u models.User) (before, after int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT mmr_before, mmr_after FROM mmr_history WHERE tournament_id = $1 AND user_id = $2`,
			m.id, u.ID).Scan(&before, &after); err != nil {
			t.Fatalf("mmr_history: %v", err)
		}
		return before, after
	}
	rescore := func(m match, wantRecomputed bool) {
		t.Helper()
		got, err := st.RescoreFinishedMatch(ctx, m.id)
		if err != nil {
			t.Fatalf("RescoreFinishedMatch: %v", err)
		}
		if got != wantRecomputed {
			t.Errorf("RescoreFinishedMatch пересчитал MMR = %v, want %v", got, wantRecomputed)
		}
	}

	a, b := mkUser("a"), mkUser("b")
	m1 := play(a, b, 2, 1)
	m2 := play(a, b, 1, 0)
	if winner(m1) != m1.pa {
		t.Fatalf("до правки первый матч должен выиграть a")
	}

	// Двух ноков b не досчитали - победа переходит к нему, а рейтинг перед вторым матчем меняется.
	knock(m1.round, m1.pb)
	knock(m1.round, m1.pb)
	rescore(m1, true)
	if winner(m1) != m1.pb {
		t.Errorf("после правки первый матч должен выиграть b")
	}
	_, aAfter1 := mmr(m1, a)
	aBefore2, _ := mmr(m2, a)
	if aBefore2 != aAfter1 {
		t.Errorf("MMR a перед вторым матчем = %d, want %d - итог первого матча после правки", aBefore2, aAfter1)
	}
	if b1, a1 := mmr(m1, a); a1 >= b1 {
		t.Errorf("a проиграл первый матч, а MMR %d → %d", b1, a1)
	}

	// Ещё нок без смены победителя: счёт обновляется, MMR не пересчитывается.
	knock(m1.round, m1.pb)
	rescore(m1, false)
	var pts int
	if err := pool.QueryRow(ctx, `SELECT total_points FROM participants WHERE id = $1`, m1.pb).Scan(&pts); err != nil {
		t.Fatalf("очки: %v", err)
	}
	if want := 4 * store.KnockPoints; pts != want {
		t.Errorf("очки b = %d, want %d", pts, want)
	}

	// Счёт сравнялся - ничья, начисления MMR за матч снимаются.
	for range 2 {
		if _, err := st.AdjustKnocks(ctx, m1.round, m1.pb, -1); err != nil {
			t.Fatalf("AdjustKnocks: %v", err)
		}
	}
	rescore(m1, true)
	if winner(m1) != "" {
		t.Errorf("при равном счёте победителя быть не должно")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mmr_history WHERE tournament_id = $1`, m1.id).Scan(&n); err != nil {
		t.Fatalf("mmr_history: %v", err)
	}
	if n != 0 {
		t.Errorf("у ничьей %d начислений MMR, want 0", n)
	}
}
