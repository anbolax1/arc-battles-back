package store_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Итоги каждого сезона сходятся с таблицей лидеров: MMR, победы и поражения у всех игроков. Только чтение,
// запуск - как у TestMMRIntegration.
func TestSeasonRecapMatchesLeaderboard(t *testing.T) {
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

	seasons, err := st.ListSeasons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sn := range seasons {
		rows, err := st.Leaderboard(ctx, "1x1", sn.ID)
		if err != nil {
			t.Fatal(err)
		}
		data, err := st.SeasonRecapJSON(ctx, sn)
		if err != nil {
			t.Fatalf("%s: %v", sn.Name, err)
		}
		var rec models.SeasonRecap
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatal(err)
		}
		if len(rec.Players) != len(rows) {
			t.Errorf("%s: игроков %d, в таблице %d", sn.Name, len(rec.Players), len(rows))
			continue
		}
		got := map[string]models.RecapPlayer{}
		for _, p := range rec.Players {
			got[p.Login] = p
		}
		for i, r := range rows {
			p, ok := got[r.Login]
			if !ok || p.Mmr != r.Mmr || p.Wins != r.Wins || p.Losses != r.Losses {
				t.Errorf("%s: %s в таблице %d MMR %d-%d, в итогах %+v", sn.Name, r.Login, r.Mmr, r.Wins, r.Losses, p)
			}
			if rec.Players[i].Mmr != r.Mmr {
				t.Errorf("%s: место %d - MMR %d, в таблице %d", sn.Name, i+1, rec.Players[i].Mmr, r.Mmr)
			}
		}
	}
}
