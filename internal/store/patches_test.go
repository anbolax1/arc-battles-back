package store

import (
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
)

// patchCase собирает матчи сезона: каждый матч - стороны A и B, победитель, очки и рейтинг до матча.
type patchCase struct {
	in       patchInput
	day      time.Time
	n, total int // матчей за день и всего: от первого - час матча, от второго - его id
}

func newPatchCase(status string) *patchCase {
	start := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	return &patchCase{
		in:  patchInput{season: models.Season{ID: "s3", Status: status, StartedAt: start}, prevGames: map[string]int{}},
		day: start.Add(7 * 24 * time.Hour),
	}
}

type sideSpec struct {
	user          string
	total         int
	before, after int
	knocks        map[int]int
	round1        int
}

func (c *patchCase) match(winner int, a, b sideSpec, opts ...func(*patchMatch)) *patchMatch {
	c.n++
	c.total++
	m := patchMatch{
		id: "m" + string(rune('a'+c.total-1)), at: c.day.Add(time.Duration(c.n) * time.Hour),
		mult: 1, games: 1, winner: winner, roundMaps: map[int]string{1: "Космопорт", 2: "Дамба"},
	}
	for i, sp := range []sideSpec{a, b} {
		k := sp.knocks
		if k == nil {
			k = map[int]int{}
		}
		m.sides[i] = patchSide{pid: m.id + sp.user, user: sp.user, name: sp.user, total: sp.total, rated: true,
			before: sp.before, after: sp.after, knocks: k, round1: sp.round1, hasRound1: sp.round1 > 0}
	}
	for _, o := range opts {
		o(&m)
	}
	c.in.matches = append(c.in.matches, m)
	return &c.in.matches[len(c.in.matches)-1]
}

func (c *patchCase) nextDay() { c.day = c.day.Add(24 * time.Hour); c.n = 0 }

func awardsOf(list []patchAward, user string) map[string]patchAward {
	out := map[string]patchAward{}
	for _, a := range list {
		if a.user == user {
			out[a.code+string(rune('0'+a.tier))] = a
		}
	}
	return out
}

func TestPatchStreakRevengeDouble(t *testing.T) {
	c := newPatchCase("active")
	c.match(1, sideSpec{user: "makar", before: 1000, after: 950}, sideSpec{user: "eden", before: 1000, after: 1050})
	for i := 0; i < 5; i++ {
		c.match(0, sideSpec{user: "makar", before: 950 + i*40, after: 990 + i*40}, sideSpec{user: "x", before: 1000, after: 960})
	}
	c.match(0, sideSpec{user: "makar", before: 1150, after: 1250}, sideSpec{user: "eden", before: 1050, after: 950},
		func(m *patchMatch) { m.mult = 2 })
	got := awardsOf(computeSeasonPatches(c.in), "makar")
	if a, ok := got["streak1"]; !ok || a.match != "mf" {
		t.Fatalf("серия из пяти побед: нашивка в пятой победе, а не %+v", a)
	}
	if got["streak1"].detail["best"] != 6 {
		t.Fatalf("лучшая серия 6, а не %v", got["streak1"].detail["best"])
	}
	if a, ok := got["revenge1"]; !ok || a.detail["opp"] != "eden" {
		t.Fatalf("реванш у eden: %+v", a)
	}
	if a, ok := got["double1"]; !ok || a.match != "mg" {
		t.Fatalf("двойная ставка в матче ×2: %+v", a)
	}
	if _, ok := got["first1"]; !ok {
		t.Fatalf("играл в первый день сезона")
	}
}

