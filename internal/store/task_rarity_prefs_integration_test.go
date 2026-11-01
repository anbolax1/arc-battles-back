package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

func TestNormalizeTaskRarities(t *testing.T) {
	got, err := store.NormalizeTaskRarities([]string{"epic", "common", "epic", "rare"})
	if err != nil || fmt.Sprint(got) != "[common rare epic]" {
		t.Fatalf("порядок и повторы: %v %v", got, err)
	}
	for _, bad := range [][]string{nil, {}, {"mythic"}, {"common", "mythic"}} {
		if _, err := store.NormalizeTaskRarities(bad); !errors.Is(err, store.ErrBadRarities) {
			t.Errorf("%v: ждали отказ, получили %v", bad, err)
		}
	}
}

// Раздача по предпочтениям игрока: из профиля, ручная настройка стороны перекрывает профиль, а если задач
// нужной редкости нет - берутся ближайшие. Запуск - как у TestMMRIntegration.
func TestTaskRarityPrefsIntegration(t *testing.T) {
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
	prefix := fmt.Sprintf("rp-%d", uniq)
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

	// В пуле по 8 обычных и 4 на время каждой из двух редкостей: хватает на обе стороны матча.
	for _, rarity := range []string{models.RarityLegendary, models.RarityRare} {
		for i := 0; i < 12; i++ {
			tag := models.TaskTagNormal
			if i >= 8 {
				tag = models.TaskTagTimed
			}
			if _, err := createTask(ctx, st, models.CatalogTask{
				Name: fmt.Sprintf("%s-%s-%d", prefix, rarity, i), Text: "Проверка предпочтений", Points: 1, ValueType: models.ValueFixed,
				Kind: "pvpve", Source: "official", Tag: tag, Active: true, Rarity: rarity,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	player := func(name string, prefs []string) models.User {
		u, _, err := st.CreatePlaceholderUser(ctx, fmt.Sprintf("%s-%s", prefix, name))
		if err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, u.ID)
		if prefs != nil {
			if _, err := st.SaveUserTaskRarities(ctx, u.ID, prefs); err != nil {
				t.Fatal(err)
			}
		}
		return u
	}
	deal := func(a, b store.MatchSide) map[string][]string {
		t.Helper()
		starts := time.Now().Add(24 * time.Hour)
		tour, err := st.CreateMatch(ctx, store.NewMatch{
			Mode: "1x1", PlayerType: "pvpve", Format: store.FormatShow, StartsAt: &starts, Sides: [2]store.MatchSide{a, b},
		})
		if err != nil {
			t.Fatalf("CreateMatch: %v", err)
		}
		tourIDs = append(tourIDs, tour.ID)
		rounds, err := st.ListRounds(ctx, tour.ID)
		if err != nil || len(rounds) == 0 {
			t.Fatalf("раунды: %v", err)
		}
		if err := st.DealRoundTasks(ctx, rounds[0].ID); err != nil {
			t.Fatal(err)
		}
		parts, _ := st.ListParticipants(ctx, tour.ID)
		userOf := map[string]string{}
		for _, p := range parts {
			if p.UserID != nil {
				userOf[p.ID] = *p.UserID
			}
		}
		dealt, err := st.ListTournamentBonusTasks(ctx, tour.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, d := range dealt {
			out[userOf[d.ParticipantID]] = append(out[userOf[d.ParticipantID]], d.Rarity)
		}
		return out
	}
	only := func(rs []string, want ...string) bool {
		return len(rs) == 3 && !slices.ContainsFunc(rs, func(r string) bool { return !slices.Contains(want, r) })
	}

	// 1. Предпочтения из профиля: у A только легендарные; задание на время - тоже легендарное.
	a := player("a", []string{models.RarityLegendary})
	b := player("b", nil)
	got := deal(store.MatchSide{UserID: a.ID}, store.MatchSide{UserID: b.ID})
	if !only(got[a.ID], models.RarityLegendary) {
		t.Errorf("профиль {legendary}: выдано %v", got[a.ID])
	}

	// 2. Ручная настройка стороны перекрывает профиль: профиль A - легендарные, на матч - редкие.
	c := player("c", []string{models.RarityLegendary})
	d := player("d", nil)
	got = deal(store.MatchSide{UserID: c.ID, TaskRarities: []string{models.RarityRare}}, store.MatchSide{UserID: d.ID})
	if !only(got[c.ID], models.RarityRare) {
		t.Errorf("ручная настройка {rare} поверх профиля {legendary}: выдано %v", got[c.ID])
	}

	// 3. Редкости, которой нет, добираются ближайшими: epic -> rare или legendary (обе на расстоянии 1).
	e := player("e", []string{models.RarityEpic})
	f := player("f", nil)
	got = deal(store.MatchSide{UserID: e.ID}, store.MatchSide{UserID: f.ID})
	if !only(got[e.ID], models.RarityEpic, models.RarityRare, models.RarityLegendary) {
		t.Errorf("профиль {epic} без эпических: выдано %v (ждали ближайшие rare/legendary)", got[e.ID])
	}

	// 4. По умолчанию у игрока только обычные: в профиле одна редкость, и выдаются три задания.
	if def, err := st.GetUserTaskRarities(ctx, f.ID); err != nil || fmt.Sprint(def) != "[common]" {
		t.Errorf("умолчание профиля: %v %v", def, err)
	}
	if len(got[f.ID]) != 3 {
		t.Errorf("по умолчанию выдано %v", got[f.ID])
	}

	// 5. Профиль хранится в каноническом виде.
	saved, err := st.SaveUserTaskRarities(ctx, b.ID, []string{"epic", "common"})
	if err != nil || fmt.Sprint(saved) != "[common epic]" {
		t.Errorf("сохранение профиля: %v %v", saved, err)
	}
	if _, err := st.SaveUserTaskRarities(ctx, b.ID, []string{}); !errors.Is(err, store.ErrBadRarities) {
		t.Errorf("пустой список принят: %v", err)
	}
}
