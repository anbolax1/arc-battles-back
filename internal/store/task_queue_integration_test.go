package store_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Очередь заданий: у стороны открыто одно обычное и одно на время; следующее обычное открывается после
// выполнения текущего, от дешёвых к дорогим; лимит очков набора сохраняется. Запуск - как у TestMMRIntegration.
func TestTaskQueueIntegration(t *testing.T) {
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

	prefix := fmt.Sprintf("tq-%d", time.Now().UnixNano())
	var tourIDs, userIDs []string
	defer func() {
		for _, id := range tourIDs {
			_ = st.DeleteTournament(ctx, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM catalog_tasks WHERE name LIKE $1`, prefix+"%")
		for _, id := range userIDs {
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
		}
	}()

	// Свои редкие задания (8 обычных и 4 на время): набор «редкое + два обычных» из них собирается всегда.
	for i := 0; i < 12; i++ {
		tag := models.TaskTagNormal
		if i >= 8 {
			tag = models.TaskTagTimed
		}
		if _, err := createTask(ctx, st, models.CatalogTask{
			Name: fmt.Sprintf("%s-rare-%d", prefix, i), Text: "Проверка очереди", Points: 1, ValueType: models.ValueFixed,
			Kind: "pvpve", Source: "official", Tag: tag, Active: true, Rarity: models.RarityRare,
		}); err != nil {
			t.Fatal(err)
		}
	}
	player := func(name string) models.User {
		u, _, err := st.CreatePlaceholderUser(ctx, prefix+"-"+name)
		if err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, u.ID)
		return u
	}
	a, b := player("a"), player("b")
	starts := time.Now().Add(24 * time.Hour)
	tour, err := st.CreateMatch(ctx, store.NewMatch{
		Mode: "1x1", PlayerType: "pvpve", Format: store.FormatShow, StartsAt: &starts,
		Sides: [2]store.MatchSide{{UserID: a.ID, TaskRarities: []string{models.RarityRare}}, {UserID: b.ID}},
	})
	if err != nil {
		t.Fatalf("CreateMatch: %v", err)
	}
	tourIDs = append(tourIDs, tour.ID)
	rounds, err := st.ListRounds(ctx, tour.ID)
	if err != nil || len(rounds) == 0 {
		t.Fatalf("раунды: %v", err)
	}
	roundID := rounds[0].ID
	if _, err := pool.Exec(ctx, `UPDATE rounds SET status = 'live' WHERE id = $1`, roundID); err != nil {
		t.Fatal(err)
	}
	if err := st.DealRoundTasks(ctx, roundID); err != nil {
		t.Fatal(err)
	}
	parts, _ := st.ListParticipants(ctx, tour.ID)
	var sideA string
	for _, p := range parts {
		if p.UserID != nil && *p.UserID == a.ID {
			sideA = p.ID
		}
	}

	// view - что сейчас открыто у стороны A: задание на время и обычные (по порядку выдачи).
	view := func() (timed []models.RoundBonusTask, normal []models.RoundBonusTask) {
		t.Helper()
		list, err := st.ListTournamentBonusTasks(ctx, tour.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range list {
			switch {
			case d.ParticipantID != sideA:
			case d.Tag == models.TaskTagTimed:
				timed = append(timed, d)
			default:
				normal = append(normal, d)
			}
		}
		// Список приходит по тексту задания, а у тестовых он одинаковый: порядок выдачи - по месту в очереди.
		slices.SortFunc(normal, func(x, y models.RoundBonusTask) int { return *x.QueuePos - *y.QueuePos })
		return timed, normal
	}
	queued := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM round_task_queue WHERE round_id = $1 AND participant_id = $2`, roundID, sideA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 1. После раздачи: одно на время, одно обычное (самое дешёвое), два ждут в очереди.
	timed, normal := view()
	if len(timed) != 1 || len(normal) != 1 || queued() != 2 {
		t.Fatalf("после раздачи: на время %d, обычных %d, в очереди %d (ждали 1, 1, 2)", len(timed), len(normal), queued())
	}
	if normal[0].Rarity != models.RarityCommon {
		t.Fatalf("первым открыто %s, ждали обычное", normal[0].Rarity)
	}

	// 2. Задание на время следующее обычное не открывает.
	if _, err := st.MarkTask(ctx, timed[0].ID, "owner", ""); err != nil {
		t.Fatal(err)
	}
	if _, n := view(); len(n) != 1 || queued() != 2 {
		t.Fatalf("после задания на время открыто %d обычных, в очереди %d", len(n), queued())
	}

	// 3. Выполнили обычное - открылось следующее, очередь убавилась.
	if _, err := st.MarkTask(ctx, normal[0].ID, "owner", ""); err != nil {
		t.Fatal(err)
	}
	_, normal = view()
	if len(normal) != 2 || normal[1].CompletedBy != nil || queued() != 1 {
		t.Fatalf("после первого: обычных %d, в очереди %d (ждали 2 и 1)", len(normal), queued())
	}

	// 4. Сняли зачёт с первого - второе уходит обратно в очередь, открыто одно.
	if _, err := st.MarkTask(ctx, normal[0].ID, "none", ""); err != nil {
		t.Fatal(err)
	}
	if _, n := view(); len(n) != 1 || queued() != 2 {
		t.Fatalf("после снятия зачёта: обычных %d, в очереди %d (ждали 1 и 2)", len(n), queued())
	}

	// 5. Заново выполнили - то же второе задание открылось снова; убрали его - открылось третье.
	if _, err := st.MarkTask(ctx, normal[0].ID, "owner", ""); err != nil {
		t.Fatal(err)
	}
	_, normal = view()
	if _, err := st.RemoveBonusTask(ctx, normal[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, n := view(); len(n) != 2 || n[1].Rarity != models.RarityRare || queued() != 0 {
		t.Fatalf("после снятия открытого: обычных %d, в очереди %d (ждали 2 и 0, редкое последним)", len(n), queued())
	}

	// 6. Задания матча не повторяются, очки открытых не больше лимита.
	_, normal = view()
	sum, seen := 0, map[string]bool{}
	for _, d := range normal {
		sum += d.Points
		if seen[d.TaskID] {
			t.Errorf("задание %s выдано дважды", d.TaskID)
		}
		seen[d.TaskID] = true
	}
	if sum > models.RoundTaskBudget {
		t.Errorf("очки заданий %d больше лимита %d", sum, models.RoundTaskBudget)
	}

	// 7. В сыгранном раунде отметка задание не открывает.
	if _, err := pool.Exec(ctx, `UPDATE rounds SET status = 'finished' WHERE id = $1`, roundID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkTask(ctx, normal[0].ID, "none", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkTask(ctx, normal[0].ID, "owner", ""); err != nil {
		t.Fatal(err)
	}
	if _, n := view(); len(n) != 2 {
		t.Errorf("в сыгранном раунде открыто %d обычных, ждали 2", len(n))
	}
}
