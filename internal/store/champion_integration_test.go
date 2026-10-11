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
	"github.com/jackc/pgx/v5"
)

// Тег чемпиона по дате окончания сезона против реальной Postgres. Запуск:
//
//	RESPECT_TEST_DB=postgres://respect:respect@localhost:5433/respect_it go test ./internal/store -run TestChampionTagByEndDate -v
//
// Без переменной - пропускается (в т.ч. в CI).
func TestChampionTagByEndDate(t *testing.T) {
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
	sn, err := st.StartNewSeason(ctx, fmt.Sprintf("it-%d", uniq), 32, 1000)
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

	// A побеждает B - первое место таблицы сезона у A.
	a, b := mkUser("a"), mkUser("b")
	tour, err := st.CreateTournament(ctx, models.Tournament{Title: "it-1x1", Mode: "1x1", RatingMultiplier: 1})
	if err != nil {
		t.Fatalf("CreateTournament: %v", err)
	}
	pa, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "player", UserID: &a.ID, Name: a.Login, Seed: 1})
	if err != nil {
		t.Fatalf("AddParticipant a: %v", err)
	}
	if _, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "player", UserID: &b.ID, Name: b.Login, Seed: 2}); err != nil {
		t.Fatalf("AddParticipant b: %v", err)
	}
	if _, err := st.SetTournamentWinner(ctx, tour.ID, pa.ID); err != nil {
		t.Fatalf("SetTournamentWinner: %v", err)
	}
	if err := st.ApplyTournamentMmr(ctx, tour.ID); err != nil {
		t.Fatalf("ApplyTournamentMmr: %v", err)
	}

	// holders - кому выдан тег чемпиона сезона; nil - тега нет.
	holders := func() []string {
		var tagID string
		err := pool.QueryRow(ctx, `SELECT id FROM tags WHERE season_id = $1`, sn.ID).Scan(&tagID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			t.Fatalf("тег сезона: %v", err)
		}
		rows, err := pool.Query(ctx, `SELECT user_id::text FROM user_tags WHERE tag_id = $1`, tagID)
		if err != nil {
			t.Fatalf("держатели тега: %v", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("держатели тега: %v", err)
		}
		return ids
	}
	sync := func() {
		if err := st.SyncSeasonWinnerTags(ctx); err != nil {
			t.Fatalf("SyncSeasonWinnerTags: %v", err)
		}
	}
	setEnd := func(end *time.Time) {
		if _, err := st.UpdateSeason(ctx, sn.ID, sn.Name, sn.StartedAt, end, sn.KFactor, sn.StartMmr); err != nil {
			t.Fatalf("UpdateSeason: %v", err)
		}
		sync()
	}
	want := func(step string, ids ...string) {
		got := holders()
		if ids == nil && got != nil {
			t.Errorf("%s: тег есть у %v, а быть его не должно", step, got)
		}
		if ids != nil && !slices.Equal(got, ids) {
			t.Errorf("%s: тег у %v, want %v", step, got, ids)
		}
	}

	past := time.Now().Add(-time.Second).Truncate(time.Microsecond)
	future := time.Now().Add(24 * time.Hour)

	sync()
	want("сезон идёт, даты окончания нет")
	setEnd(&past)
	want("дата окончания наступила", a.ID)
	setEnd(nil)
	want("дату окончания убрали")
	setEnd(&future)
	want("дата окончания впереди")
	setEnd(&past)
	want("дату окончания вернули", a.ID)

	// Новый сезон закрывает этот: наступившая дата окончания не сдвигается, чемпион остаётся.
	if _, err := st.StartNewSeason(ctx, fmt.Sprintf("it-%d-next", uniq), 32, 1000); err != nil {
		t.Fatalf("StartNewSeason: %v", err)
	}
	if err := st.RefreshMmrCaches(ctx); err != nil {
		t.Fatalf("RefreshMmrCaches: %v", err)
	}
	closed, err := st.GetSeason(ctx, sn.ID)
	if err != nil {
		t.Fatalf("GetSeason: %v", err)
	}
	if closed.Status != "finished" || closed.EndedAt == nil || !closed.EndedAt.Equal(past) {
		t.Errorf("закрытый сезон: статус %s, окончание %v, want finished и %v", closed.Status, closed.EndedAt, past)
	}
	want("сезон закрыт новым", a.ID)
}
