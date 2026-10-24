package store

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
)

func TestWinTieLead(t *testing.T) {
	// A: 0 или 3 поровну, B: всегда 1. При равном старте A выигрывает в половине случаев.
	a := []float64{0.5, 0, 0, 0.5}
	b := []float64{0, 1}
	if w, tie := winTie(a, b, 0); math.Abs(w-0.5) > 1e-12 || tie != 0 {
		t.Fatalf("без форы: %v %v", w, tie)
	}
	// Фора 1: при 0 у A счёт 1:1 - ничья.
	if w, tie := winTie(a, b, 1); math.Abs(w-0.5) > 1e-12 || math.Abs(tie-0.5) > 1e-12 {
		t.Fatalf("фора 1: %v %v", w, tie)
	}
	// A отстаёт на 5: не догонит.
	if w, tie := winTie(a, b, -5); w != 0 || tie != 0 {
		t.Fatalf("отставание 5: %v %v", w, tie)
	}
}

func TestRaidPMFKnocksSoFar(t *testing.T) {
	p := raidParams{Knocks: 1.5, Adj: [3]float64{1, 0, 0}}
	pmf := raidPMF(nil, p.Knocks, 2, p.Adj)
	for i := 0; i < 6; i++ {
		if pmf[i] != 0 {
			t.Fatalf("два нока уже есть: очков меньше 6 быть не может, у %d вероятность %v", i, pmf[i])
		}
	}
	sum := 0.0
	for _, x := range pmf {
		sum += x
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("сумма %v", sum)
	}
}

// Модель на Go должна давать те же шансы, что эталон исследования: 350 матчей 2-4 сезонов,
// каждый оценён по матчам до него.
func TestChanceHistoryMatchesReference(t *testing.T) {
	raw, err := os.ReadFile("testdata/chance_history.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Matches  []chanceMatchRec `json:"matches"`
		Expected map[string]struct {
			Pre   [3]float64   `json:"pre"`
			Raids [][3]float64 `json:"raids"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	got, _ := chanceHistory(fx.Matches)
	const tol = 1e-6
	near := func(c models.Chance, want [3]float64) bool {
		return math.Abs(c.A-want[0]) < tol && math.Abs(c.Draw-want[1]) < tol && math.Abs(c.B-want[2]) < tol
	}
	bad := 0
	for _, m := range fx.Matches {
		want := fx.Expected[m.ID]
		g := got[m.ID]
		if !near(g.Pre, want.Pre) {
			bad++
			if bad <= 5 {
				t.Errorf("%s перед матчем: %+v, эталон %v", m.ID, g.Pre, want.Pre)
			}
		}
		if len(g.Raids) != len(want.Raids) {
			t.Errorf("%s: %d точек после рейдов, эталон %d", m.ID, len(g.Raids), len(want.Raids))
			continue
		}
		for i := range want.Raids {
			if !near(g.Raids[i], want.Raids[i]) {
				bad++
				if bad <= 5 {
					t.Errorf("%s после рейда %d: %+v, эталон %v", m.ID, i+1, g.Raids[i], want.Raids[i])
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("расхождений с эталоном: %d", bad)
	}
}

// Незасчитанный матч: рейды пострадавшего не тянут его темп вниз, исход не двигает скрытый рейтинг.
func TestChanceVoidedMatch(t *testing.T) {
	raid := func(knocks int) chanceRaidRec {
		return chanceRaidRec{Knocks: knocks, Points: 3 * knocks, Adj: 0, Tasks: []chanceTaskRec{{Kind: taskProtocol}, {Kind: taskGeneral}, {Kind: taskOnMap}}}
	}
	rec := chanceMatchRec{
		ID: "m", Users: [2]string{"winner", "victim"}, Winner: 0, Games: 1, Detailed: true, Voided: true,
		Rounds: []chanceRoundRec{{Kit: kitFree, Played: true, Raids: [2]chanceRaidRec{raid(3), raid(0)}}},
	}
	m := newChanceModel()
	m.learn(rec)
	if _, ok := m.knocks["victim"]; ok {
		t.Fatalf("рейд пострадавшего не должен попасть в его темп")
	}
	if _, ok := m.knocks["winner"]; !ok {
		t.Fatalf("рейд победителя учитывается")
	}
	if m.ratingOf("winner") != chanceRatingStart || m.ratingOf("victim") != chanceRatingStart {
		t.Fatalf("исход незасчитанного матча не двигает рейтинг: %v %v", m.ratingOf("winner"), m.ratingOf("victim"))
	}
}

// Все рейды сыграны, счёт равный: ничья наверняка, а шанс стороны - её шанс выиграть дуэль до первого нока.
func TestChanceTiedGoesToDuel(t *testing.T) {
	st := matchSetup{RatingP: 0.5, Duel: 0.7}
	c := splitChance(sideRaids{base: 9}, sideRaids{base: 9}, st, 1)
	if c.Draw != 1 || c.A <= 0.5 || math.Abs(c.A+c.B-1) > 1e-12 {
		t.Fatalf("дуэль при равном счёте: %+v", c)
	}
	// У кого ноков больше, тот и дуэль выигрывает чаще.
	m := newChanceModel()
	tally(m.knocks, "hunter", 30, 10)
	if s := m.setup([2]string{"hunter", "calm"}, nil); s.Duel <= 0.5 {
		t.Fatalf("дуэль у того, кто чаще нокает: %v", s.Duel)
	}
}
