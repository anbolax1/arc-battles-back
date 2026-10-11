package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Место и разбивка команды 2×2 по сезонам против реальной Postgres. Запуск:
//
//	RESPECT_TEST_DB=postgres://respect:respect@localhost:5433/respect_it go test ./internal/store -run TestTeamSeasonStats -v
//
// Без переменной - пропускается (в т.ч. в CI).
func TestTeamSeasonStats(t *testing.T) {
	url := os.Getenv("RESPECT_TEST_DB")
	if url == "" {
		t.Skip("RESPECT_TEST_DB не задан - интеграционный тест пропущен")
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
	first, err := st.StartNewSeason(ctx, fmt.Sprintf("it-%d", uniq), 32, 1000)
	if err != nil {
		t.Fatalf("StartNewSeason: %v", err)
	}
	if err := st.RefreshMmrCaches(ctx); err != nil {
		t.Fatalf("RefreshMmrCaches: %v", err)
	}
	mkUser := func(tag string) models.User {
		login := fmt.Sprintf("it_%d_%s", uniq, tag)
		u, err := st.CreateUser(ctx, login, login, "x", models.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
		return u
	}
	members := func(ids ...string) json.RawMessage {
		var arr []map[string]string
		for _, id := range ids {
			arr = append(arr, map[string]string{"userId": id, "name": id})
		}
		b, _ := json.Marshal(arr)
		return b
	}
	key := func(a, b models.User) string {
		ids := []string{a.ID, b.ID}
		sort.Strings(ids)
		return ids[0] + "|" + ids[1]
	}

	// (E,F) бьют (G,H) в первом сезоне.
	e, f, g, h := mkUser("e"), mkUser("f"), mkUser("g"), mkUser("h")
	tour, err := st.CreateTournament(ctx, models.Tournament{Title: "it-2x2", Mode: "2x2", RatingMultiplier: 1})
	if err != nil {
		t.Fatalf("CreateTournament: %v", err)
	}
	win, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "team", Name: "T1", Seed: 1, Members: members(e.ID, f.ID)})
	if err != nil {
		t.Fatalf("AddParticipant T1: %v", err)
	}
	if _, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "team", Name: "T2", Seed: 2, Members: members(g.ID, h.ID)}); err != nil {
		t.Fatalf("AddParticipant T2: %v", err)
	}
	if _, err := st.SetTournamentWinner(ctx, tour.ID, win.ID); err != nil {
		t.Fatalf("SetTournamentWinner: %v", err)
	}
	if err := st.ApplyTournamentMmr(ctx, tour.ID); err != nil {
		t.Fatalf("ApplyTournamentMmr: %v", err)
	}
	winner, loser := key(e, f), key(g, h)

	place := func(teamKey string) int {
		p, err := st.TeamPlace(ctx, teamKey)
		if err != nil {
			t.Fatalf("TeamPlace: %v", err)
		}
		return p
	}
	if got := place(winner); got != 1 {
		t.Errorf("место победителя в своём сезоне = %d, want 1", got)
	}
	if got := place(loser); got != 2 {
		t.Errorf("место проигравшего в своём сезоне = %d, want 2", got)
	}

	// Новый сезон: команды ещё не играли - в таблицу сезона не входят, разбивка прошлого сезона остаётся.
	if _, err := st.StartNewSeason(ctx, fmt.Sprintf("it-%d-next", uniq), 32, 1000); err != nil {
		t.Fatalf("StartNewSeason: %v", err)
	}
	if err := st.RefreshMmrCaches(ctx); err != nil {
		t.Fatalf("RefreshMmrCaches: %v", err)
	}
	if got := place(winner); got != 0 {
		t.Errorf("место в сезоне без матчей = %d, want 0", got)
	}
	tp, ok, err := st.TeamProfile(ctx, winner)
	if err != nil || !ok {
		t.Fatalf("TeamProfile: ok=%v err=%v", ok, err)
	}
	if tp.Stats.Place != 0 || tp.Stats.CurrentMmr != 1000 {
		t.Errorf("сводка в новом сезоне: место %d, MMR %d, want 0 и 1000", tp.Stats.Place, tp.Stats.CurrentMmr)
	}
	a, ok := tp.Analytics[first.ID]
	if !ok || len(a.Opponents) != 1 || a.Opponents[0].Wins != 1 || a.Opponents[0].TeamKey != loser {
		t.Errorf("разбивка первого сезона: %+v, want одна победа над %s", tp.Analytics, loser)
	}
}
