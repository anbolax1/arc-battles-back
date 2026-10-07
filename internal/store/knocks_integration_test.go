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

// Ноки в пульте и статистика ноков игрока против живой Postgres. Запуск - как у TestMMRIntegration.
func TestKnocksIntegration(t *testing.T) {
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
		login := fmt.Sprintf("kn_%d_%s", uniq, tag)
		u, err := st.CreateUser(ctx, login, login, "x", models.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
		return u
	}
	type side struct {
		pid   string
		round string
	}
	// match1x1 - завершённый матч из одного раунда; ноки и поправки вносятся до завершения.
	match1x1 := func(a, b models.User, play func(sa, sb side)) string {
		tour, err := st.CreateTournament(ctx, models.Tournament{Title: "kn-1x1", Mode: "1x1", RatingMultiplier: 1})
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
		play(side{pa.ID, rd.ID}, side{pb.ID, rd.ID})
		if _, err := st.UpdateTournamentStatus(ctx, tour.ID, "finished"); err != nil {
			t.Fatalf("UpdateTournamentStatus: %v", err)
		}
		return tour.ID
	}
	knock := func(s side, delta, wantApplied int) {
		t.Helper()
		got, err := st.AdjustKnocks(ctx, s.round, s.pid, delta)
		if err != nil {
			t.Fatalf("AdjustKnocks: %v", err)
		}
		if got != wantApplied {
			t.Errorf("AdjustKnocks(%d) = %d, want %d", delta, got, wantApplied)
		}
	}
	points := func(s side, delta, wantApplied int) {
		t.Helper()
		got, err := st.AdjustRoundPoints(ctx, s.round, s.pid, delta)
		if err != nil {
			t.Fatalf("AdjustRoundPoints: %v", err)
		}
		if got != wantApplied {
			t.Errorf("AdjustRoundPoints(%d) = %d, want %d", delta, got, wantApplied)
		}
	}
	entry := func(s side, wantPoints, wantKnocks int) {
		t.Helper()
		var p, k int
		if err := pool.QueryRow(ctx, `SELECT points, knocks FROM round_entries WHERE round_id = $1 AND participant_id = $2`,
			s.round, s.pid).Scan(&p, &k); err != nil {
			t.Fatalf("round_entries: %v", err)
		}
		if p != wantPoints || k != wantKnocks {
			t.Errorf("очки/ноки = %d/%d, want %d/%d", p, k, wantPoints, wantKnocks)
		}
	}
	knocksOf := func(u models.User) models.KnockStats {
		t.Helper()
		by, err := st.Player1x1KnocksBySeason(ctx, u.ID)
		if err != nil {
			t.Fatalf("Player1x1KnocksBySeason: %v", err)
		}
		var sum models.KnockStats
		for _, k := range by {
			sum.Matches += k.Matches
			sum.Knocks += k.Knocks
			if k.Best > sum.Best {
				sum.Best, sum.BestMatch, sum.BestOpponent = k.Best, k.BestMatch, k.BestOpponent
			}
		}
		return sum
	}

	a, b, c := mkUser("a"), mkUser("b"), mkUser("c")

	// Нок приносит 3 очка, поправка не съедает очки за ноки, снять больше ноков, чем есть, нельзя.
	m1 := match1x1(a, b, func(sa, _ side) {
		knock(sa, 1, 1)
		knock(sa, 1, 1)
		knock(sa, 1, 1)
		entry(sa, 9, 3)
		points(sa, -1, 0)
		points(sa, 1, 1)
		points(sa, -1, -1)
		knock(sa, -5, -3)
		entry(sa, 0, 0)
		knock(sa, 1, 1)
		knock(sa, 1, 1)
		points(sa, 1, 1)
		entry(sa, 7, 2)
	})
	// Ни одного нока у обеих сторон - ноки в матче не считали, в статистику он не идёт.
	match1x1(a, c, func(_, _ side) {})
	// Ноки только у соперника: матч идёт в зачёт и стороне без ноков - с нулём.
	m3 := match1x1(b, c, func(_, sc side) {
		for range 4 {
			knock(sc, 1, 1)
		}
	})

	if got, want := knocksOf(a), (models.KnockStats{Matches: 1, Knocks: 2, Best: 2, BestMatch: m1, BestOpponent: b.Login}); got != want {
		t.Errorf("ноки a = %+v, want %+v", got, want)
	}
	if got, want := knocksOf(b), (models.KnockStats{Matches: 2}); got != want {
		t.Errorf("ноки b = %+v, want %+v", got, want)
	}
	if got, want := knocksOf(c), (models.KnockStats{Matches: 1, Knocks: 4, Best: 4, BestMatch: m3, BestOpponent: b.Login}); got != want {
		t.Errorf("ноки c = %+v, want %+v", got, want)
	}
}
