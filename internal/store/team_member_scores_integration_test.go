package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Матч 2×2: ноки и ручные очки ставятся за игрока команды и сходятся с суммой стороны, задание команды
// засчитывается один раз одному игроку, отмена из журнала возвращает строку игрока. Запуск - как у TestMMRIntegration.
func TestTeamMemberScores(t *testing.T) {
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
		login := fmt.Sprintf("tms_%d_%s", uniq, tag)
		u, err := st.CreateUser(ctx, login, login, "x", models.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
		return u
	}
	a, b, c, d := mkUser("a"), mkUser("b"), mkUser("c"), mkUser("d")
	ab, err := st.CreateTeam(ctx, "Пара AB", a.ID, b.ID)
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	cd, err := st.CreateTeam(ctx, "Пара CD", c.ID, d.ID)
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	at := time.Now().Add(72 * time.Hour)
	tour, err := st.CreateMatch(ctx, store.NewMatch{Mode: "2x2", Format: store.FormatShow, StartsAt: &at,
		Sides: [2]store.MatchSide{{TeamKey: ab.TeamKey}, {TeamKey: cd.TeamKey}}})
	if err != nil {
		t.Fatalf("CreateMatch: %v", err)
	}
	defer func() { _ = st.DeleteTournament(ctx, tour.ID) }()
	sort.Slice(tour.Rounds, func(i, j int) bool { return tour.Rounds[i].Number < tour.Rounds[j].Number })
	round := tour.Rounds[0].ID
	var side string
	for _, p := range tour.Participants {
		if key, _ := store.ParticipantTeamKey(p); key == ab.TeamKey {
			side = p.ID
		}
	}

	type counts struct{ points, knocks int }
	member := func(ms models.MatchState, user string) counts {
		for _, m := range ms.Members {
			if m.RoundNumber == 1 && m.ParticipantID == side && m.UserID == user {
				return counts{m.Points, m.Knocks}
			}
		}
		return counts{}
	}
	state := func() (models.MatchState, counts) {
		t.Helper()
		ms, err := st.GetMatchState(ctx, tour.ID)
		if err != nil {
			t.Fatalf("GetMatchState: %v", err)
		}
		var sideCounts counts
		for _, m := range ms.Manual {
			if m.RoundNumber == 1 && m.ParticipantID == side {
				sideCounts.points = m.Points
			}
		}
		for _, k := range ms.Knocks {
			if k.RoundNumber == 1 && k.ParticipantID == side {
				sideCounts.knocks = k.Knocks
			}
		}
		return ms, sideCounts
	}
	step := func(what string, got, want int, err error) {
		t.Helper()
		if err != nil || got != want {
			t.Fatalf("%s = %d, %v; want %d", what, got, err, want)
		}
	}

	got, err := st.AdjustMemberKnocks(ctx, round, side, a.ID, 1)
	step("нок A", got, 1, err)
	got, err = st.AdjustMemberKnocks(ctx, round, side, b.ID, 1)
	step("нок B", got, 1, err)
	got, err = st.AdjustMemberKnocks(ctx, round, side, b.ID, -1)
	step("снять нок B", got, -1, err)
	got, err = st.AdjustMemberKnocks(ctx, round, side, b.ID, -1)
	step("снять нок B, когда его нет", got, 0, err)
	got, err = st.AdjustMemberPoints(ctx, round, side, a.ID, -5)
	step("ручные A не съедают очки за нок", got, 0, err)
	got, err = st.AdjustMemberPoints(ctx, round, side, a.ID, 2)
	step("ручные A", got, 2, err)

	ms, sideCounts := state()
	if m := member(ms, a.ID); m != (counts{5, 1}) {
		t.Errorf("игрок A: %+v, want очки 5 и нок 1", m)
	}
	if m := member(ms, b.ID); m != (counts{0, 0}) {
		t.Errorf("игрок B: %+v, want пусто", m)
	}
	if sideCounts != (counts{5, 1}) {
		t.Errorf("сторона: %+v, want сумма игроков 5 и 1", sideCounts)
	}

	// Отмена нока из журнала возвращает его и у игрока, и у стороны.
	if err := st.AddMatchLog(ctx, tour.ID, 1, &side, "knock", "Пара AB · A: нок", store.KnockPoints, map[string]any{
		"type": "knock", "roundId": round, "participantId": side, "userId": a.ID, "applied": 1,
	}); err != nil {
		t.Fatalf("AddMatchLog: %v", err)
	}
	if _, err := st.UndoLastMatchLog(ctx, tour.ID); err != nil {
		t.Fatalf("UndoLastMatchLog: %v", err)
	}
	ms, sideCounts = state()
	if m := member(ms, a.ID); m != (counts{2, 0}) || sideCounts != (counts{2, 0}) {
		t.Errorf("после отмены нока: игрок %+v, сторона %+v; want 2 очка без ноков", m, sideCounts)
	}

	// Задание команды засчитывается одному игроку: второму - только после отмены.
	var task string
	if err := pool.QueryRow(ctx, `
		INSERT INTO round_bonus_tasks (round_id, participant_id, task_id)
		SELECT $1, $2, id FROM catalog_tasks ORDER BY id LIMIT 1 RETURNING id`, round, side).Scan(&task); err != nil {
		t.Logf("каталог заданий пуст - зачёт задания не проверен: %v", err)
		return
	}
	if _, err := st.MarkTask(ctx, task, "owner", a.ID); err != nil {
		t.Fatalf("зачёт A: %v", err)
	}
	if _, err := st.MarkTask(ctx, task, "owner", b.ID); !errors.Is(err, store.ErrTaskTaken) {
		t.Errorf("зачёт B поверх A: %v, want ErrTaskTaken", err)
	}
	if _, err := st.MarkTask(ctx, task, "none", ""); err != nil {
		t.Fatalf("снять зачёт: %v", err)
	}
	if _, err := st.MarkTask(ctx, task, "owner", b.ID); err != nil {
		t.Fatalf("зачёт B после отмены: %v", err)
	}
	ms, _ = state()
	for _, tk := range ms.Tasks {
		if tk.ID == task && (tk.CompletedBy == nil || *tk.CompletedBy != side || tk.CompletedUser == nil || *tk.CompletedUser != b.ID) {
			t.Errorf("задание засчитано %v игроку %v, want стороне и игроку B", tk.CompletedBy, tk.CompletedUser)
		}
	}
}
