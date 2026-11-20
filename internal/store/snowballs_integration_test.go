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

// Снежки: пауза между бросками, свой профиль, счёт с нуля после сыгранного матча.
// Запуск - как у TestMMRIntegration.
func TestSnowballsIntegration(t *testing.T) {
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
	mkUser := func(tag string) models.User {
		login := fmt.Sprintf("sb_%d_%s", uniq, tag)
		u, err := st.CreateUser(ctx, login, login, "x", models.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
		return u
	}
	a, b, c := mkUser("a"), mkUser("b"), mkUser("c")

	if err := st.ThrowSnowball(ctx, a.ID, a.ID); !errors.Is(err, store.ErrSnowballSelf) {
		t.Fatalf("в себя: ждали ErrSnowballSelf, получили %v", err)
	}
	if err := st.ThrowSnowball(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("первый бросок: %v", err)
	}
	// Сразу второй - в другого игрока, но пауза общая на бросающего.
	var wait store.SnowballWaitError
	if err := st.ThrowSnowball(ctx, a.ID, c.ID); !errors.As(err, &wait) || wait.Wait <= 0 || wait.Wait > store.SnowballCooldown {
		t.Fatalf("второй бросок подряд: ждали паузу, получили %v", err)
	}
	// Другой игрок бросает независимо.
	if err := st.ThrowSnowball(ctx, c.ID, b.ID); err != nil {
		t.Fatalf("бросок другого игрока: %v", err)
	}

	sum, err := st.SnowballSummary(ctx, b.ID, a.ID)
	if err != nil {
		t.Fatalf("SnowballSummary: %v", err)
	}
	if sum.Count != 2 || len(sum.Recent) != 2 || sum.Recent[0].Login != c.Login {
		t.Fatalf("сводка: %+v", sum)
	}
	if sum.Me == nil || sum.Me.Self || sum.Me.WaitSec < 1 || sum.Me.WaitSec > 15 {
		t.Fatalf("Me бросавшего: %+v", sum.Me)
	}
	guest, err := st.SnowballSummary(ctx, b.ID, "")
	if err != nil || guest.Me != nil || guest.Count != 2 {
		t.Fatalf("гость: %+v, %v", guest, err)
	}
	own, err := st.SnowballSummary(ctx, b.ID, b.ID)
	if err != nil || own.Me == nil || !own.Me.Self || own.Me.WaitSec != 0 {
		t.Fatalf("свой профиль: %+v, %v", own, err)
	}

	// Игрок сыграл матч после броска - счёт с нуля.
	tour, err := st.CreateTournament(ctx, models.Tournament{Title: "sb-1x1", Mode: "1x1", RatingMultiplier: 1})
	if err != nil {
		t.Fatalf("CreateTournament: %v", err)
	}
	if _, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "player", UserID: &b.ID, Name: b.Login, Seed: 1}); err != nil {
		t.Fatalf("AddParticipant: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tournaments SET status = 'finished', starts_at = now() + interval '1 second' WHERE id = $1`, tour.ID); err != nil {
		t.Fatalf("завершение матча: %v", err)
	}
	after, err := st.SnowballSummary(ctx, b.ID, "")
	if err != nil || after.Count != 0 || len(after.Recent) != 0 {
		t.Fatalf("после матча счёт должен обнулиться: %+v, %v", after, err)
	}
}
