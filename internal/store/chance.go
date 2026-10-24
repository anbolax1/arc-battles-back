package store

import (
	"math"

	"github.com/battle-for-respect/backend/internal/models"
)

// Шанс на победу считается по рейдам. Очки рейда - протокол и два задания, каждое выполняется с личной
// вероятностью рейдера, ноки с его личным темпом и ручные очки, как в среднем по лиге. Рейды соперников
// не пересекаются: шанс - вероятность, что итог одной стороны больше. При равном счёте матч решает дуэль
// до первого нока, её доля входит в шанс каждой стороны, а ничья показывает, как часто до неё доходит.

const (
	chancePointsCap = 220 // очков стороны за матч: дальше вероятности ничтожны
	chanceKnocksCap = 45  // ноков за рейд
	// Сколько ожидаемых ноков и заданий нужно, чтобы личный темп рейдера ушёл от среднего по лиге наполовину.
	chanceKnockPrior = 12.0
	chanceTaskPrior  = 2.0
	chanceMapPrior   = 16.0 // то же для темпа ноков и заданий на карте
	// Форма распределения ноков за рейд: чем меньше, тем чаще и пустые рейды, и рейды на десяток ноков.
	chanceKnockShape = 0.8
	chanceTaskCap    = 0.95 // задание не выполняется наверняка, даже у лучших
	// Вес скрытого рейтинга перед матчем; с каждым сыгранным рейдом он уменьшается и к концу уходит в ноль.
	chanceRatingWeight = 0.75
	// На истории модель обещала чуть больше, чем сбывалось: уверенность немного смягчается.
	chanceTemper = 0.9
	// Скрытый рейтинг - Эло без сброса между сезонами: учитывает, кого обыгрывали соперники.
	chanceRatingK     = 20.0
	chanceRatingStart = 1000.0
)

// Виды заданий рейда и сколько очков они дают в обычном раунде.
const (
	taskProtocol = iota
	taskGeneral
	taskOnMap
	taskKinds
)

var standardTaskPoints = [taskKinds]int{1, 2, 2}

// Наборы: в первом раунде обычного матча все играют на бесплатном, в остальных раундах - на своём.
const (
	kitFree = iota
	kitOwn
	kitKinds
)

// Чего ждать от рейда, пока о рейдере ничего не известно: первые матчи лиги.
var (
	leagueTaskDefault  = [taskKinds]float64{0.35, 0.25, 0.27}
	leagueKnockDefault = [kitKinds]float64{0.8, 1.5}
)

const (
	leagueTaskPrior  = 5.0  // заданий, за которые доля лиги отходит от начальной наполовину
	leagueKnockRaids = 20.0 // рейдов, после которых темп ноков лиги берётся как есть
	leagueKnockPrior = 10.0 // до этого он подтягивается к начальному с таким весом
)

// raidParams - чего ждать от рейда стороны: вероятности заданий, средний темп ноков и ручные очки 0, +1, +2.
type raidParams struct {
	Rates  [taskKinds]float64 `json:"rates"`
	Knocks float64            `json:"knocks"`
	Adj    [3]float64         `json:"adj"`
}

// raidTask - задание идущего рейда: вероятность выполнить и сколько оно даёт.
type raidTask struct {
	p      float64
	points int
}

func convolve(a, b []float64) []float64 {
	n := len(a) + len(b) - 1
	if n > chancePointsCap {
		n = chancePointsCap
	}
	out := make([]float64, n)
	for i, x := range a {
		if x == 0 {
			continue
		}
		for j, y := range b {
			if i+j >= n {
				break
			}
			out[i+j] += x * y
		}
	}
	return out
}

// knocksPMF - отрицательное биномиальное распределение ноков с заданным средним.
func knocksPMF(mean float64) []float64 {
	if mean <= 1e-9 {
		return []float64{1}
	}
	r := chanceKnockShape
	p := r / (r + mean)
	lp0 := r * math.Log(p)
	l1p := math.Log(1 - p)
	lgr, _ := math.Lgamma(r)
	out := make([]float64, chanceKnocksCap+1)
	sum := 0.0
	for k := range out {
		lk, _ := math.Lgamma(float64(k) + r)
		lf, _ := math.Lgamma(float64(k) + 1)
		out[k] = math.Exp(lk - lgr - lf + lp0 + float64(k)*l1p)
		sum += out[k]
	}
	for k := range out {
		out[k] /= sum
	}
	return out
}

