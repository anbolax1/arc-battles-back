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

// Задание на двух картах выпадает на любой из них и в матче один раз, даже если играются обе карты.
// Запуск - как у TestMMRIntegration.
func TestMultiMapTaskIntegration(t *testing.T) {
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

	// Свои карты без других заданий: в слот карты может выпасть только это задание. Карты выключены,
	// чтобы не попасть в пики-баны.
	uniq := time.Now().UnixNano()
	maps := []string{fmt.Sprintf("mm_a_%d", uniq), fmt.Sprintf("mm_b_%d", uniq)}
	var tourID, taskID string
	defer func() {
		if tourID != "" {
			_ = st.DeleteTournament(ctx, tourID)
		}
		if taskID != "" {
			_ = st.DeleteCatalogTask(ctx, taskID)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM maps WHERE code = ANY($1)`, maps)
	}()

	for i, code := range maps {
		if _, err := pool.Exec(ctx, `INSERT INTO maps (code, name, sort_order, active) VALUES ($1, $1, $2, false)`, code, 1000+i); err != nil {
			t.Fatalf("карта %s: %v", code, err)
		}
	}
	task, err := st.CreateCatalogTask(ctx, models.CatalogTask{
		Name: fmt.Sprintf("mm-%d", uniq), Text: "Одно задание на две карты.", Points: 2, ValueType: models.ValueFixed,
		Kind: "pvpve", Source: "official", Category: "task", MapCodes: []string{maps[1], "no_such_map", maps[0], maps[1]}, Active: true,
	})
	if err != nil {
		t.Fatalf("CreateCatalogTask: %v", err)
	}
	taskID = task.ID
	if got := fmt.Sprint(task.MapCodes); got != fmt.Sprint(maps) {
		t.Fatalf("карты задания = %s, want %s (без лишних и повторов, в порядке карт)", got, fmt.Sprint(maps))
	}

	tour, err := st.CreateTournament(ctx, models.Tournament{Title: "mm-two-maps", Mode: "1x1", PlayerType: "pvp"})
	if err != nil {
		t.Fatalf("CreateTournament: %v", err)
	}
	tourID = tour.ID
	for i, name := range []string{"mm-a", "mm-b"} {
		if _, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Name: name, Seed: i + 1}); err != nil {
			t.Fatalf("AddParticipant(%s): %v", name, err)
		}
	}
	// Первый раунд - на второй карте задания: подходит любая карта из списка, а не только первая.
	for i, code := range []string{maps[1], maps[0]} {
		rd, err := st.CreateRound(ctx, models.Round{TournamentID: tour.ID, Number: i + 1})
		if err != nil {
			t.Fatalf("CreateRound(%d): %v", i+1, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE rounds SET map_code = $2 WHERE id = $1`, rd.ID, code); err != nil {
			t.Fatalf("карта раунда %d: %v", i+1, err)
		}
		if err := st.DealRoundTasks(ctx, rd.ID); err != nil {
			t.Fatalf("DealRoundTasks(%d): %v", i+1, err)
		}
	}

	dealt, err := st.ListTournamentBonusTasks(ctx, tour.ID)
	if err != nil {
		t.Fatalf("ListTournamentBonusTasks: %v", err)
	}
	var got []string
	for _, d := range dealt {
		if d.TaskID == task.ID {
			got = append(got, fmt.Sprintf("раунд %d, карта %s", d.RoundNumber, d.MapCode))
		}
	}
	if want := fmt.Sprintf("раунд 1, карта %s", maps[1]); len(got) != 1 || got[0] != want {
		t.Errorf("задание выдано: %v, want [%s]", got, want)
	}
}
