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

// Настройки раздачи: задание на время выключается, очки и число обычных заданий задаются; замена обычного
// задания берёт ту же редкость. Запуск - как у TestMMRIntegration.
func TestTaskDealSettingsIntegration(t *testing.T) {
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

	prev, err := st.TaskDealSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("ts-%d", time.Now().UnixNano())
	var tourIDs []string
	defer func() {
		_ = st.SaveTaskDealSettings(ctx, prev)
		for _, id := range tourIDs {
			_ = st.DeleteTournament(ctx, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM catalog_tasks WHERE name LIKE $1`, prefix+"%")
	}()

	for _, bad := range []store.TaskDealSettings{
		{TimedEnabled: true, MaxPoints: 0, MaxTasks: 5}, {TimedEnabled: true, MaxPoints: 31, MaxTasks: 5},
		{TimedEnabled: true, MaxPoints: 5, MaxTasks: 0}, {TimedEnabled: true, MaxPoints: 5, MaxTasks: 31},
	} {
		if err := st.SaveTaskDealSettings(ctx, bad); !errors.Is(err, store.ErrBadTaskSettings) {
			t.Errorf("%+v: ждали отказ, получили %v", bad, err)
		}
	}

	// Свои задания всех редкостей, чтобы набор не зависел от каталога на этой машине.
	for _, rarity := range models.TaskRarities {
		for i := 0; i < 12; i++ {
			if _, err := createTask(ctx, st, models.CatalogTask{
				Name: fmt.Sprintf("%s-%s-%d", prefix, rarity, i), Text: "Проверка настроек", Points: 1, ValueType: models.ValueFixed,
				Kind: "pvpve", Source: "official", Active: true, Rarity: rarity,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	deal := func() ([]models.RoundBonusTask, string) {
		t.Helper()
		tour, err := st.CreateTournament(ctx, models.Tournament{Title: prefix, Mode: "1x1", PlayerType: "pvpve"})
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
		if err := st.DealRoundTasks(ctx, rd.ID); err != nil {
			t.Fatal(err)
		}
		dealt, err := st.ListTournamentBonusTasks(ctx, tour.ID)
		if err != nil {
			t.Fatal(err)
		}
		return dealt, tour.ID
	}
	// sums - у каждой стороны: очки и число обычных заданий и число заданий на время.
	type side struct{ points, count, timed int }
	sums := func(dealt []models.RoundBonusTask) map[string]*side {
		out := map[string]*side{}
		for _, d := range dealt {
			s := out[d.ParticipantID]
			if s == nil {
				s = &side{}
				out[d.ParticipantID] = s
			}
			if d.Tag == models.TaskTagTimed {
				s.timed++
				continue
			}
			s.points += d.Points
			s.count++
		}
		return out
	}

	// Свои лимиты: 7 очков не больше чем двумя заданиями (из всех редкостей), задание на время есть.
	if err := st.SaveTaskDealSettings(ctx, store.TaskDealSettings{TimedEnabled: true, MaxPoints: 7, MaxTasks: 2}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.TaskDealSettings(ctx); err != nil || got != (store.TaskDealSettings{TimedEnabled: true, MaxPoints: 7, MaxTasks: 2}) {
		t.Fatalf("прочитали %+v %v", got, err)
	}
	dealt, tourID := deal()
	for pid, s := range sums(playOutTaskQueue(ctx, t, pool, st, tourID)) {
		// Обычные по умолчанию - только обычные редкости: на 7 очков из двух заданий их не собрать,
		// поэтому набор добирается из любых редкостей.
		if s.points != 7 || s.count != 2 || s.timed != 1 {
			t.Errorf("сторона %s: %+v, ждали 7 очков двумя заданиями и одно на время", pid, *s)
		}
	}

	// Задание на время выключено: обычные остаются, на время нет.
	if err := st.SaveTaskDealSettings(ctx, store.TaskDealSettings{TimedEnabled: false, MaxPoints: 5, MaxTasks: 5}); err != nil {
		t.Fatal(err)
	}
	dealt, tourID = deal()
	for pid, s := range sums(playOutTaskQueue(ctx, t, pool, st, tourID)) {
		if s.timed != 0 || s.points != 5 {
			t.Errorf("сторона %s без заданий на время: %+v", pid, *s)
		}
	}

	// Замена обычного задания берёт ту же редкость, очки сохраняются.
	dealt, tourID = deal()
	var target models.RoundBonusTask
	for _, d := range dealt {
		if d.Tag != models.TaskTagTimed {
			target = d
			break
		}
	}
	if err := st.RerollTask(ctx, target.ID); err != nil {
		t.Fatalf("замена: %v", err)
	}
	after, _ := st.ListTournamentBonusTasks(ctx, tourID)
	for _, d := range after {
		if d.ID == target.ID && (d.Rarity != target.Rarity || d.Points != target.Points || d.TaskID == target.TaskID) {
			t.Errorf("после замены: %s/%d (было %s/%d), задание то же: %v", d.Rarity, d.Points, target.Rarity, target.Points, d.TaskID == target.TaskID)
		}
	}
}