// raidPMF - распределение итоговых очков рейда. knocksSoFar - уже сделанные ноки идущего рейда:
// итог не меньше их, а сверх этого рейд продолжается как обычно.
func raidPMF(tasks []raidTask, knockMean float64, knocksSoFar int, adj [3]float64) []float64 {
	pmf := []float64{1}
	for _, t := range tasks {
		b := make([]float64, t.points+1)
		b[0] = 1 - t.p
		b[t.points] += t.p
		pmf = convolve(pmf, b)
	}
	kn := knocksPMF(knockMean)
	if knocksSoFar > 0 {
		if knocksSoFar >= len(kn) {
			knocksSoFar = len(kn) - 1
		}
		tail := 0.0
		for _, x := range kn[knocksSoFar:] {
			tail += x
		}
		cut := make([]float64, len(kn))
		if tail > 1e-12 {
			for k := knocksSoFar; k < len(kn); k++ {
				cut[k] = kn[k] / tail
			}
		} else {
			cut[knocksSoFar] = 1
		}
		kn = cut
	}
	byPoints := make([]float64, 3*(len(kn)-1)+1)
	for k, x := range kn {
		byPoints[3*k] += x
	}
	pmf = convolve(pmf, byPoints)
	return convolve(pmf, adj[:])
}

// standardTasks - задания рейда, который ещё не начался: протокол, общее и на карту.
func standardTasks(p raidParams) []raidTask {
	out := make([]raidTask, taskKinds)
	for k := range out {
		out[k] = raidTask{p: p.Rates[k], points: standardTaskPoints[k]}
	}
	return out
}

// winTie - вероятность, что сторона A наберёт больше B, и что поровну; lead - сколько A уже ведёт.
func winTie(a, b []float64, lead int) (win, tie float64) {
	cum := make([]float64, len(b)+1)
	for j, y := range b {
		cum[j+1] = cum[j] + y
	}
	for i, x := range a {
		if x == 0 {
			continue
		}
		// B меньше, пока j < i + lead.
		k := i + lead
		switch {
		case k > len(b):
			win += x
		case k > 0:
			win += x * cum[k]
		}
		if k >= 0 && k < len(b) {
			tie += x * b[k]
		}
	}
	return win, tie
}

func logit(p float64) float64 {
	p = math.Min(math.Max(p, 1e-6), 1-1e-6)
	return math.Log(p / (1 - p))
}

func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

// temper делит дуэль при равном счёте, смягчает уверенность и добавляет скрытый рейтинг. duel - шанс A
// выиграть дуэль.
func temper(win, tie, duel, ratingP, played float64) models.Chance {
	a := win + tie*duel
	b := math.Max(0, 1-win-tie) + tie*(1-duel)
	if a <= 1e-12 || b <= 1e-12 {
		return models.Chance{A: a, Draw: tie, B: b}
	}
	z := chanceTemper*math.Log(a/b) + chanceRatingWeight*(1-played)*logit(ratingP)
	a = sigmoid(z)
	return models.Chance{A: a, Draw: tie, B: 1 - a}
}

// sideRaids - что ещё может набрать сторона: идущий рейд и рейды, которые не начались.
type sideRaids struct {
	base    int // очки, которые уже не изменятся, и ручные очки идущего рейда
	current *liveRaid
	left    []raidParams
}

type liveRaid struct {
	params raidParams
	tasks  []raidTask
	knocks int
}

func (s sideRaids) pmf() []float64 {
	pmf := []float64{1}
	if c := s.current; c != nil {
		pmf = convolve(pmf, raidPMF(c.tasks, c.params.Knocks, c.knocks, c.params.Adj))
	}
	for _, p := range s.left {
		pmf = convolve(pmf, raidPMF(standardTasks(p), p.Knocks, 0, p.Adj))
	}
	return pmf
}

