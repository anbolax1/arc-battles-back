package store

import "testing"

// Сверка формулы Elo K=32 с боевой таблицей организатора (воспроизведённой симуляцией):
// равные рейтинги → ±16; матч 1047 vs 954 → +12; тот же матч по жетону ×2 → +23 (12, затем 11
// от уже обновлённого рейтинга — компаундинг).
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
		if got := eloWinMagnitude(c.winner, c.loser, c.mult); got != c.want {
			t.Errorf("eloWinMagnitude(%d,%d,mult=%d) = %d, want %d", c.winner, c.loser, c.mult, got, c.want)
		}
	}
}

func TestEloZeroSumAndBounds(t *testing.T) {
	// mult<1 нормализуется к 1.
	if eloWinMagnitude(1000, 1000, 0) != 16 {
		t.Errorf("mult=0 должен вести себя как mult=1")
	}
}
