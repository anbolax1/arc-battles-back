package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Команды 2×2 из списка организатора: пара - одна команда, матч собирается из команд, состав сыгравшей
// не меняется, новое название доходит до запланированного матча и рейтинга. Запуск - как у TestMMRIntegration.
func TestTeamsIntegration(t *testing.T) {
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
		login := fmt.Sprintf("tm_%d_%s", uniq, tag)
		u, err := st.CreateUser(ctx, login, login, "x", models.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
		return u
	}
	a, b, c, d, e := mkUser("a"), mkUser("b"), mkUser("c"), mkUser("d"), mkUser("e")

	alpha, err := st.CreateTeam(ctx, "Альфа", b.ID, a.ID)
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if alpha.TeamKey != store.TeamKey(a.ID, b.ID) || len(alpha.Members) != 2 || !alpha.IsNew || alpha.Matches != 0 {
		t.Errorf("новая команда %+v", alpha)
	}
	if _, err := st.CreateTeam(ctx, "Дубль", a.ID, b.ID); !errors.Is(err, store.ErrTeamExists) {
		t.Errorf("та же пара: %v, want ErrTeamExists", err)
	}
	if _, err := st.CreateTeam(ctx, "Соло", a.ID, a.ID); !errors.Is(err, store.ErrConflict) {
		t.Errorf("один игрок дважды: %v, want ErrConflict", err)
	}
	beta, err := st.CreateTeam(ctx, "Бета", c.ID, d.ID)
	if err != nil {
		t.Fatalf("CreateTeam Бета: %v", err)
	}
	gamma, err := st.CreateTeam(ctx, "Гамма", a.ID, e.ID)
	if err != nil {
		t.Fatalf("CreateTeam Гамма: %v", err)
	}

	// Пока команда не играла, состав меняется; занятую пару взять нельзя.
	gamma, _, err = st.UpdateTeam(ctx, gamma.TeamKey, "Гамма", []string{e.ID, d.ID})
	if err != nil || gamma.TeamKey != store.TeamKey(d.ID, e.ID) {
		t.Fatalf("смена состава: %+v, %v", gamma, err)
	}
	if _, _, err := st.UpdateTeam(ctx, gamma.TeamKey, "Гамма", []string{c.ID, d.ID}); !errors.Is(err, store.ErrTeamExists) {
		t.Errorf("состав чужой команды: %v, want ErrTeamExists", err)
	}

	// Общий игрок у сторон - матча нет.
	at := time.Now().Add(48 * time.Hour)
	show := func(x, y models.MatchTeam) (models.Tournament, error) {
		return st.CreateMatch(ctx, store.NewMatch{Mode: "2x2", Format: store.FormatShow, StartsAt: &at,
			Sides: [2]store.MatchSide{{TeamKey: x.TeamKey}, {TeamKey: y.TeamKey}}})
	}
	if _, err := show(beta, gamma); !errors.Is(err, store.ErrConflict) {
		t.Errorf("у команд общий игрок: %v, want ErrConflict", err)
	}
	if _, err := st.CreateMatch(ctx, store.NewMatch{Mode: "2x2", Format: store.FormatShow, StartsAt: &at,
		Sides: [2]store.MatchSide{{TeamKey: alpha.TeamKey}, {TeamKey: "нет|такой"}}}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("команды нет: %v, want ErrNotFound", err)
	}

	tour, err := show(alpha, beta)
	if err != nil {
		t.Fatalf("CreateMatch: %v", err)
	}
	defer func() { _ = st.DeleteTournament(ctx, tour.ID) }()
	if tour.Mode != "2x2" || len(tour.Participants) != 2 {
		t.Fatalf("матч %+v", tour)
	}
	for _, p := range tour.Participants {
		var ms []map[string]string
		_ = json.Unmarshal(p.Members, &ms)
		if p.Kind != "team" || len(ms) != 2 || ms[0]["name"] == ms[0]["userId"] {
			t.Errorf("сторона %s: %s, состав %s", p.Name, p.Kind, p.Members)
		}
	}

	// Команда в матче: состав и удаление закрыты, название переезжает в запланированный матч.
	if _, _, err := st.UpdateTeam(ctx, alpha.TeamKey, "Альфа", []string{a.ID, e.ID}); !errors.Is(err, store.ErrTeamPlayed) {
		t.Errorf("состав команды из матча: %v, want ErrTeamPlayed", err)
	}
	if err := st.DeleteTeam(ctx, alpha.TeamKey); !errors.Is(err, store.ErrTeamPlayed) {
		t.Errorf("удаление команды из матча: %v, want ErrTeamPlayed", err)
	}
	alpha, renamed, err := st.UpdateTeam(ctx, alpha.TeamKey, "Альфа-2", nil)
	if err != nil || alpha.Name != "Альфа-2" || alpha.Matches != 1 || len(renamed) != 1 || renamed[0] != tour.ID {
		t.Fatalf("переименование: %+v, матчи %v, %v", alpha, renamed, err)
	}
	got, err := st.GetTournament(ctx, tour.ID)
	if err != nil {
		t.Fatalf("GetTournament: %v", err)
	}
	var winner string
	for _, p := range got.Participants {
		if key, _ := store.ParticipantTeamKey(p); key == alpha.TeamKey {
			winner = p.ID
			if p.Name != "Альфа-2" {
				t.Errorf("сторона в матче называется %q", p.Name)
			}
		}
	}
	if got.Title != "Альфа-2 vs Бета" && got.Title != "Бета vs Альфа-2" {
		t.Errorf("название матча %q", got.Title)
	}

	// Итог матча: рейтинг команды под названием из списка.
	if _, err := pool.Exec(ctx, `UPDATE tournaments SET status = 'finished', winner_participant_id = $2 WHERE id = $1`, tour.ID, winner); err != nil {
		t.Fatalf("итог: %v", err)
	}
	if err := st.ApplyTournamentMmr(ctx, tour.ID); err != nil {
		t.Fatalf("ApplyTournamentMmr: %v", err)
	}
	if _, _, err := st.UpdateTeam(ctx, alpha.TeamKey, "Альфа-3", nil); err != nil {
		t.Fatalf("переименование после матча: %v", err)
	}
	if err := st.ApplyTournamentMmr(ctx, tour.ID); err != nil {
		t.Fatalf("повторный ApplyTournamentMmr: %v", err)
	}
	tp, ok, err := st.TeamProfile(ctx, alpha.TeamKey)
	if err != nil || !ok || tp.Name != "Альфа-3" {
		t.Errorf("страница команды: %q, ok=%v, %v", tp.Name, ok, err)
	}
	if got, _ := st.GetTournament(ctx, tour.ID); got.Title == "Альфа-3 vs Бета" || got.Title == "Бета vs Альфа-3" {
		t.Errorf("сыгранный матч переименован: %q", got.Title)
	}
	alpha, err = st.GetMatchTeam(ctx, alpha.TeamKey)
	if err != nil {
		t.Fatalf("GetMatchTeam: %v", err)
	}
	beta, err = st.GetMatchTeam(ctx, beta.TeamKey)
	if err != nil {
		t.Fatalf("GetMatchTeam: %v", err)
	}
	if alpha.IsNew || alpha.Wins != 1 || beta.Losses != 1 || alpha.Mmr <= beta.Mmr {
		t.Errorf("после матча: победитель %+v, проигравший %+v", alpha, beta)
	}

	// Страница команды есть и до первого матча; несыгравшую можно убрать.
	tp, ok, err = st.TeamProfile(ctx, gamma.TeamKey)
	if err != nil || !ok || tp.Name != "Гамма" || len(tp.Members) != 2 || len(tp.Timeline) != 0 {
		t.Errorf("страница новой команды: %+v, ok=%v, %v", tp, ok, err)
	}
	if err := st.DeleteTeam(ctx, gamma.TeamKey); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}
	if _, err := st.GetMatchTeam(ctx, gamma.TeamKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("удалённая команда: %v, want ErrNotFound", err)
	}
}