// splitChance - шансы сторон по тому, что у них уже есть и что осталось. played - доля сыгранных рейдов.
func splitChance(a, b sideRaids, st matchSetup, played float64) models.Chance {
	win, tie := winTie(a.pmf(), b.pmf(), a.base-b.base)
	return temper(win, tie, st.Duel, st.RatingP, played)
}

// ---------- обучение по истории ----------

type leagueTally struct {
	tasks  [kitKinds][taskKinds][2]float64 // выдано и выполнено
	knocks [kitKinds][2]float64            // рейдов и ноков
	adj    [kitKinds][3]float64
}

func (l *leagueTally) params(kit int) raidParams {
	var p raidParams
	for k := 0; k < taskKinds; k++ {
		n, d := l.tasks[kit][k][0], l.tasks[kit][k][1]
		p.Rates[k] = (d + leagueTaskPrior*leagueTaskDefault[k]) / (n + leagueTaskPrior)
	}
	raids, sum := l.knocks[kit][0], l.knocks[kit][1]
	if raids >= leagueKnockRaids {
		p.Knocks = sum / raids
	} else {
		p.Knocks = (sum + leagueKnockPrior*leagueKnockDefault[kit]) / (raids + leagueKnockPrior)
	}
	// Ручные очки: обычно 0, иногда +1 или +2; начальные доли - как в первых матчах лиги.
	tot := l.adj[kit][0] + l.adj[kit][1] + l.adj[kit][2]
	p.Adj = [3]float64{(l.adj[kit][0] + 8) / (tot + 10), (l.adj[kit][1] + 1.5) / (tot + 10), (l.adj[kit][2] + 0.5) / (tot + 10)}
	return p
}

type ratio struct{ got, want float64 }

// chanceModel - что известно о лиге, картах и рейдерах к началу очередного матча.
type chanceModel struct {
	league  leagueTally
	knocks  map[string]*ratio // по рейдеру: ноки и сколько ждали по лиге
	tasks   map[string]*ratio
	mapKn   map[string]*ratio // по карте
	mapTask map[string]*ratio
	rating  map[string]float64
}

func newChanceModel() *chanceModel {
	return &chanceModel{
		knocks: map[string]*ratio{}, tasks: map[string]*ratio{},
		mapKn: map[string]*ratio{}, mapTask: map[string]*ratio{},
		rating: map[string]float64{},
	}
}

func shrunk(r *ratio, prior float64) float64 {
	if r == nil {
		return 1
	}
	return (r.got + prior) / (r.want + prior)
}

func tally(m map[string]*ratio, key string, got, want float64) {
	r := m[key]
	if r == nil {
		r = &ratio{}
		m[key] = r
	}
	r.got += got
	r.want += want
}

func (m *chanceModel) mapMult(code string) (knocks, tasks float64) {
	return shrunk(m.mapKn[code], chanceMapPrior), shrunk(m.mapTask[code], chanceMapPrior/2)
}

func (m *chanceModel) playerMult(user string) (knocks, tasks float64) {
	return shrunk(m.knocks[user], chanceKnockPrior), shrunk(m.tasks[user], chanceTaskPrior)
}

// sideParams - чего ждать от рейда игрока на этой карте и этом наборе.
func (m *chanceModel) sideParams(kit int, mapCode, user string) raidParams {
	p := m.league.params(kit)
	km, tm := m.mapMult(mapCode)
	pk, pt := m.playerMult(user)
	p.Knocks *= km * pk
	for k := range p.Rates {
		p.Rates[k] = math.Min(chanceTaskCap, math.Min(chanceTaskCap, p.Rates[k]*tm)*pt)
	}
	return p
}

func (m *chanceModel) ratingOf(user string) float64 {
	if r, ok := m.rating[user]; ok {
		return r
	}
	return chanceRatingStart
}

