package api

import (
	"strings"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

func TestBeltNews(t *testing.T) {
	day := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, beltMoscow) }
	p := func(id, name string) models.BeltPlayer {
		return models.BeltPlayer{UserID: id, Login: id, DisplayName: name}
	}
	ptr := func(v time.Time) *time.Time { return &v }
	eden, makar, mol := p("eden", "EDEN"), p("makar", "MAKAR <b>"), p("mol", "MOLODOY")
	b := models.Belt{RustLimit: 8, HonorAt: 10, Leaders: []models.BeltPlayer{mol}, Reigns: []models.BeltReign{
		{Holder: eden, From: day(1, 20), To: ptr(day(4, 21)), How: "first", MatchID: "m1", End: "lost", Time: 1, Newcomer: 1,
			Defenses: []models.BeltDefense{{At: day(2, 20), MatchID: "d1", Opponent: mol}}},
		{Holder: makar, From: day(4, 21), To: ptr(day(15, 0)), How: "take", MatchID: "m2", Opponent: &eden, Score: []int{34, 21},
			End: "drop", Time: 1, Newcomer: 2, Defenses: []models.BeltDefense{}},
	}}

	take := beltNews(b, store.BeltEvent{Kind: "take", Reign: 1, Match: "m2"})
	for _, want := range []string{
		`<a href="/profile/makar">MAKAR &lt;b&gt;</a> обыграл носителя <a href="/profile/eden">EDEN</a> со счётом 34:21`,
		"EDEN держал пояс 3 дня и отбился 1 раз.",
		"MAKAR &lt;b&gt; — 2-й носитель в истории лиги.",
		`<a href="/tournament/m2">Разбор матча</a>`,
	} {
		if !strings.Contains(take.body, want) {
			t.Errorf("смена носителя: нет %q в\n%s", want, take.body)
		}
	}
	if clean := newsHTML(take.body); !strings.Contains(clean, `href="/profile/eden"`) {
		t.Errorf("ссылки на профиль срезаны при чистке:\n%s", clean)
	}
	if take.title != "MAKAR <b> забрал пояс лиги" || take.art != (models.NewsBelt{Name: "MAKAR <b>"}) {
		t.Errorf("смена носителя: заголовок %q, рисунок %+v", take.title, take.art)
	}

	drop := beltNews(b, store.BeltEvent{Kind: "drop", Reign: 1, At: day(15, 0)})
	for _, want := range []string{
		"8 игровых дней подряд не выходил на обычный матч, и пояс лиги выпал. Пояс был у него с 4 октября: 11 дней, ни одной защиты.",
		`ближайший обычный матч лидера таблицы сезона: <a href="/profile/mol">MOLODOY</a>.`,
	} {
		if !strings.Contains(drop.body, want) {
			t.Errorf("пояс выпал: нет %q в\n%s", want, drop.body)
		}
	}
	if strings.Contains(drop.body, "Разбор матча") || !drop.art.Vacant {
		t.Errorf("пояс выпал: у события нет матча, а пояс ничей: %+v\n%s", drop.art, drop.body)
	}

	honor := beltNews(b, store.BeltEvent{Kind: "honor", Reign: 0, Match: "d1", Honor: 2})
	if honor.title != "EDEN — почётный носитель пояса" || honor.art.Stars != 10 || !strings.Contains(honor.body, "Это 2-й почётный носитель") {
		t.Errorf("почётный носитель: %q, %+v\n%s", honor.title, honor.art, honor.body)
	}
}

func TestRuCount(t *testing.T) {
	for n, want := range map[int]string{1: "1 день", 2: "2 дня", 5: "5 дней", 11: "11 дней", 14: "14 дней", 21: "21 день", 22: "22 дня", 111: "111 дней"} {
		if got := ruCount(n, "день", "дня", "дней"); got != want {
			t.Errorf("ruCount(%d) = %q, want %q", n, got, want)
		}
	}
}
