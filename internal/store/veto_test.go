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

// Два раунда: по два бана, первую карту берёт B, вторую - A; три раунда: третью карту выбирает чат.
// У прошлых матчей с одним раундом пиков-банов не было - для них порядок двухраундовый.
func TestVetoOrder(t *testing.T) {
	cases := []struct {
		rounds int
		want   string
	}{
		{2, "ban A, ban B, ban A, ban B, pick B 1, pick A 2"},
		{3, "ban A, ban B, pick A 1, pick B 2, vote 3"},
		{1, "ban A, ban B, ban A, ban B, pick B 1, pick A 2"},
	}
	for _, c := range cases {
		if got := vetoText(VetoOrder(c.rounds)); got != c.want {
			t.Errorf("VetoOrder(%d) = %s, want %s", c.rounds, got, c.want)
		}
	}
}

// Сторона A - меньший MMR, при равном - меньше матчей в прошлых сезонах, при полном равенстве - жребий.
func TestAGoesFirst(t *testing.T) {
	heads := func() bool { return true }
	tails := func() bool { return false }
	cases := []struct {
		name string
		a, b sideInfo
		coin func() bool
		want bool
	}{
		{"меньше MMR ходит первым", sideInfo{mmr: 990, prevGames: 50}, sideInfo{mmr: 1010}, tails, true},
		{"больше MMR уступает", sideInfo{mmr: 1010}, sideInfo{mmr: 990, prevGames: 50}, heads, false},
		{"при равном MMR - кто меньше играл раньше", sideInfo{mmr: 1000, prevGames: 3}, sideInfo{mmr: 1000, prevGames: 12}, tails, true},
		{"опытный при равном MMR уступает", sideInfo{mmr: 1000, prevGames: 12}, sideInfo{mmr: 1000, prevGames: 3}, heads, false},
		{"два новичка - орёл", sideInfo{mmr: 1000}, sideInfo{mmr: 1000}, heads, true},
		{"два новичка - решка", sideInfo{mmr: 1000}, sideInfo{mmr: 1000}, tails, false},
	}
	for _, c := range cases {
		if got := aGoesFirst(c.a, c.b, c.coin); got != c.want {
			t.Errorf("%s: aGoesFirst = %v, want %v", c.name, got, c.want)
		}
	}
}
