package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Ведущий меняет, кто первым ходит в пиках-банах: сделанные ходы переходят вместе со стороной A, после
// начала раунда очерёдность не меняется. Запуск - как у TestMMRIntegration.
func TestFirstSideIntegration(t *testing.T) {
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
	var users [2]models.User
	for i, tag := range []string{"a", "b"} {
		login := fmt.Sprintf("fs_%d_%s", uniq, tag)
		if users[i], err = st.CreateUser(ctx, login, login, "x", models.RoleUser); err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
	}
	at := time.Now().Add(24 * time.Hour)
	tour, err := st.CreateMatch(ctx, store.NewMatch{Format: store.FormatShow, StartsAt: &at,
		Sides: [2]store.MatchSide{{UserID: users[0].ID}, {UserID: users[1].ID}}})
	if err != nil {
		t.Fatalf("CreateMatch: %v", err)
	}
	defer func() { _ = st.DeleteTournament(ctx, tour.ID) }()

	sides := func() ([]models.Participant, string) {
		t.Helper()
		got, err := st.GetTournament(ctx, tour.ID)
		if err != nil {
			t.Fatalf("GetTournament: %v", err)
		}
		parts := append([]models.Participant(nil), got.Participants...)
		sort.SliceStable(parts, func(i, j int) bool { return parts[i].Seed < parts[j].Seed })
		return parts, got.Title
	}
	parts, _ := sides()
	a, b := parts[0], parts[1]

	maps, err := st.ListMaps(ctx)
	if err != nil || len(maps) < 2 {
		t.Fatalf("карты: %d, %v", len(maps), err)
	}
	for _, m := range maps[:2] {
		if err := st.VetoMap(ctx, tour.ID, m.Code); err != nil {
			t.Fatalf("VetoMap(%s): %v", m.Code, err)
		}
	}

	if err := st.SetFirstSide(ctx, tour.ID, b.ID); err != nil {
		t.Fatalf("SetFirstSide: %v", err)
	}
	parts, title := sides()
	if parts[0].ID != b.ID || parts[1].ID != a.ID {
		t.Fatalf("первым ходит %s, want %s", parts[0].Name, b.Name)
	}
	if want := b.Name + " vs " + a.Name; title != want {
		t.Errorf("название %q, want %q", title, want)
	}
	veto, err := st.ListVeto(ctx, tour.ID)
	if err != nil {
		t.Fatalf("ListVeto: %v", err)
	}
	if len(veto) != 2 || veto[0].Side != "A" || veto[0].MapCode != maps[0].Code || veto[1].Side != "B" {
		t.Errorf("ходы %+v: первый бан остаётся за стороной A, теперь это %s", veto, b.Name)
	}

	if err := st.SetFirstSide(ctx, tour.ID, b.ID); err != nil {
		t.Fatalf("повторный SetFirstSide: %v", err)
	}
	if parts, _ := sides(); parts[0].ID != b.ID {
		t.Errorf("повтор поменял стороны обратно")
	}
	if err := st.SetFirstSide(ctx, tour.ID, "no-such-side"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("чужая сторона: %v, want ErrNotFound", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE tournaments SET title = 'fs-своё' WHERE id = $1`, tour.ID); err != nil {
		t.Fatalf("название: %v", err)
	}
	if err := st.SetFirstSide(ctx, tour.ID, a.ID); err != nil {
		t.Fatalf("SetFirstSide обратно: %v", err)
	}
	if parts, title := sides(); parts[0].ID != a.ID || title != "fs-своё" {
		t.Errorf("первым %s, название %q: своё название не трогаем", parts[0].Name, title)
	}

	if _, err := pool.Exec(ctx, `UPDATE rounds SET status = 'live' WHERE tournament_id = $1 AND number = 1`, tour.ID); err != nil {
		t.Fatalf("раунд: %v", err)
	}
	if err := st.SetFirstSide(ctx, tour.ID, b.ID); !errors.Is(err, store.ErrMatchStarted) {
		t.Errorf("после начала раунда: %v, want ErrMatchStarted", err)
	}
}
