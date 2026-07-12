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

// Интеграционная проверка MMR против реальной Postgres. Запуск:
//
//	RESPECT_TEST_DB=postgres://respect:respect@localhost:5433/respect go test ./internal/store -run TestMMRIntegration -v
//
// Без переменной — пропускается (в т.ч. в CI).
func TestMMRIntegration(t *testing.T) {
	url := os.Getenv("RESPECT_TEST_DB")
	if url == "" {
		t.Skip("RESPECT_TEST_DB не задан — интеграционный тест пропущен")
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
		login := fmt.Sprintf("it_%d_%s", uniq, tag)
		u, err := st.CreateUser(ctx, login, login, "x", models.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
		return u
	}
	members := func(ids ...string) json.RawMessage {
		var arr []map[string]string
		for _, id := range ids {
			arr = append(arr, map[string]string{"userId": id, "name": id})
		}
		b, _ := json.Marshal(arr)
		return b
	}

	// finish1x1 создаёт 1×1-турнир, ставит участников и завершает победой winnerIdx (0 или 1).
	finish1x1 := func(a, b models.User, mult, winnerIdx int) {
		tour, err := st.CreateTournament(ctx, models.Tournament{Title: "it-1x1", Mode: "1x1", RatingMultiplier: mult})
		if err != nil {
			t.Fatalf("CreateTournament: %v", err)
		}
		pa, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "player", UserID: &a.ID, Name: a.Login, Seed: 1})
		if err != nil {
			t.Fatalf("AddParticipant a: %v", err)
		}
		pb, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Kind: "player", UserID: &b.ID, Name: b.Login, Seed: 2})
		if err != nil {
			t.Fatalf("AddParticipant b: %v", err)
		}
		winner := pa.ID
		if winnerIdx == 1 {
			winner = pb.ID
		}
		if _, err := st.SetTournamentWinner(ctx, tour.ID, winner); err != nil {
			t.Fatalf("SetTournamentWinner: %v", err)
		}
		if err := st.ApplyTournamentMmr(ctx, tour.ID); err != nil {
			t.Fatalf("ApplyTournamentMmr: %v", err)
		}
	}

	assertUser := func(u models.User, want int) {
		got, err := st.GetUserMmr(ctx, u.ID, "1x1")
		if err != nil {
			t.Fatalf("GetUserMmr: %v", err)
		}
		if got != want {
			t.Errorf("1x1 MMR %s = %d, want %d", u.Login, got, want)
		}
	}
	assertTeam := func(u models.User, want int) {
		got, err := st.BestTeamMmr(ctx, u.ID)
		if err != nil {
			t.Fatalf("BestTeamMmr: %v", err)
		}
		if got != want {
			t.Errorf("2x2 team MMR (%s) = %d, want %d", u.Login, got, want)
		}
	}

	// 1×1 обычный: равные 1000, победа A → 1016 / 984.
	a, b := mkUser("a"), mkUser("b")
	finish1x1(a, b, 1, 0)
	assertUser(a, 1016)
	assertUser(b, 984)

	// 1×1 жетон ×2: победа C → 1031 / 969 (16, затем 15 от обновлённого рейтинга).
	c, d := mkUser("c"), mkUser("d")
	finish1x1(c, d, 2, 0)
	assertUser(c, 1031)
	assertUser(d, 969)

	// 2×2 командный: (E,F) бьют (G,H) → команда-победитель 1016, проигравшая 984.
	e, f, g, h := mkUser("e"), mkUser("f"), mkUser("g"), mkUser("h")
	tour2, err := st.CreateTournament(ctx, models.Tournament{Title: "it-2x2", Mode: "2x2", RatingMultiplier: 1})
	if err != nil {
		t.Fatalf("CreateTournament 2x2: %v", err)
	}
	t1, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour2.ID, Kind: "team", Name: "T1", Seed: 1, Members: members(e.ID, f.ID)})
	if err != nil {
		t.Fatalf("AddParticipant t1: %v", err)
	}
	if _, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour2.ID, Kind: "team", Name: "T2", Seed: 2, Members: members(g.ID, h.ID)}); err != nil {
		t.Fatalf("AddParticipant t2: %v", err)
	}
	if _, err := st.SetTournamentWinner(ctx, tour2.ID, t1.ID); err != nil {
		t.Fatalf("SetTournamentWinner 2x2: %v", err)
	}
	if err := st.ApplyTournamentMmr(ctx, tour2.ID); err != nil {
		t.Fatalf("ApplyTournamentMmr 2x2: %v", err)
	}
	assertTeam(e, 1016)
	assertTeam(f, 1016)
	assertTeam(g, 984)
	assertTeam(h, 984)
	// Игроки 2×2 НЕ получают персональный MMR (рейтинг у команды).
	if got, _ := st.GetUserMmr(ctx, e.ID, "2x2"); got != 1000 {
		t.Errorf("2x2 user_mmr(E) = %d, want 1000 (персонального 2×2 быть не должно)", got)
	}

	// Идемпотентность: повторный Apply не задваивает.
	if err := st.ApplyTournamentMmr(ctx, tour2.ID); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	assertTeam(e, 1016)
	assertTeam(g, 984)

	// Откат возвращает команды к 1000.
	if err := st.RevertTournamentMmr(ctx, tour2.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	assertTeam(e, 1000)
	assertTeam(g, 1000)

	// Командный лидерборд (за всё время) содержит команду-победителя из 1×1... нет — из 2×2.
	// После отката 2×2 истории нет, поэтому вернём начисление и проверим лидерборд.
	if err := st.ApplyTournamentMmr(ctx, tour2.ID); err != nil {
		t.Fatalf("re-apply for lb: %v", err)
	}
	rows, err := st.TeamLeaderboard(ctx, "")
	if err != nil {
		t.Fatalf("TeamLeaderboard: %v", err)
	}
	var foundWinner bool
	for _, r := range rows {
		if r.Mmr == 1016 && len(r.Members) == 2 {
			for _, m := range r.Members {
				if m.UserID == e.ID || m.UserID == f.ID {
					foundWinner = true
				}
			}
		}
	}
	if !foundWinner {
		t.Errorf("TeamLeaderboard не содержит команду-победителя (E,F) с MMR 1016")
	}

	// --- Статистика игрока 1×1 (A победил B) ---
	st1, tl1, mp1, op1, err := st.PlayerStatsBundle(ctx, a.ID)
	if err != nil {
		t.Fatalf("PlayerStatsBundle: %v", err)
	}
	if st1.CurrentMmr != 1016 || st1.Wins != 1 || st1.Losses != 0 || st1.Games != 1 {
		t.Errorf("player a stats = %+v, want mmr1016 W1 L0 G1", st1)
	}
	if st1.Winrate != 100 || st1.BestWinStreak != 1 || st1.Place < 1 {
		t.Errorf("player a stats winrate/streak/place = %+v", st1)
	}
	if len(tl1) != 1 || len(mp1) != 1 || len(op1) != 1 {
		t.Errorf("player a timeline/maps/opps len = %d/%d/%d, want 1/1/1", len(tl1), len(mp1), len(op1))
	}
	if len(op1) == 1 && op1[0].Login != b.Login {
		t.Errorf("player a opponent login = %q, want %q", op1[0].Login, b.Login)
	}

	// --- Статистика команды 2×2 (E,F) ---
	teams, err := st.TeamsForUser(ctx, e.ID)
	if err != nil {
		t.Fatalf("TeamsForUser: %v", err)
	}
	if len(teams) != 1 || teams[0].Mmr != 1016 || teams[0].Wins != 1 {
		t.Errorf("TeamsForUser(e) = %+v, want 1 team mmr1016 W1", teams)
	}
	tk1, tk2 := e.ID, f.ID
	if tk1 > tk2 {
		tk1, tk2 = tk2, tk1
	}
	tp, ok, err := st.TeamProfile(ctx, tk1+"|"+tk2)
	if err != nil || !ok {
		t.Fatalf("TeamProfile: ok=%v err=%v", ok, err)
	}
	if tp.Stats.CurrentMmr != 1016 || tp.Stats.Wins != 1 || tp.Stats.Games != 1 {
		t.Errorf("team stats = %+v, want mmr1016 W1 G1", tp.Stats)
	}
	if len(tp.Timeline) != 1 || len(tp.Opponents) != 1 {
		t.Errorf("team timeline/opps len = %d/%d, want 1/1", len(tp.Timeline), len(tp.Opponents))
	}
}