// ratingP - ожидаемый результат стороны A по скрытому рейтингу.
func (m *chanceModel) ratingP(a, b string) float64 {
	return 1 / (1 + math.Pow(10, (m.ratingOf(b)-m.ratingOf(a))/400))
}

// chanceTaskRec - задание сыгранного рейда.
type chanceTaskRec struct {
	Kind int  `json:"kind"`
	Done bool `json:"done"`
}

// chanceRaidRec - сыгранный рейд стороны.
type chanceRaidRec struct {
	Knocks int             `json:"knocks"`
	Adj    int             `json:"adj"`    // ручные очки сверх ноков
	Points int             `json:"points"` // очки рейда целиком
	Tasks  []chanceTaskRec `json:"tasks"`
}

type chanceRoundRec struct {
	Map    string           `json:"map"`
	Kit    int              `json:"kit"`
	Played bool             `json:"played"`
	First  int              `json:"first"` // кто рейдил первым: 0 - A, 1 - B
	Raids  [2]chanceRaidRec `json:"raids"`
}

// chanceMatchRec - сыгранный матч 1×1 в том виде, в каком его видит модель.
type chanceMatchRec struct {
	ID       string           `json:"id"`
	Users    [2]string        `json:"users"`
	Winner   int              `json:"winner"` // -1 - ничья
	Games    int              `json:"games"`  // матч из таблицы, засчитанный за несколько
	Detailed bool             `json:"detailed"`
	Voided   bool             `json:"voided,omitempty"` // исход не засчитан: проигравшего убили читеры
	Rounds   []chanceRoundRec `json:"rounds"`
}

// learn добавляет сыгранный матч: темп рейдеров и карт, средние лиги и скрытый рейтинг. У незасчитанного
// матча рейды пострадавшего ничего не говорят о его умении, а исход - о силе сторон.
func (m *chanceModel) learn(rec chanceMatchRec) {
	if rec.Detailed {
		var lp [kitKinds]raidParams
		for kit := range lp {
			lp[kit] = m.league.params(kit)
		}
		mm := map[string][2]float64{}
		for _, r := range rec.Rounds {
			km, tm := m.mapMult(r.Map)
			mm[r.Map] = [2]float64{km, tm}
		}
		for _, r := range rec.Rounds {
			if !r.Played {
				continue
			}
			p := lp[r.Kit]
			mult := mm[r.Map]
			for side, raid := range r.Raids {
				if rec.Voided && side != rec.Winner {
					continue
				}
				user := rec.Users[side]
				tally(m.knocks, user, float64(raid.Knocks), p.Knocks*mult[0])
				for _, t := range raid.Tasks {
					done := 0.0
					if t.Done {
						done = 1
					}
					tally(m.tasks, user, done, math.Min(chanceTaskCap, p.Rates[t.Kind]*mult[1]))
					tally(m.mapTask, r.Map, done, p.Rates[t.Kind])
					m.league.tasks[r.Kit][t.Kind][0]++
					m.league.tasks[r.Kit][t.Kind][1] += done
				}
				tally(m.mapKn, r.Map, float64(raid.Knocks), p.Knocks)
				m.league.knocks[r.Kit][0]++
				m.league.knocks[r.Kit][1] += float64(raid.Knocks)
				m.league.adj[r.Kit][min(2, max(0, raid.Adj))]++
			}
		}
	}
	if rec.Winner < 0 || rec.Voided || rec.Users[0] == "" || rec.Users[1] == "" {
		return
	}
	y := 1.0
	if rec.Winner == 1 {
		y = 0
	}
	for i := 0; i < max(1, rec.Games); i++ {
		d := chanceRatingK * (y - m.ratingP(rec.Users[0], rec.Users[1]))
		m.rating[rec.Users[0]] = m.ratingOf(rec.Users[0]) + d
		m.rating[rec.Users[1]] = m.ratingOf(rec.Users[1]) - d
	}
}

// matchSetup - стороны матча глазами модели перед его началом: чего ждать от каждого рейда, рейтинг и
// шанс A выиграть дуэль при равном счёте - кто чаще нокает, тот чаще и первым.
type matchSetup struct {
	Rounds  []roundSetup `json:"rounds"`
	RatingP float64      `json:"ratingP"`
	Duel    float64      `json:"duel"`
}

