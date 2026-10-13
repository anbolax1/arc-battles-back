package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
)

func TestMatchRounds(t *testing.T) {
	cases := []struct {
		format string
		rounds int
		want   int
	}{
		{FormatMatch, 0, 2}, // поле не прислали - обычный матч на два раунда
		{FormatMatch, 2, 2},
		{FormatMatch, 3, 3},
		{FormatMatch, 5, 2},
		{FormatShow, 0, 3},
		{FormatShow, 2, 3}, // шоу-матч всегда на три раунда
	}
	for _, c := range cases {
		if got := MatchRounds(c.format, c.rounds); got != c.want {
			t.Errorf("MatchRounds(%q, %d) = %d, want %d", c.format, c.rounds, got, c.want)
		}
	}
}

func vetoText(order []models.VetoStep) string {
	parts := make([]string, len(order))
	for i, s := range order {
		parts[i] = strings.TrimSpace(s.Action + " " + s.Side)
		if s.Round > 0 {
			parts[i] += fmt.Sprintf(" %d", s.Round)
		}
	}
	return strings.Join(parts, ", ")
}

// Пики-баны раздают карты ровно на все раунды матча, без пропусков.
func TestVetoOrderCoversRounds(t *testing.T) {
	for _, rounds := range []int{2, 3} {
		for _, pool := range []int{6, 7, 8} {
			seen := map[int]bool{}
			for _, st := range VetoOrder(rounds, pool) {
				if st.Round > 0 {
					seen[st.Round] = true
				}
			}
			for r := 1; r <= rounds; r++ {
				if !seen[r] {
					t.Errorf("VetoOrder(%d, %d): нет карты на раунд %d", rounds, pool, r)
				}
			}
			if len(seen) != rounds {
				t.Errorf("VetoOrder(%d, %d): карты на %d раундов", rounds, pool, len(seen))
			}
		}
	}
	if got := vetoText(VetoOrder(1, 7)); got != vetoText(VetoOrder(2, 7)) {
		t.Errorf("VetoOrder(1) должен совпадать с двухраундовым: %s", got)
	}
}

// Семь карт: в двух раундах вторую карту выбирает B и одна карта не играется, в трёх баны идут
// до последней карты - она уходит в 3-й раунд.
func TestVetoOrderSevenMaps(t *testing.T) {
	cases := []struct {
		rounds int
		want   string
	}{
		{2, "ban A, ban B, pick A 1, ban B, ban A, pick B 2"},
		{3, "pick A 1, pick B 2, ban A, ban B, ban A, ban B, rest 3"},
	}
	for _, c := range cases {
		if got := vetoText(VetoOrder(c.rounds, 7)); got != c.want {
			t.Errorf("VetoOrder(%d, 7) = %s, want %s", c.rounds, got, c.want)
		}
	}
}

// В трёх раундах ходов ровно столько, сколько карт в пуле: последний бан решает, какая карта останется.
func TestVetoOrderThreeRoundsUsesWholePool(t *testing.T) {
	for _, pool := range []int{5, 6, 7, 8} {
		if n := len(VetoOrder(3, pool)); n != pool {
			t.Errorf("VetoOrder(3, %d): %d ходов, а карт %d", pool, n, pool)
		}
	}
}
