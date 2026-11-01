package api

import "testing"

func TestTaskTagDefaultsByCategory(t *testing.T) {
	cases := []struct {
		body taskBody
		want string
	}{
		{taskBody{Text: "задание", Category: "task"}, "normal"},
		{taskBody{Text: "протокол", Category: "protocol"}, "timed"},
		{taskBody{Text: "задание на время", Category: "task", Tag: "timed"}, "timed"},
		{taskBody{Text: "протокол без спешки", Category: "protocol", Tag: "normal"}, "normal"},
		{taskBody{Text: "незнакомый тег", Category: "task", Tag: "hard"}, "normal"},
	}
	for _, c := range cases {
		got, err := c.body.toModel()
		if err != nil || got.Tag != c.want {
			t.Errorf("%q: тег %q, ждали %q", c.body.Text, got.Tag, c.want)
		}
	}
}

func TestTaskClassDefaultsAndValidation(t *testing.T) {
	got, err := taskBody{Text: "старое задание"}.toModel()
	if err != nil {
		t.Fatalf("тело без новых полей: %v", err)
	}
	if got.Format != "any" || got.Rarity != "common" || got.RoundType != "any" || got.Points != 2 {
		t.Errorf("умолчания старого клиента: %+v", got)
	}

	full, err := taskBody{Text: "x", Format: "2x2", Rarity: "epic", RoundType: "paid", Tag: "timed", Points: 3}.toModel()
	if err != nil {
		t.Fatalf("корректное задание: %v", err)
	}
	if full.Format != "2x2" || full.Rarity != "epic" || full.RoundType != "paid" || full.Tag != "timed" || full.Points != 3 {
		t.Errorf("новые поля потеряны: %+v", full)
	}

	for _, bad := range []taskBody{
		{Text: "x", Format: "3x3"},
		{Text: "x", Rarity: "mythic"},
		{Text: "x", RoundType: "vip"},
	} {
		if _, err := bad.toModel(); err == nil {
			t.Errorf("%+v: ждали ошибку валидации", bad)
		}
	}
}

// Баллы по умолчанию зависят только от «на время», редкость их не меняет; ручные баллы не трогаем.
func TestTaskDefaultPoints(t *testing.T) {
	cases := []struct {
		body taskBody
		want int
	}{
		{taskBody{Text: "x", Rarity: "common"}, 2},
		{taskBody{Text: "x", Rarity: "legendary"}, 2},
		{taskBody{Text: "x", Rarity: "rare", Tag: "timed"}, 1},
		{taskBody{Text: "x", Category: "protocol"}, 1},
		{taskBody{Text: "x", Rarity: "epic", Points: 5}, 5},
	}
	for _, c := range cases {
		got, err := c.body.toModel()
		if err != nil || got.Points != c.want {
			t.Errorf("%+v: баллы %d (%v), ждали %d", c.body, got.Points, err, c.want)
		}
	}
}

// Редкость и «на время» независимы: любая комбинация сохраняется как есть.
func TestRarityIndependentFromTimed(t *testing.T) {
	for _, rarity := range []string{"common", "uncommon", "rare", "epic", "legendary"} {
		for _, tag := range []string{"normal", "timed"} {
			got, err := taskBody{Text: "x", Rarity: rarity, Tag: tag, Category: "task"}.toModel()
			if err != nil || got.Rarity != rarity || got.Tag != tag {
				t.Errorf("%s + %s: получили %s + %s (%v)", rarity, tag, got.Rarity, got.Tag, err)
			}
		}
	}
}
