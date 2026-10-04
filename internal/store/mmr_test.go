package store

import "testing"

// Сверка формулы Elo K=32 с таблицей 2 сезона: равные рейтинги → ±16; матч 1047 vs 954 → +12;
// тот же матч по жетону ×2 (там это два матча) → +23 (12, затем 11 от уже обновлённого рейтинга).
func TestEloWinMagnitude(t *testing.T) {
	cases := []struct {
		winner, loser, mult, want int
	}{
		{1000, 1000, 1, 16},
		{1000, 1000, 2, 31}, // 16, затем 15 (1016 vs 984)
		{1047, 954, 1, 12},
		{1047, 954, 2, 23}, // 12, затем 11 (1059 vs 942)
		{954, 1047, 1, 20}, // андердог выигрывает: больше очков
	}
	for _, c := range cases {
		if got := eloWinMagnitude(c.winner, c.loser, c.mult, c.mult, 32); got != c.want {
			t.Errorf("eloWinMagnitude(%d,%d,mult=%d) = %d, want %d", c.winner, c.loser, c.mult, got, c.want)
		}
	}
}

// Правила 3 сезона: K=100. Равные рейтинги → ±50; 1000 против 1054 → +58 (в таблице SURPRISE011
// стал 1058, обыграв MADARA_SB с его 1054). ×2 - один матч с удвоенным изменением, как на arcarena:
// BLLACER 1150 против SHISHKOVK - +66.
func TestEloWinMagnitudeSeason3(t *testing.T) {
	cases := []struct {
		winner, loser, mult, games, want int
	}{
		{1000, 1000, 1, 1, 50},
		{1000, 1054, 1, 1, 58},
		{1000, 1000, 2, 1, 100}, // 2×50
		{1000, 1000, 2, 2, 86},  // ×2 таблицы - два матча: 50, затем 36 (1050 vs 950)
		{1015, 1352, 1, 1, 87},
	}
	for _, c := range cases {
		if got := eloWinMagnitude(c.winner, c.loser, c.mult, c.games, 100); got != c.want {
			t.Errorf("K=100 eloWinMagnitude(%d,%d,mult=%d) = %d, want %d", c.winner, c.loser, c.mult, got, c.want)
		}
	}
}

func TestEloZeroSumAndBounds(t *testing.T) {
	// mult<1 нормализуется к 1.
	if eloWinMagnitude(1000, 1000, 0, 0, 32) != 16 {
		t.Errorf("mult=0 должен вести себя как mult=1")
	}
}