func TestPatchKnocks(t *testing.T) {
	c := newPatchCase("active")
	c.match(0, sideSpec{user: "od", total: 20, knocks: map[int]int{1: 4, 2: 3}}, sideSpec{user: "cheb", total: 12, knocks: map[int]int{1: 1}})
	c.match(0, sideSpec{user: "od", total: 30, knocks: map[int]int{1: 9}}, sideSpec{user: "takens", total: 10})
	live := c.match(-1, sideSpec{user: "od", knocks: map[int]int{1: 10}}, sideSpec{user: "orochi"})
	live.live = true
	c.match(0, sideSpec{user: "takens", total: 5}, sideSpec{user: "cheb", total: 3, knocks: map[int]int{2: 1}})
	all := computeSeasonPatches(c.in)

	od := awardsOf(all, "od")
	if a, ok := od["hunter1"]; !ok || a.match != "mb" {
		t.Fatalf("десятый нок во втором матче - «Охотник I»: %+v", a)
	}
	if a, ok := od["hunter2"]; !ok || a.match != "mc" {
		t.Fatalf("25 ноков набраны в идущем матче - «Охотник II» сразу: %+v", a)
	}
	if a, ok := od["clear1"]; !ok || a.match != "mb" || a.detail["knocks"] != 10 {
		t.Fatalf("«Зачистка»: 8+ ноков за рейд, лучший рейд 10: %+v", a)
	}
	if a, ok := od["topknock1"]; !ok || !a.provisional {
		t.Fatalf("«Главный охотник» в идущем сезоне - предварительный: %+v", a)
	}
	if _, ok := awardsOf(all, "takens")["pacifist1"]; !ok {
		t.Fatalf("победа без ноков, когда у соперника ноки есть, - «Пацифист»")
	}
	if _, ok := awardsOf(all, "od")["pacifist1"]; ok {
		t.Fatalf("у победителя с ноками «Пацифиста» нет")
	}
}

func TestPatchScoresAndLeader(t *testing.T) {
	c := newPatchCase("active")
	c.match(0, sideSpec{user: "eden", total: 9, before: 1000, after: 1100}, sideSpec{user: "mol", total: 0, before: 1000, after: 900})
	c.match(0, sideSpec{user: "makar", total: 7, before: 1000, after: 1080}, sideSpec{user: "eden", total: 6, before: 1100, after: 1020})
	c.match(0, sideSpec{user: "under", total: 4, round1: 1, before: 900, after: 1000}, sideSpec{user: "makar", total: 3, round1: 3, before: 1300, after: 1200})
	all := computeSeasonPatches(c.in)

	if a, ok := awardsOf(all, "eden")["shutout1"]; !ok || a.detail["opp"] != "mol" {
		t.Fatalf("соперник без очков - «Всухую»: %+v", a)
	}
	mk := awardsOf(all, "makar")
	if _, ok := mk["photo1"]; !ok {
		t.Fatalf("победа в одно очко - «Фотофиниш»")
	}
	if a, ok := mk["regicide1"]; !ok || a.detail["opp"] != "eden" {
		t.Fatalf("eden был первым перед матчем - «Цареубийца»: %+v", a)
	}
	un := awardsOf(all, "under")
	if _, ok := un["david1"]; !ok {
		t.Fatalf("900 против 1300 - шанс меньше 40%%, «Против шансов»")
	}
	if _, ok := un["comeback1"]; !ok {
		t.Fatalf("проиграл первый раунд и выиграл матч - «Камбэк»")
	}
}

func TestPatchBeltAndSeasonEnd(t *testing.T) {
	c := newPatchCase("finished")
	c.in.prevChamp = "makar"
	c.in.prevGames = map[string]int{"makar": 25}
	c.in.leader, c.in.leaderMmr = "eden", 1300
	c.match(0, sideSpec{user: "makar"}, sideSpec{user: "x"})
	c.match(0, sideSpec{user: "eden"}, sideSpec{user: "makar"})
	c.nextDay()
	c.match(0, sideSpec{user: "eden"}, sideSpec{user: "x"})
	all := computeSeasonPatches(c.in)

	mk := awardsOf(all, "makar")
	if a, ok := mk["belt1"]; !ok || a.match != "" {
		t.Fatalf("чемпион прошлого сезона начинает сезон с поясом: %+v", a)
	}
	if mk["belt1"].detail["defenses"] != 1 {
		t.Fatalf("одна защита пояса, а не %v", mk["belt1"].detail["defenses"])
	}
	if _, ok := mk["veteran1"]; !ok {
		t.Fatalf("играл в прошлом сезоне - «Ветеран»")
	}
	ed := awardsOf(all, "eden")
	if a, ok := ed["belt1"]; !ok || a.match != "mb" {
		t.Fatalf("пояс переходит к победителю носителя: %+v", a)
	}
	if a, ok := ed["top11"]; !ok || a.provisional {
		t.Fatalf("закрытый сезон - «Первый номер» окончательный: %+v", a)
	}
	if a, ok := ed["final1"]; !ok || a.match != "mc" || patchDay(a.at) != patchDay(c.day) {
		t.Fatalf("победа в последний день сезона - «Последний аккорд»: %+v", a)
	}
	if _, ok := mk["final1"]; ok {
		t.Fatalf("в последний день makar не играл")
	}
}
