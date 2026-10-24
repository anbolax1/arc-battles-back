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

// Незасчитанный матч против живой Postgres. Запуск - как у TestMMRIntegration.
func TestVoidedMatchIntegration(t *testing.T) {
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
		login := fmt.Sprintf("vd_%d_%s", uniq, tag)
		u, err := st.CreateUser(ctx, login, login, "x", models.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
		return u
	}
	// play - матч 1×1 из одного раунда: побеждает a по нокам.
	play := func(a, b models.User) string {
		tour, err := st.CreateTournament(ctx, models.Tournament{Title: "vd-1x1", Mode: "1x1", RatingMultiplier: 1})
		if err != nil {
			t.Fatalf("CreateTournament: %v", err)
		}
		rd, err := st.CreateRound(ctx, models.Round{TournamentID: tour.ID, Number: 1, Status: "live"})
		if err != nil {
			t.Fatalf("CreateRound: %v", err)
		}
		pa, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "player", UserID: &a.ID, Name: a.Login, Seed: 1})
		if err != nil {
			t.Fatalf("AddParticipant a: %v", err)
		}
		if _, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "player", UserID: &b.ID, Name: b.Login, Seed: 2}); err != nil {
			t.Fatalf("AddParticipant b: %v", err)
		}
		if _, err := st.AdjustKnocks(ctx, rd.ID, pa.ID, 1); err != nil {
			t.Fatalf("AdjustKnocks: %v", err)
		}
		if _, err := st.FinishMatch(ctx, tour.ID); err != nil {
			t.Fatalf("FinishMatch: %v", err)
		}
		return tour.ID
	}
	rows := func(match string, u models.User) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM mmr_history WHERE tournament_id = $1 AND user_id = $2`, match, u.ID).Scan(&n); err != nil {
			t.Fatalf("mmr_history: %v", err)
		}
		return n
	}
	before := func(match string, u models.User) int {
		t.Helper()
		var v int
		if err := pool.QueryRow(ctx, `SELECT mmr_before FROM mmr_history WHERE tournament_id = $1 AND user_id = $2`, match, u.ID).Scan(&v); err != nil {
			t.Fatalf("mmr_history: %v", err)
		}
		return v
	}

	a, b, c := mkUser("a"), mkUser("b"), mkUser("c")
	m1 := play(a, b)
	m2 := play(b, c)
	var season string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(season_id, '') FROM tournaments WHERE id = $1`, m1).Scan(&season); err != nil {
		t.Fatalf("сезон: %v", err)
	}
	start := st.ActiveSeasonStart(ctx)

	if _, err := pool.Exec(ctx, `UPDATE tournaments SET voided = true, void_reason = 'читеры' WHERE id = $1`, m1); err != nil {
		t.Fatalf("отметка: %v", err)
	}
	if err := st.RecomputeAllMmr(ctx); err != nil {
		t.Fatalf("RecomputeAllMmr: %v", err)
	}

	// Победителю MMR остаётся, проигравшему возвращается - и следующий матч он начинает со старта.
	if rows(m1, a) != 1 || rows(m1, b) != 1 {
		t.Fatalf("матч в истории MMR: a %d, b %d; want у обоих", rows(m1, a), rows(m1, b))
	}
	if got := before(m2, b); got != start {
		t.Errorf("MMR b перед вторым матчем = %d, want %d - поражение не списало MMR", got, start)
	}
	if mmr, err := st.GetUserMmr(ctx, a.ID, "1x1"); err != nil || mmr <= start {
		t.Errorf("MMR a = %d (%v), want больше %d", mmr, err, start)
	}

	// Матч сыгран: он в числе матчей, но ни победа, ни поражение.
	board, err := st.Leaderboard(ctx, "1x1", season)
	if err != nil {
		t.Fatalf("Leaderboard: %v", err)
	}
	got := map[string]models.LeaderboardRow{}
	for _, r := range board {
		got[r.UserID] = r
	}
	if r := got[a.ID]; r.Wins != 0 || r.Losses != 0 || r.Tournaments != 1 {
		t.Errorf("a в таблице: %d-%d, матчей %d; want 0-0 и 1", r.Wins, r.Losses, r.Tournaments)
	}
	if r := got[b.ID]; r.Wins != 1 || r.Losses != 0 || r.Tournaments != 2 {
		t.Errorf("b в таблице: %d-%d, матчей %d; want 1-0 и 2", r.Wins, r.Losses, r.Tournaments)
	}

	stats, timeline, _, err := st.PlayerStatsBundle(ctx, a.ID)
	if err != nil {
		t.Fatalf("PlayerStatsBundle: %v", err)
	}
	if len(timeline) != 1 || !timeline[0].Voided {
		t.Errorf("лента a: %+v, want одна точка незасчитанного матча", timeline)
	}
	if stats.Games != 1 || stats.Wins != 0 || stats.Losses != 0 || stats.CurrentStreakLen != 0 {
		t.Errorf("сводка a: матчей %d, %d-%d, серия %d; want матч без побед, поражений и серии",
			stats.Games, stats.Wins, stats.Losses, stats.CurrentStreakLen)
	}

	history, err := st.PlayerHistory(ctx, b.ID)
	if err != nil {
		t.Fatalf("PlayerHistory: %v", err)
	}
	for _, h := range history {
		if h.TournamentID == m1 && (!h.Voided || h.MmrDelta == nil || *h.MmrDelta != 0) {
			t.Errorf("матч в истории b: незасчитан %v, MMR %v; want незасчитан и MMR не изменился", h.Voided, h.MmrDelta)
		}
	}
}
