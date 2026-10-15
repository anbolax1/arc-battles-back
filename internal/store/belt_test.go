package store

import (
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
)

// beltCase собирает матчи по дням: каждый вызов day() - новый игровой день в 15:00 по Москве.
type beltCase struct {
	in  beltInput
	day time.Time
	n   int
}

func newBeltCase() *beltCase {
	return &beltCase{in: beltInput{current: "s3"}, day: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
}

func (c *beltCase) nextDay() { c.day = c.day.AddDate(0, 0, 1) }

// play - матч в текущий день: победил a, mmr - рейтинг сторон после матча.
func (c *beltCase) play(a, b string, opts ...func(*beltMatch)) *beltMatch {
	c.n++
	m := beltMatch{
		id: "m" + string(rune('a'+c.n-1)), at: c.day.Add(time.Duration(c.n) * time.Minute), season: "s3", games: 1, winner: 0,
		sides: [2]beltSide{{user: a, rated: true, after: 1000 + c.n}, {user: b, rated: true, after: 1000 - c.n}},
	}
	for _, o := range opts {
		o(&m)
	}
	c.in.matches = append(c.in.matches, m)
	return &c.in.matches[len(c.in.matches)-1]
}

func show(m *beltMatch) { m.show = true }
func draw(m *beltMatch) { m.winner = -1 }

// rated - MMR сторон после матча, чтобы матч не менял лидера таблицы.
func rated(a, b int) func(*beltMatch) {
	return func(m *beltMatch) { m.sides[0].after, m.sides[1].after = a, b }
}

func holders(st beltState) []string {
	out := []string{}
	for _, r := range st.reigns {
		out = append(out, r.holder+":"+r.how)
	}
	return out
}

func TestBeltPassesToWinnerOfHolder(t *testing.T) {
	c := newBeltCase()
	c.play("eden", "x") // первый матч лиги
	c.play("eden", "y")
	c.play("eden", "z", draw)
	c.play("makar", "eden")
	c.play("x", "y") // носителя нет в матче - пояс не трогается
	st := computeBelt(c.in, c.day)

	if got := holders(st); len(got) != 2 || got[0] != "eden:first" || got[1] != "makar:take" {
		t.Fatalf("eden с первого матча, потом makar обыграл носителя: %v", got)
	}
	eden := st.reigns[0]
	if len(eden.defs) != 1 || eden.end != "lost" || eden.lostTo != "makar" {
		t.Fatalf("у eden одна защита, ничья защитой не считается, проиграл makar: %+v", eden)
	}
}

func TestBeltIgnoresShowMatches(t *testing.T) {
	c := newBeltCase()
	c.play("eden", "x")
	c.play("makar", "eden", show)
	st := computeBelt(c.in, c.day)
	if got := holders(st); len(got) != 1 || got[0] != "eden:first" {
		t.Fatalf("шоу-матч пояс не разыгрывает: %v", got)
	}
}

func TestBeltRustsAfterIdleGameDays(t *testing.T) {
	c := newBeltCase()
	c.play("eden", "x")
	for i := 0; i < BeltRustDays-1; i++ {
		c.nextDay()
		c.play("a", "b") // игровой день без носителя
	}
	c.nextDay()
	c.play("a", "b") // восьмой пропущенный - день ещё идёт, носитель может успеть
	st := computeBelt(c.in, c.day.Add(2*time.Hour))
	if st.rust != BeltRustDays-1 || len(st.reigns) != 1 || st.reigns[0].end != "" {
		t.Fatalf("семь закончившихся игровых дней без матча - ржавчина 7, пояс ещё у eden: rust %d, %+v", st.rust, st.reigns)
	}

	st = computeBelt(c.in, c.day.AddDate(0, 0, 1))
	r := st.reigns[0]
	if r.end != "drop" || patchDay(r.to) != patchDay(c.day.AddDate(0, 0, 1)) {
		t.Fatalf("восьмой игровой день закончился - пояс выпал в полночь: %+v", r)
	}
	if len(st.leaders) == 0 || st.leaders[0] != "a" {
		t.Fatalf("ничей пояс ждёт матча лидера таблицы - a: %v", st.leaders)
	}
}

func TestBeltRustWaitsBetweenSeasons(t *testing.T) {
	c := newBeltCase()
	c.play("eden", "x")
	// месяц без матчей в лиге - игровых дней нет, ржавчина не растёт
	st := computeBelt(c.in, c.day.AddDate(0, 1, 0))
	if st.rust != 0 || st.reigns[0].end != "" {
		t.Fatalf("без матчей в лиге пояс не ржавеет: rust %d, %+v", st.rust, st.reigns[0])
	}
}

func TestBeltVacantGoesToLeaderMatch(t *testing.T) {
	c := newBeltCase()
	c.play("eden", "x")
	for i := 0; i < BeltRustDays; i++ {
		c.nextDay()
		c.play("lead", "b") // lead поднимается в таблице
	}
	c.nextDay()
	c.play("c", "d", rated(990, 980)) // не лидер - пояс по-прежнему ничей
	c.play("y", "lead")               // лидер проиграл - пояс у того, кто его обыграл
	st := computeBelt(c.in, c.day)
	if got := holders(st); len(got) != 2 || got[1] != "y:pick" {
		t.Fatalf("ничей пояс подбирает победитель матча лидера: %v", got)
	}
	if st.reigns[0].end != "drop" {
		t.Fatalf("у eden пояс выпал: %+v", st.reigns[0])
	}
}

func TestBeltHolderMatchKeepsBeltFromRusting(t *testing.T) {
	c := newBeltCase()
	c.play("eden", "x")
	for i := 0; i < BeltRustDays-1; i++ {
		c.nextDay()
		c.play("a", "b")
	}
	c.nextDay()
	c.play("eden", "y") // успел на восьмой игровой день
	for i := 0; i < 3; i++ {
		c.nextDay()
		c.play("a", "b")
	}
	st := computeBelt(c.in, c.day.AddDate(0, 0, 1))
	if len(st.reigns) != 1 || st.reigns[0].end != "" || st.rust != 3 {
		t.Fatalf("матч на восьмой день снимает ржавчину: rust %d, %+v", st.rust, st.reigns)
	}
}

func TestBeltForSeasonAndHonor(t *testing.T) {
	c := newBeltCase()
	c.play("eden", "x")
	for i := 0; i < BeltHonorDefenses; i++ {
		c.nextDay()
		c.play("eden", "y")
	}
	s4 := c.day.AddDate(0, 0, 10)
	c.day = s4.Add(time.Hour)
	m := c.play("makar", "eden")
	m.season = "s4"
	st := computeBelt(c.in, c.day)

	got := st.forSeason(models.Season{ID: "s4", StartedAt: s4})
	if got.start != "eden" || got.takes[m.id] != "makar" || got.reigns["eden"] != 1 || got.defenses["eden"] != 0 {
		t.Fatalf("в 4 сезон eden вошёл с поясом, makar забрал его в матче сезона: %+v", got)
	}
	if h := st.honored(); len(h) != 1 || h[0] != "eden" {
		t.Fatalf("10 защит за всё время - почётный носитель eden: %v", h)
	}
}