type roundSetup struct {
	Kit   int           `json:"kit"`
	Map   string        `json:"map"`
	Sides [2]raidParams `json:"sides"`
}

func (m *chanceModel) setup(users [2]string, rounds []chanceRoundRec) matchSetup {
	ka, _ := m.playerMult(users[0])
	kb, _ := m.playerMult(users[1])
	st := matchSetup{RatingP: m.ratingP(users[0], users[1]), Duel: ka / (ka + kb)}
	for _, r := range rounds {
		rs := roundSetup{Kit: r.Kit, Map: r.Map}
		for side, u := range users {
			rs.Sides[side] = m.sideParams(r.Kit, r.Map, u)
		}
		st.Rounds = append(st.Rounds, rs)
	}
	return st
}

// prematch - шансы до первого рейда.
func (st matchSetup) prematch() models.Chance {
	var sides [2]sideRaids
	for _, r := range st.Rounds {
		for i := range sides {
			sides[i].left = append(sides[i].left, r.Sides[i])
		}
	}
	return splitChance(sides[0], sides[1], st, 0)
}

// matchChances - шансы сыгранного матча: перед ним и сразу после каждого рейда, кроме последнего.
type matchChances struct {
	Pre    models.Chance   `json:"pre"`
	Raids  []models.Chance `json:"raids"`
	MinWin [2]float64      `json:"minWin"` // самый низкий шанс стороны на победу в эти моменты
	Setup  matchSetup      `json:"setup"`
}

// betweenRaids считает шансы сыгранного матча по итоговым очкам рейдов: ошибка ввода, исправленная
// по ходу рейда, сюда не попадает.
func betweenRaids(rec chanceMatchRec, st matchSetup) matchChances {
	out := matchChances{Setup: st}
	type slot struct{ round, side int }
	var played []int
	for i, r := range rec.Rounds {
		if r.Played {
			played = append(played, i)
		}
	}
	total := 2 * len(played)
	done := map[slot]bool{}
	state := func() models.Chance {
		var sides [2]sideRaids
		for _, i := range played {
			for side := range sides {
				if done[slot{i, side}] {
					sides[side].base += rec.Rounds[i].Raids[side].Points
				} else {
					sides[side].left = append(sides[side].left, st.Rounds[i].Sides[side])
				}
			}
		}
		share := 0.0
		if total > 0 {
			share = float64(len(done)) / float64(total)
		}
		return splitChance(sides[0], sides[1], st, share)
	}
	out.Pre = state()
	out.MinWin = [2]float64{out.Pre.A, out.Pre.B}
	for n, i := range played {
		first := rec.Rounds[i].First
		for k, side := range [2]int{first, 1 - first} {
			done[slot{i, side}] = true
			if n == len(played)-1 && k == 1 {
				break
			}
			c := state()
			out.Raids = append(out.Raids, c)
			out.MinWin[0] = math.Min(out.MinWin[0], c.A)
			out.MinWin[1] = math.Min(out.MinWin[1], c.B)
		}
	}
	return out
}

// chanceHistory проходит матчи по времени: каждый оценивается тем, что было известно до него.
func chanceHistory(recs []chanceMatchRec) (map[string]matchChances, *chanceModel) {
	m := newChanceModel()
	out := make(map[string]matchChances, len(recs))
	for _, rec := range recs {
		rounds := rec.Rounds
		if !rec.Detailed {
			// Матч из таблицы: раундов нет - как обычный матч на двух картах без подробностей.
			rounds = []chanceRoundRec{{Kit: kitFree, Played: true}, {Kit: kitOwn, Played: true}}
		}
		st := m.setup(rec.Users, rounds)
		if rec.Detailed {
			out[rec.ID] = betweenRaids(rec, st)
		} else {
			pre := st.prematch()
			out[rec.ID] = matchChances{Pre: pre, MinWin: [2]float64{pre.A, pre.B}, Setup: st}
		}
		m.learn(rec)
	}
	return out, m
}
