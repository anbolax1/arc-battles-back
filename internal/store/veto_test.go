package store

import "testing"

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

// Пики-баны раздают карты ровно на все раунды матча, без пропусков.
func TestVetoOrderCoversRounds(t *testing.T) {
	for _, rounds := range []int{2, 3} {
		seen := map[int]bool{}
		for _, st := range VetoOrder(rounds) {
			if st.Round > 0 {
				seen[st.Round] = true
			}
		}
		for r := 1; r <= rounds; r++ {
			if !seen[r] {
				t.Errorf("VetoOrder(%d): нет карты на раунд %d", rounds, r)
			}
		}
		if len(seen) != rounds {
			t.Errorf("VetoOrder(%d): карты на %d раундов", rounds, len(seen))
		}
	}
	if got := VetoOrder(1); len(got) != len(VetoOrder(2)) || got[0] != VetoOrder(2)[0] {
		t.Errorf("VetoOrder(1) должен совпадать с двухраундовым")
	}
	if first := VetoOrder(3)[0]; first.Action != "pick" || first.Side != "A" {
		t.Errorf("три раунда начинаются с пика A, как в шоу-матче: %+v", first)
	}
}
