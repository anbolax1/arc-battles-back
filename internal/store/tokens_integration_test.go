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

// Жетоны игрока и сброс рейтинга против живой Postgres. Запуск - как у TestMMRIntegration.
func TestTokensIntegration(t *testing.T) {
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
		login := fmt.Sprintf("tk_%d_%s", uniq, tag)
		u, err := st.CreateUser(ctx, login, login, "x", models.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
		return u
	}
	grant := func(u models.User, kind string, n int) {
		t.Helper()
		if err := st.GrantTokens(ctx, u.ID, kind, n, "тест", ""); err != nil {
			t.Fatalf("GrantTokens(%s, %s): %v", u.Login, kind, err)
		}
	}
	wallet := func(u models.User) models.TokenWallet {
		t.Helper()
		w, err := st.TokenWallet(ctx, u.ID)
		if err != nil {
			t.Fatalf("TokenWallet: %v", err)
		}
		return w
	}
	// match - матч до старта: первый раунд ещё не начат, жетоны можно ставить.
	match := func(mode string, a, b models.Participant) (string, models.Participant) {
		t.Helper()
		tour, err := st.CreateTournament(ctx, models.Tournament{Title: "tk-" + mode, Mode: mode, RatingMultiplier: 1})
		if err != nil {
			t.Fatalf("CreateTournament: %v", err)
		}
		a.TournamentID, b.TournamentID = tour.ID, tour.ID
		pa, err := st.AddParticipant(ctx, a)
		if err != nil {
			t.Fatalf("AddParticipant a: %v", err)
		}
		if _, err := st.AddParticipant(ctx, b); err != nil {
			t.Fatalf("AddParticipant b: %v", err)
		}
		return tour.ID, pa
	}
	player := func(u models.User, seed int) models.Participant {
		return models.Participant{Kind: "player", UserID: &u.ID, Name: u.Login, Seed: seed}
	}
	team := func(x, y models.User, seed int) models.Participant {
		raw, _ := json.Marshal([]map[string]string{{"name": x.Login, "userId": x.ID}, {"name": y.Login, "userId": y.ID}})
		return models.Participant{Kind: "team", Name: x.Login + " & " + y.Login, Seed: seed, Members: raw}
	}
	// finish - матч начат, сторона pa берёт нок и побеждает.
	finish := func(tid string, pa models.Participant) {
		t.Helper()
		rd, err := st.CreateRound(ctx, models.Round{TournamentID: tid, Number: 1, Status: "live"})
		if err != nil {
			t.Fatalf("CreateRound: %v", err)
		}
		if _, err := st.AdjustKnocks(ctx, rd.ID, pa.ID, 1); err != nil {
			t.Fatalf("AdjustKnocks: %v", err)
		}
		if _, err := st.FinishMatch(ctx, tid); err != nil {
			t.Fatalf("FinishMatch: %v", err)
		}
	}
	delta := func(tid string, u models.User) int {
		t.Helper()
		var d int
		if err := pool.QueryRow(ctx, `SELECT delta FROM mmr_history WHERE tournament_id = $1 AND user_id = $2`, tid, u.ID).Scan(&d); err != nil {
			t.Fatalf("mmr_history: %v", err)
		}
		return d
	}
	mmr := func(u models.User) int {
		t.Helper()
		v, err := st.GetUserMmr(ctx, u.ID, "1x1")
		if err != nil {
			t.Fatalf("GetUserMmr: %v", err)
		}
		return v
	}
	start := st.ActiveSeasonStart(ctx)

	// --- ×2 в 1×1: удваивает только тому, кто поставил, и в победе, и в поражении ---
	a, b, c, d := mkUser("a"), mkUser("b"), mkUser("c"), mkUser("d")
	grant(a, store.TokenDouble, 2)
	m1, pa := match("1x1", player(a, 1), player(b, 2))
	if err := st.SetDoubleToken(ctx, m1, a.ID, true, ""); err != nil {
		t.Fatalf("SetDoubleToken: %v", err)
	}
	if err := st.SetDoubleToken(ctx, m1, a.ID, true, ""); err != nil {
		t.Fatalf("повторная активация: %v", err)
	}
	if w := wallet(a); w.Double != 1 {
		t.Fatalf("жетонов ×2 у a после активации = %d, want 1 - повторное нажатие не тратит второй", w.Double)
	}
	if err := st.SetDoubleToken(ctx, m1, c.ID, true, ""); !errors.Is(err, store.ErrNotInMatch) {
		t.Errorf("жетон игрока не из матча: %v, want ErrNotInMatch", err)
	}
	if err := st.SetDoubleToken(ctx, m1, b.ID, true, ""); !errors.Is(err, store.ErrNoTokens) {
		t.Errorf("жетон без баланса: %v, want ErrNoTokens", err)
	}
	finish(m1, pa)
	if da, dl := delta(m1, a), delta(m1, b); da <= 0 || da != -2*dl {
		t.Errorf("победа с жетоном: a %+d, b %+d; want a вдвое больше потери b", da, dl)
	}
	if err := st.SetDoubleToken(ctx, m1, a.ID, false, ""); !errors.Is(err, store.ErrMatchStarted) {
		t.Errorf("снять жетон после матча: %v, want ErrMatchStarted", err)
	}

	m2, pc := match("1x1", player(c, 1), player(a, 2))
	if err := st.SetDoubleToken(ctx, m2, a.ID, true, ""); err != nil {
		t.Fatalf("SetDoubleToken m2: %v", err)
	}
	finish(m2, pc)
	if da, dc := delta(m2, a), delta(m2, c); dc <= 0 || da != -2*dc {
		t.Errorf("поражение с жетоном: a %+d, c %+d; want потеря a вдвое больше выигрыша c", da, dc)
	}

	// --- отмена матча и снятие жетона до старта возвращают его ---
	grant(d, store.TokenDouble, 1)
	m3, _ := match("1x1", player(d, 1), player(b, 2))
	if err := st.SetDoubleToken(ctx, m3, d.ID, true, ""); err != nil {
		t.Fatalf("SetDoubleToken m3: %v", err)
	}
	if err := st.SetDoubleToken(ctx, m3, d.ID, false, ""); err != nil || wallet(d).Double != 1 {
		t.Errorf("снять жетон до старта: %v, жетонов %d; want вернулся", err, wallet(d).Double)
	}
	if err := st.SetDoubleToken(ctx, m3, d.ID, true, ""); err != nil {
		t.Fatalf("SetDoubleToken m3 снова: %v", err)
	}
	if err := st.DeleteTournament(ctx, m3); err != nil {
		t.Fatalf("DeleteTournament: %v", err)
	}
	if w := wallet(d); w.Double != 1 {
		t.Errorf("жетонов ×2 после отмены матча = %d, want 1", w.Double)
	}

	// --- ×2 в 2×2: удваивает MMR всей команды, на сторону - один жетон ---
	e, f, g, h := mkUser("e"), mkUser("f"), mkUser("g"), mkUser("h")
	grant(e, store.TokenDouble, 1)
	grant(f, store.TokenDouble, 1)
	m4, pt := match("2x2", team(e, f, 1), team(g, h, 2))
	if err := st.SetDoubleToken(ctx, m4, e.ID, true, ""); err != nil {
		t.Fatalf("SetDoubleToken 2×2: %v", err)
	}
	if err := st.SetDoubleToken(ctx, m4, f.ID, true, ""); !errors.Is(err, store.ErrSideHasToken) {
		t.Errorf("второй жетон на ту же сторону: %v, want ErrSideHasToken", err)
	}
	finish(m4, pt)
	teamDelta := func(x, y models.User) int {
		t.Helper()
		var v int
		key := x.ID + "|" + y.ID
		if y.ID < x.ID {
			key = y.ID + "|" + x.ID
		}
		if err := pool.QueryRow(ctx, `SELECT delta FROM team_mmr_history WHERE tournament_id = $1 AND team_key = $2`, m4, key).Scan(&v); err != nil {
			t.Fatalf("team_mmr_history: %v", err)
		}
		return v
	}
	if dw, dl := teamDelta(e, f), teamDelta(g, h); dw <= 0 || dw != -2*dl {
		t.Errorf("2×2 с жетоном: команда e&f %+d, g&h %+d; want победители вдвое больше", dw, dl)
	}

	// --- сброс рейтинга: MMR снова стартовый, раз в сезон, пересчёт повторяет его ---
	if err := st.ResetBlocked(ctx, a.ID); !errors.Is(err, store.ErrNoTokens) {
		t.Errorf("сброс без жетона: %v, want ErrNoTokens", err)
	}
	grant(a, store.TokenReset, 2)
	live, _ := match("1x1", player(a, 1), player(d, 2))
	if _, err := pool.Exec(ctx, `UPDATE tournaments SET status = 'live' WHERE id = $1`, live); err != nil {
		t.Fatalf("матч в эфир: %v", err)
	}
	if err := st.ResetRating(ctx, a.ID); !errors.Is(err, store.ErrInMatch) {
		t.Errorf("сброс во время матча: %v, want ErrInMatch", err)
	}
	if err := st.DeleteTournament(ctx, live); err != nil {
		t.Fatalf("DeleteTournament: %v", err)
	}
	if mmr(a) == start {
		t.Fatalf("MMR a = %d - тест ждёт рейтинг не со старта", mmr(a))
	}
	if err := st.ResetRating(ctx, a.ID); err != nil {
		t.Fatalf("ResetRating: %v", err)
	}
	if got := mmr(a); got != start {
		t.Errorf("MMR a после сброса = %d, want %d", got, start)
	}
	if err := st.ResetRating(ctx, a.ID); !errors.Is(err, store.ErrResetUsed) {
		t.Errorf("второй сброс в сезоне: %v, want ErrResetUsed", err)
	}
	if w := wallet(a); w.Reset != 1 || w.LastReset == nil || !w.ResetUsed {
		t.Errorf("кошелёк после сброса: %+v; want остался 1 жетон, дата сброса и отметка сезона", w)
	}
	m5, pa5 := match("1x1", player(a, 1), player(b, 2))
	finish(m5, pa5)
	var before5 int
	if err := pool.QueryRow(ctx, `SELECT mmr_before FROM mmr_history WHERE tournament_id = $1 AND user_id = $2`, m5, a.ID).Scan(&before5); err != nil {
		t.Fatalf("mmr_history: %v", err)
	}
	if before5 != start {
		t.Errorf("MMR a перед матчем после сброса = %d, want %d", before5, start)
	}
	want := map[string]int{a.ID: mmr(a), b.ID: mmr(b), c.ID: mmr(c)}
	if err := st.RecomputeAllMmr(ctx); err != nil {
		t.Fatalf("RecomputeAllMmr: %v", err)
	}
	for id, v := range want {
		if got, _ := st.GetUserMmr(ctx, id, "1x1"); got != v {
			t.Errorf("после полного пересчёта MMR %s = %d, want %d - жетоны и сброс должны повториться", id, got, v)
		}
	}
	_, timeline, _, err := st.PlayerStatsBundle(ctx, a.ID)
	if err != nil {
		t.Fatalf("PlayerStatsBundle: %v", err)
	}
	resets := 0
	for _, p := range timeline {
		if p.Reset {
			resets++
			if p.Mmr != start || p.Correction || p.Win {
				t.Errorf("точка сброса: %+v; want MMR %d, не сверка и не победа", p, start)
			}
		}
		if p.TournamentID == m1 && p.Mult != 2 {
			t.Errorf("матч с жетоном на графике: множитель %d, want 2", p.Mult)
		}
	}
	if resets != 1 {
		t.Errorf("сбросов на графике a: %d, want 1", resets)
	}

	// --- «Без очереди»: до конца игрового дня, повторно - через сутки, ошибочную активацию можно вернуть ---
	grant(b, store.TokenPriority, 2)
	if err := st.ActivatePriority(ctx, b.ID, ""); err != nil {
		t.Fatalf("ActivatePriority: %v", err)
	}
	if w := wallet(b); w.Priority != 1 || w.PriorityUntil == nil || w.PriorityNext == nil {
		t.Errorf("кошелёк после «Без очереди»: %+v", w)
	}
	if err := st.ActivatePriority(ctx, b.ID, ""); !errors.Is(err, store.ErrTokenCooldown) {
		t.Errorf("второй «Без очереди» за сутки: %v, want ErrTokenCooldown", err)
	}
	if err := st.CancelPriority(ctx, b.ID); err != nil || wallet(b).Priority != 2 {
		t.Errorf("отмена «Без очереди»: %v, жетонов %d; want вернулся", err, wallet(b).Priority)
	}
	if err := st.GrantTokens(ctx, b.ID, store.TokenPriority, -3, "", ""); !errors.Is(err, store.ErrTokenBalance) {
		t.Errorf("забрать больше, чем есть: %v, want ErrTokenBalance", err)
	}
}
