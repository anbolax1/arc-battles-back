package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Общая раскладка прежних версий переезжает в сцену, а /overlay дальше показывает копию выбранной сцены.
// Запуск - как у TestMMRIntegration; состояние оверлея базы тест возвращает как было.
func TestOverlayScenesIntegration(t *testing.T) {
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

	saved, err := st.GetLiveState(ctx)
	if err != nil {
		t.Fatalf("состояние оверлея: %v", err)
	}
	before, err := st.ListOverlayPresets(ctx)
	if err != nil {
		t.Fatalf("сцены: %v", err)
	}
	known := map[string]bool{}
	for _, p := range before {
		known[p.ID] = true
	}
	defer func() {
		_ = st.SetLiveState(ctx, saved)
		after, _ := st.ListOverlayPresets(ctx)
		for _, p := range after {
			if !known[p.ID] {
				_ = st.DeleteOverlayPreset(ctx, p.ID)
			}
		}
	}()

	// Своя координата, чтобы раскладка теста не совпала ни с одной сценой базы.
	x := float64(time.Now().UnixNano()%1000) / 10000
	layout := func(visible bool, at float64) models.OverlayLayout {
		return models.OverlayLayout{
			Version: 1,
			StageBg: models.OverlayBg{Opacity: 0.4},
			Widgets: []models.WidgetInstance{{ID: "bn", Type: "bonusTasks", X: at, Y: 0.6, Scale: 1, Z: 1, Visible: visible, Bg: models.OverlayBg{On: true, Opacity: 1}}},
		}
	}
	setLive := func(l models.OverlayLayout) {
		b, _ := json.Marshal(models.LiveState{TournamentName: "тест сцен", Tasks: []models.Task{}, Layout: &l})
		if err := st.SetLiveState(ctx, b); err != nil {
			t.Fatalf("запись состояния: %v", err)
		}
	}
	live := func() models.LiveState {
		var ls models.LiveState
		data, err := st.GetLiveState(ctx)
		if err != nil {
			t.Fatalf("чтение состояния: %v", err)
		}
		if err := json.Unmarshal(data, &ls); err != nil {
			t.Fatalf("состояние: %v", err)
		}
		return ls
	}
	count := func() int {
		ps, err := st.ListOverlayPresets(ctx)
		if err != nil {
			t.Fatalf("сцены: %v", err)
		}
		return len(ps)
	}

	// Видимая раскладка, которой нет ни в одной сцене, становится сценой и выбирается.
	setLive(layout(true, x))
	n := count()
	if err := st.EnsureOverlayScene(ctx); err != nil {
		t.Fatalf("EnsureOverlayScene: %v", err)
	}
	ls := live()
	if ls.Layout == nil || ls.Layout.ActivePresetID == "" {
		t.Fatalf("раскладка не стала сценой: %+v", ls.Layout)
	}
	if count() != n+1 {
		t.Fatalf("сцен %d, ждали %d", count(), n+1)
	}
	kept, err := st.GetOverlayPresetByKey(ctx, ls.Layout.ActivePresetID)
	if err != nil || kept.Name != "Общая раскладка" {
		t.Fatalf("сцена из раскладки: %+v, %v", kept, err)
	}
	if ls.TournamentName != "тест сцен" || ls.Layout.Widgets[0].X != x {
		t.Fatalf("состояние оверлея изменилось: %+v", ls)
	}

	// Повторный запуск ничего не создаёт: копия совпадает с выбранной сценой.
	if err := st.EnsureOverlayScene(ctx); err != nil {
		t.Fatalf("повторный EnsureOverlayScene: %v", err)
	}
	if count() != n+1 {
		t.Fatalf("повторный запуск создал сцену: %d", count())
	}

	// Пустая раскладка сценой не становится, копия берётся из выбранной сцены.
	raw, _ := json.Marshal(layout(true, x+0.05))
	scene, err := st.CreateOverlayPreset(ctx, fmt.Sprintf("сцена теста %d", time.Now().UnixNano()), "", raw)
	if err != nil {
		t.Fatalf("сцена: %v", err)
	}
	hidden := layout(false, x)
	hidden.ActivePresetID = scene.ID
	setLive(hidden)
	if err := st.EnsureOverlayScene(ctx); err != nil {
		t.Fatalf("EnsureOverlayScene с пустой раскладкой: %v", err)
	}
	ls = live()
	if count() != n+2 {
		t.Fatalf("пустая раскладка стала сценой: %d", count())
	}
	if ls.Layout == nil || ls.Layout.ActivePresetID != scene.ID || !ls.Layout.Widgets[0].Visible || ls.Layout.Widgets[0].X != x+0.05 {
		t.Fatalf("копия не взята из выбранной сцены: %+v", ls.Layout)
	}

	// Правка сцены доходит до копии.
	raw, _ = json.Marshal(layout(true, x+0.1))
	if _, err := st.UpdateOverlayPreset(ctx, scene.ID, scene.Name, "", raw); err != nil {
		t.Fatalf("правка сцены: %v", err)
	}
	if err := st.SelectOverlayScene(ctx, ""); err != nil {
		t.Fatalf("SelectOverlayScene: %v", err)
	}
	if got := live().Layout; got == nil || got.Widgets[0].X != x+0.1 {
		t.Fatalf("правка сцены не дошла до копии: %+v", got)
	}

	// Переименование без раскладки раскладку сцены не трогает.
	renamed, err := st.UpdateOverlayPreset(ctx, scene.ID, scene.Name+" новое имя", "", nil)
	if err != nil {
		t.Fatalf("переименование: %v", err)
	}
	var rl models.OverlayLayout
	if err := json.Unmarshal(renamed.Layout, &rl); err != nil || renamed.Name != scene.Name+" новое имя" || len(rl.Widgets) != 1 || rl.Widgets[0].X != x+0.1 {
		t.Fatalf("переименование задело раскладку: %+v (%v)", renamed, err)
	}

	// Выбор другой сцены по id.
	if err := st.SelectOverlayScene(ctx, kept.ID); err != nil {
		t.Fatalf("выбор сцены: %v", err)
	}
	if got := live().Layout; got == nil || got.ActivePresetID != kept.ID || got.Widgets[0].X != x {
		t.Fatalf("выбрана не та сцена: %+v", got)
	}

	// Удалённая выбранная сцена снимает выбор: /overlay рисует раскладку по умолчанию.
	if err := st.DeleteOverlayPreset(ctx, kept.ID); err != nil {
		t.Fatalf("удаление сцены: %v", err)
	}
	if err := st.SelectOverlayScene(ctx, ""); err != nil {
		t.Fatalf("SelectOverlayScene после удаления: %v", err)
	}
	if got := live().Layout; got != nil {
		t.Fatalf("после удаления сцены осталась раскладка: %+v", got)
	}
}
