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

// У каждой стороны в раунде три задания, из них ровно одно на время; заданий на карту нет (отключены);
// задание на время чужой карты не выпадает; задание на время засчитывается только владельцу. Запуск - как у TestMMRIntegration.
func TestTimedSlotIntegration(t *testing.T) {
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
	prefix := fmt.Sprintf("tm-%d", uniq)
	roundMap := fmt.Sprintf("tm_a_%d", uniq)
	otherMap := fmt.Sprintf("tm_b_%d", uniq)
	var tourIDs []string
	defer func() {
		for _, id := range tourIDs {
			_ = st.DeleteTournament(ctx, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM catalog_tasks WHERE name LIKE $1`, prefix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM maps WHERE code IN ($1, $2)`, roundMap, otherMap)
	}()
	for i, code := range []string{roundMap, otherMap} {
		if _, err := pool.Exec(ctx, `INSERT INTO maps (code, name, sort_order, active) VALUES ($1, $1, $2, false)`, code, 3000+i); err != nil {
			t.Fatalf("карта %s: %v", code, err)
		}
	}
	// Задание на время только для чужой карты: на карте раунда его быть не должно.
	foreign, err := createTask(ctx, st, models.CatalogTask{
		Name: prefix + "-foreign", Text: "На время, чужая карта", Points: 1, ValueType: models.ValueFixed, Kind: "pvpve",
		Source: "official", Tag: models.TaskTagTimed, Active: true, MapCodes: []string{otherMap},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Задание на время для карты раунда: может выпасть как на время.
	if _, err := createTask(ctx, st, models.CatalogTask{
		Name: prefix + "-own", Text: "На время, карта раунда", Points: 1, ValueType: models.ValueFixed, Kind: "pvpve",
		Source: "official", Tag: models.TaskTagTimed, Active: true, MapCodes: []string{roundMap},
	}); err != nil {
		t.Fatal(err)
	}

	// Обычное задание на карту раунда: пока задания на карту отключены, оно не выдаётся.
	if _, err := createTask(ctx, st, models.CatalogTask{
		Name: prefix + "-map", Text: "Обычное, карта раунда", Points: 2, ValueType: models.ValueFixed, Kind: "pvpve",
		Source: "official", Active: true, MapCodes: []string{roundMap},
	}); err != nil {
		t.Fatal(err)
	}

	var lastTour, lastRound string
	for i := 0; i < 15; i++ {
		tour, err := st.CreateTournament(ctx, models.Tournament{Title: fmt.Sprintf("%s-%d", prefix, i), Mode: "1x1", PlayerType: "pvp"})
		if err != nil {
			t.Fatal(err)
		}
		tourIDs = append(tourIDs, tour.ID)
		for j, n := range []string{"p1", "p2"} {
			if _, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Name: n, Seed: j + 1}); err != nil {
				t.Fatal(err)
			}
		}
		rd, err := st.CreateRound(ctx, models.Round{TournamentID: tour.ID, Number: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE rounds SET map_code = $2 WHERE id = $1`, rd.ID, roundMap); err != nil {
			t.Fatal(err)
		}
		if err := st.DealRoundTasks(ctx, rd.ID); err != nil {
			t.Fatal(err)
		}
		dealt, err := st.ListTournamentBonusTasks(ctx, tour.ID)
		if err != nil {
			t.Fatal(err)
		}
		timed, normal := map[string]int{}, map[string]int{}
		for _, d := range dealt {
			if d.MapCode != "" {
				t.Fatalf("выдано задание на карту %s, а они отключены", d.MapCode)
			}
			if d.TaskID == foreign.ID {
				t.Fatalf("задание на время чужой карты выпало на карте раунда")
			}
			if d.Tag == models.TaskTagTimed {
				timed[d.ParticipantID]++
			} else {
				normal[d.ParticipantID]++
			}
		}
		if len(timed) != 2 {
			t.Fatalf("раздача %d: задание на время есть не у всех сторон: %v", i, timed)
		}
		for pid, n := range timed {
			if n != 1 || normal[pid] != 2 {
				t.Fatalf("раздача %d, сторона %s: на время %d, обычных %d (ждали 1 и 2)", i, pid, n, normal[pid])
			}
		}
		lastTour, lastRound = tour.ID, rd.ID
	}

	// Заменить задание на время можно только другим на время; обычное - обычным.
	dealt, err := st.ListTournamentBonusTasks(ctx, lastTour)
	if err != nil {
		t.Fatal(err)
	}
	var timedAssign, normalAssign *models.RoundBonusTask
	for i := range dealt {
		if dealt[i].Tag == models.TaskTagTimed && timedAssign == nil {
			timedAssign = &dealt[i]
		}
		if dealt[i].Tag != models.TaskTagTimed && normalAssign == nil {
			normalAssign = &dealt[i]
		}
	}
	if err := st.RerollTask(ctx, timedAssign.ID); err != nil {
		t.Fatalf("замена задания на время: %v", err)
	}
	after, _ := st.ListTournamentBonusTasks(ctx, lastTour)
	for _, d := range after {
		if d.ID == timedAssign.ID && d.Tag != models.TaskTagTimed {
			t.Errorf("задание на время заменилось обычным")
		}
	}

	// Чужой игрок задание на время не засчитывает, владелец - да; обычное чужой засчитывает.
	if _, err := st.MarkTask(ctx, timedAssign.ID, "opponent", ""); !errors.Is(err, store.ErrConflict) {
		t.Errorf("соперник отметил задание на время: %v", err)
	}
	if _, err := st.MarkTask(ctx, timedAssign.ID, "owner", ""); err != nil {
		t.Errorf("владелец не смог отметить задание на время: %v", err)
	}
	if _, err := st.MarkTask(ctx, normalAssign.ID, "opponent", ""); err != nil {
		t.Errorf("соперник не смог отметить обычное задание: %v", err)
	}
	_ = lastRound
}
