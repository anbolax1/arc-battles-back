package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Матч на один раунд: карта выпадает случайно из пула при создании, пиков-банов нет, матч сразу готов к старту.
// Запуск - как у TestMMRIntegration.
func TestSingleRoundMatchIntegration(t *testing.T) {
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
		login := fmt.Sprintf("sr_%d_%s", uniq, tag)
		if users[i], err = st.CreateUser(ctx, login, login, "x", models.RoleUser); err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
	}
	tour, err := st.CreateMatch(ctx, store.NewMatch{Format: store.FormatMatch, Rounds: 1,
		Sides: [2]store.MatchSide{{UserID: users[0].ID}, {UserID: users[1].ID}}})
	if err != nil {
		t.Fatalf("CreateMatch: %v", err)
	}
	defer func() { _ = st.DeleteTournament(ctx, tour.ID) }()

	if tour.TotalRounds != 1 || len(tour.Rounds) != 1 {
		t.Fatalf("раундов %d (в списке %d), want 1", tour.TotalRounds, len(tour.Rounds))
	}
	if r := tour.Rounds[0]; r.MapCode == "" || r.Map == "" {
		t.Errorf("карта раунда не выбрана: %+v", r)
	}

	state, err := st.GetMatchState(ctx, tour.ID)
	if err != nil {
		t.Fatalf("GetMatchState: %v", err)
	}
	if state.Stage != "ready" || len(state.VetoOrder) != 0 || len(state.Veto) != 0 {
		t.Errorf("стадия %q, пиков-банов %d/%d: ждали готовый к старту матч без пиков-банов", state.Stage, len(state.VetoOrder), len(state.Veto))
	}

	maps, err := st.ListMaps(ctx)
	if err != nil || len(maps) == 0 {
		t.Fatalf("карты: %d, %v", len(maps), err)
	}
	if err := st.VetoMap(ctx, tour.ID, maps[0].Code); !errors.Is(err, store.ErrConflict) {
		t.Errorf("пик в матче на один раунд: %v, want ErrConflict", err)
	}
	if n, err := st.StartNextRound(ctx, tour.ID); err != nil || n != 1 {
		t.Errorf("старт раунда: %d, %v", n, err)
	}
}
