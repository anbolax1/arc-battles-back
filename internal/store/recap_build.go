package store

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
)

// Дни сезона, будни и часы матчей считаются по Москве: так их видят игроки и организатор.
var moscow = time.FixedZone("MSK", 3*3600)

// Пороги списков: задание с парой выдач - не статистика, а случайность.
const (
	recapTaskMinOffered = 8
	recapListLen        = 6
	recapTopLen         = 5
	recapKnockersLen    = 10
	recapTaskDoersLen   = 8
	recapRivalMeetings  = 3 // столько встреч, чтобы пара считалась соперничеством
)

type recapInput struct {
	season   models.Season
	start    int
	matches  []recapRaw // по времени
	history  []recapHist
	logins   map[string]string // id игрока -> логин
	tags     map[string][]string
	maps     []models.MapInfo
	upcoming []models.RecapUpcoming
}

type recapRaw struct {
	id     string
	at     time.Time
	show   bool
	mult   int
	games  int
	winner int // -1 - ничья
	users  [2]string
	total  [2]int
	rounds []recapRawRound
	veto   []recapRawVeto
}

type recapRawRound struct {
	number  int
	mapCode string
	played  bool
	points  [2]int
	knocks  [2]int
	entries bool
	tasks   []recapRawTask
}

type recapRawTask struct {
	side     int
	name     string
	text     string
	category string
	mapCode  string
	doneBy   int // -1 - не выполнено
}

type recapRawVeto struct{ action, mapCode string }

type recapHist struct {
	user       string
	tournament string // пусто - сверка рейтинга
	at         time.Time
	delta      int
	before     int
}

// detailed - у матча есть раунды со счётом, задания или пики-баны; у матчей из таблицы организатора только итог и карта.
func (m recapRaw) detailed() bool {
	if len(m.veto) > 0 {
		return true
	}
	for _, r := range m.rounds {
		if r.entries || len(r.tasks) > 0 {
			return true
		}
	}
	return false
}

func mskDay(t time.Time) time.Time {
	y, mo, d := t.In(moscow).Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, moscow)
}

type recapAcc struct {
	user                string
	login               string
	mmr, peak, low      int
	peakAt              time.Time
	wins, losses, games int
	seq                 []bool
	opponents           map[string]bool
	first               time.Time
	curve               [][3]int64
	played              bool
}

// curveAt - MMR по кривой игрока до момента t (мс); до первой точки - старт сезона.
func curveAt(curve [][3]int64, t int64, start int) int {
	v := start
	for _, c := range curve {
		if c[0] >= t {
			break
		}
		v = int(c[1])
	}
	return v
}

func streakOf(seq []bool, v bool) int {
	best, cur := 0, 0
	for _, x := range seq {
		if x == v {
			cur++
			best = max(best, cur)
		} else {
			cur = 0
		}
	}
	return best
}

func buildRecap(in recapInput) models.SeasonRecap {
	out := models.SeasonRecap{Season: in.season, Live: in.season.Status != "finished", Decisive: -1}
	out.Summary.StartMmr = in.start

	acc := map[string]*recapAcc{}
	get := func(u string) *recapAcc {
		a := acc[u]
		if a == nil {
			a = &recapAcc{user: u, login: in.logins[u], mmr: in.start, opponents: map[string]bool{}}
			acc[u] = a
		}
		return a
	}
	for _, m := range in.matches {
		if m.users[0] == "" || m.users[1] == "" {
			continue
		}
		for s := 0; s < 2; s++ {
			a := get(m.users[s])
			if a.first.IsZero() {
				a.first = m.at
			}
			a.opponents[m.users[1-s]] = true
			a.games++
			switch m.winner {
			case s:
				a.wins += m.games
				a.seq = append(a.seq, true)
			case 1 - s:
				a.losses += m.games
				a.seq = append(a.seq, false)
			}
		}
	}

	// Рейтинг - по истории MMR: матч или сверка меняют его пакетом, точка кривой - после пакета.
	type step struct {
		at   time.Time
		corr bool
		rows []recapHist
	}
	var steps []step
	for _, h := range in.history {
		key := h.tournament
		n := len(steps)
		if n > 0 && ((key != "" && steps[n-1].rows[0].tournament == key) || (key == "" && steps[n-1].corr && steps[n-1].at.Equal(h.at))) {
			steps[n-1].rows = append(steps[n-1].rows, h)
			continue
		}
		steps = append(steps, step{at: h.at, corr: key == "", rows: []recapHist{h}})
	}
	before := map[string]map[string]int{} // матч -> игрок -> MMR до
	delta := map[string]map[string]int{}
	for _, st := range steps {
		for _, h := range st.rows {
			a := get(h.user)
			if !st.corr {
				if before[h.tournament] == nil {
					before[h.tournament], delta[h.tournament] = map[string]int{}, map[string]int{}
				}
				before[h.tournament][h.user] = a.mmr
				delta[h.tournament][h.user] = h.delta
				a.played = true
			}
			a.mmr += h.delta
			if !a.played {
				continue
			}
			c := int64(0)
			if st.corr {
				c = 1
			}
			a.curve = append(a.curve, [3]int64{st.at.UnixMilli(), int64(a.mmr), c})
			if len(a.curve) == 1 || a.mmr > a.peak {
				a.peak, a.peakAt = a.mmr, st.at
			}
			if len(a.curve) == 1 || a.mmr < a.low {
				a.low = a.mmr
			}
		}
		if st.corr {
			out.Summary.Corrections = append(out.Summary.Corrections, models.RecapCorrection{At: st.at, Players: len(st.rows)})
		}
	}

	// Места - как в таблице лидеров сайта: MMR, затем победы.
	var ranked []*recapAcc
	for _, a := range acc {
		if a.games > 0 {
			ranked = append(ranked, a)
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.mmr != b.mmr {
			return a.mmr > b.mmr
		}
		if a.wins != b.wins {
			return a.wins > b.wins
		}
		return strings.ToLower(a.login) < strings.ToLower(b.login)
	})
	idx := map[string]int{}
	for i, a := range ranked {
		idx[a.user] = i
		tags := in.tags[a.user]
		if tags == nil {
			tags = []string{}
		}
		out.Players = append(out.Players, models.RecapPlayer{
			Login: a.login, Mmr: a.mmr, Rank: i + 1, Peak: a.peak, PeakAt: a.peakAt, Low: a.low,
			Wins: a.wins, Losses: a.losses, Matches: a.games, WinStreak: streakOf(a.seq, true), LossStreak: streakOf(a.seq, false),
			Opponents: len(a.opponents), First: a.first, Tags: tags, Curve: a.curve,
		})
		if a.games == 1 {
			out.Summary.OneMatch++
		}
	}
	out.Summary.Players = len(ranked)

	// Матчи
	mapIdx := map[string]int{}
	for _, mi := range in.maps {
		mapIdx[mi.Code] = len(out.Maps)
		out.Maps = append(out.Maps, models.RecapMap{Code: mi.Code, Name: mi.Name})
	}
	roundStat := map[int]*models.RecapRoundStat{}
	type taskKey struct{ name, category, mapCode string }
	taskStat := map[taskKey]*models.RecapTask{}
	var taskOrder []taskKey
	doneBy, offeredTo := map[int]int{}, map[int]int{}
	knocks, knockMatches := map[int]int{}, map[int]int{}
	type pairKey struct{ a, b int }
	pairs := map[pairKey][2]int{}
	var pairOrder []pairKey
	for _, m := range in.matches {
		if m.users[0] == "" || m.users[1] == "" {
			continue
		}
		mi := len(out.Matches)
		rm := models.RecapMatch{ID: m.id, At: m.at, P: [2]int{idx[m.users[0]], idx[m.users[1]]}, Winner: m.winner,
			Mult: m.mult, Games: m.games, Show: m.show, Rounds: []models.RecapRound{}}
		for s := 0; s < 2; s++ {
			b, ok := before[m.id][m.users[s]]
			if !ok {
				// ничья не меняет рейтинг и в историю не пишется: берём MMR игрока на момент матча
				b = curveAt(acc[m.users[s]].curve, m.at.UnixMilli(), in.start)
			}
			rm.Before[s], rm.Delta[s] = b, delta[m.id][m.users[s]]
		}
		det := m.detailed()
		if det {
			sc := m.total
			rm.Score = &sc
			out.Summary.Detailed++
			out.Hours[m.at.In(moscow).Hour()]++
		}
		out.Summary.Matches++
		out.Summary.Games += m.games
		if m.mult > 1 {
			out.Summary.X2++
		}
		withKnocks := false
		for _, r := range m.rounds {
			rm.Rounds = append(rm.Rounds, models.RecapRound{Map: r.mapCode, Played: r.played, Points: r.points, Knocks: r.knocks})
			if r.knocks[0]+r.knocks[1] > 0 {
				withKnocks = true
			}
			if !det || !r.played {
				continue
			}
			rs := roundStat[r.number]
			if rs == nil {
				rs = &models.RecapRoundStat{Number: r.number}
				roundStat[r.number] = rs
			}
			rs.Rounds++
			rs.Points += r.points[0] + r.points[1]
			rs.Knocks += r.knocks[0] + r.knocks[1]
			var mp *models.RecapMap
			if i, ok := mapIdx[r.mapCode]; ok {
				mp = &out.Maps[i]
				mp.Rounds++
				mp.Points += r.points[0] + r.points[1]
				mp.Knocks += r.knocks[0] + r.knocks[1]
			}
			for _, t := range r.tasks {
				done := t.doneBy >= 0
				rs.TasksOffered++
				if mp != nil {
					mp.TasksOffered++
				}
				owner := idx[m.users[t.side]]
				offeredTo[owner]++
				cnt := &out.Tasks.General
				switch {
				case t.category == "protocol":
					cnt = &out.Tasks.Protocols
				case t.mapCode != "":
					cnt = &out.Tasks.MapTasks
				}
				cnt[1]++
				out.Tasks.All[1]++
				k := taskKey{t.name, t.category, t.mapCode}
				ts := taskStat[k]
				if ts == nil {
					ts = &models.RecapTask{Name: t.name, Text: t.text, Category: t.category, Map: t.mapCode}
					taskStat[k] = ts
					taskOrder = append(taskOrder, k)
				}
				ts.Offered++
				if done {
					rs.TasksDone++
					if mp != nil {
						mp.TasksDone++
					}
					cnt[0]++
					out.Tasks.All[0]++
					ts.Done++
					doneBy[idx[m.users[t.doneBy]]]++
				}
			}
		}
		if withKnocks {
			for s := 0; s < 2; s++ {
				k := 0
				for _, r := range m.rounds {
					k += r.knocks[s]
				}
				p := idx[m.users[s]]
				knocks[p] += k
				knockMatches[p]++
				out.Summary.Knocks += k
				if out.BestKnock == nil || k > out.BestKnock.Knocks {
					out.BestKnock = &models.RecapBestKnock{Match: mi, Player: p, Knocks: k}
				}
			}
		}
		for _, v := range m.veto {
			if i, ok := mapIdx[v.mapCode]; ok {
				switch v.action {
				case "ban":
					out.Maps[i].Ban++
				case "pick":
					out.Maps[i].Pick++
				case "rest":
					out.Maps[i].Rest++
				}
			}
		}
		if len(m.veto) > 0 {
			out.Summary.Vetoed++
		}
		if det && m.winner >= 0 {
			w := m.winner
			for _, r := range m.rounds {
				if r.number == 1 && r.played && r.points[w] < r.points[1-w] {
					out.Comebacks = append(out.Comebacks, models.RecapComeback{Match: mi, Round1: [2]int{r.points[w], r.points[1-w]}})
				}
			}
		}
		if m.winner >= 0 {
			pk := pairKey{rm.P[0], rm.P[1]}
			if pk.a > pk.b {
				pk.a, pk.b = pk.b, pk.a
			}
			win, ok := pairs[pk]
			if !ok {
				pairOrder = append(pairOrder, pk)
			}
			if rm.P[m.winner] == pk.a {
				win[0]++
			} else {
				win[1]++
			}
			pairs[pk] = win
		}
		out.Matches = append(out.Matches, rm)
	}
	if n := len(out.Matches); n > 0 {
		first, last := out.Matches[0].At, out.Matches[n-1].At
		out.Summary.First, out.Summary.Last = &first, &last
	}

	// Фаворит и неожиданные победы - по Эло от MMR сторон перед матчем.
	var upsets []models.RecapUpset
	for i, m := range out.Matches {
		if m.Winner < 0 || m.Before[0] == m.Before[1] {
			continue
		}
		out.Favorites[1]++
		w, l := m.Winner, 1-m.Winner
		if m.Before[w] > m.Before[l] {
			out.Favorites[0]++
		}
		upsets = append(upsets, models.RecapUpset{Match: i, Chance: expectedScore(m.Before[w], m.Before[l])})
	}
	sort.SliceStable(upsets, func(i, j int) bool { return upsets[i].Chance < upsets[j].Chance })
	out.Upsets = append([]models.RecapUpset{}, upsets[:min(recapTopLen, len(upsets))]...)
	swings := make([]int, len(out.Matches))
	for i := range swings {
		swings[i] = i
	}
	gain := func(m models.RecapMatch) int { return max(m.Delta[0], m.Delta[1]) }
	sort.SliceStable(swings, func(i, j int) bool { return gain(out.Matches[swings[i]]) > gain(out.Matches[swings[j]]) })
	out.Swings = swings[:min(recapTopLen, len(swings))]

	for _, pk := range pairOrder {
		w := pairs[pk]
		if w[0]+w[1] >= recapRivalMeetings {
			out.Rivals = append(out.Rivals, models.RecapRival{A: pk.a, B: pk.b, WinsA: w[0], WinsB: w[1]})
		}
	}
	sort.SliceStable(out.Rivals, func(i, j int) bool {
		return out.Rivals[i].WinsA+out.Rivals[i].WinsB > out.Rivals[j].WinsA+out.Rivals[j].WinsB
	})

	for p, k := range knocks {
		if k > 0 {
			out.Knockers = append(out.Knockers, models.RecapKnocker{Player: p, Knocks: k, Matches: knockMatches[p]})
		}
	}
	sort.Slice(out.Knockers, func(i, j int) bool {
		a, b := out.Knockers[i], out.Knockers[j]
		if a.Knocks != b.Knocks {
			return a.Knocks > b.Knocks
		}
		return a.Player < b.Player
	})
	out.Knockers = out.Knockers[:min(recapKnockersLen, len(out.Knockers))]

	for _, n := range []int{1, 2, 3} {
		if rs := roundStat[n]; rs != nil {
			out.Rounds = append(out.Rounds, *rs)
		}
	}

	var tasks []models.RecapTask
	for _, k := range taskOrder {
		tasks = append(tasks, *taskStat[k])
	}
	out.Tasks.Distinct = len(tasks)
	var frequent []models.RecapTask
	for _, t := range tasks {
		if t.Offered >= recapTaskMinOffered {
			frequent = append(frequent, t)
		}
	}
	rate := func(t models.RecapTask) float64 { return float64(t.Done) / float64(t.Offered) }
	easy := append([]models.RecapTask{}, frequent...)
	sort.SliceStable(easy, func(i, j int) bool {
		if rate(easy[i]) != rate(easy[j]) {
			return rate(easy[i]) > rate(easy[j])
		}
		return easy[i].Offered > easy[j].Offered
	})
	hard := append([]models.RecapTask{}, frequent...)
	sort.SliceStable(hard, func(i, j int) bool {
		if rate(hard[i]) != rate(hard[j]) {
			return rate(hard[i]) < rate(hard[j])
		}
		return hard[i].Offered > hard[j].Offered
	})
	out.Tasks.Easy = easy[:min(recapListLen, len(easy))]
	out.Tasks.Hard = hard[:min(recapListLen, len(hard))]
	for p, d := range doneBy {
		out.Tasks.Doers = append(out.Tasks.Doers, models.RecapTaskDoer{Player: p, Done: d, Offered: offeredTo[p]})
	}
	sort.Slice(out.Tasks.Doers, func(i, j int) bool {
		a, b := out.Tasks.Doers[i], out.Tasks.Doers[j]
		if a.Done != b.Done {
			return a.Done > b.Done
		}
		return a.Player < b.Player
	})
	out.Tasks.Doers = out.Tasks.Doers[:min(recapTaskDoersLen, len(out.Tasks.Doers))]

	buildRecapDays(&out, ranked, idx)
	out.Upcoming = in.upcoming
	if out.Upcoming == nil {
		out.Upcoming = []models.RecapUpcoming{}
	}
	nilToEmpty(&out)
	return out
}

// buildRecapDays - таблица на конец каждого дня и смены лидера. При равенстве MMR выше тот, кто был выше
// накануне: иначе лидерство «менялось бы» на ничьих по алфавиту.
func buildRecapDays(out *models.SeasonRecap, ranked []*recapAcc, idx map[string]int) {
	if len(out.Matches) == 0 {
		return
	}
	first, last := mskDay(out.Matches[0].At), mskDay(out.Matches[len(out.Matches)-1].At)
	byDay := map[string]int{}
	for _, m := range out.Matches {
		byDay[m.At.In(moscow).Format("2006-01-02")]++
		out.Weekday[(int(m.At.In(moscow).Weekday())+6)%7]++
	}
	newBy := map[string]int{}
	for _, p := range out.Players {
		newBy[p.First.In(moscow).Format("2006-01-02")]++
	}
	prevRank := map[int]int{}
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		end := d.AddDate(0, 0, 1).UnixMilli()
		type row struct{ p, v int }
		var rows []row
		for _, a := range ranked {
			v, ok := 0, false
			for _, c := range a.curve {
				if c[0] >= end {
					break
				}
				v, ok = int(c[1]), true
			}
			if ok {
				rows = append(rows, row{idx[a.user], v})
			}
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].v != rows[j].v {
				return rows[i].v > rows[j].v
			}
			ri, okI := prevRank[rows[i].p]
			rj, okJ := prevRank[rows[j].p]
			if okI != okJ {
				return okI
			}
			if ri != rj {
				return ri < rj
			}
			return rows[i].p < rows[j].p
		})
		key := d.Format("2006-01-02")
		day := models.RecapDay{Date: key, Matches: byDay[key], New: newBy[key], Order: []int{}}
		prevRank = map[int]int{}
		for i, r := range rows {
			day.Order = append(day.Order, r.p)
			prevRank[r.p] = i
		}
		k := len(out.Days)
		out.Days = append(out.Days, day)
		if len(day.Order) == 0 {
			continue
		}
		leader := day.Order[0]
		if n := len(out.Leaders); n > 0 && out.Leaders[n-1].Player == leader {
			out.Leaders[n-1].To = k
		} else {
			out.Leaders = append(out.Leaders, models.RecapLeader{Player: leader, From: k, To: k})
		}
	}
	out.Summary.GameDays = len(byDay)

	// Решающий матч - победа итогового лидера в день, когда он стал первым в последний раз.
	if n := len(out.Leaders); n > 1 {
		lead := out.Leaders[n-1]
		key := out.Days[lead.From].Date
		bestGain := math.MinInt
		for i, m := range out.Matches {
			if m.At.In(moscow).Format("2006-01-02") != key || m.Winner < 0 || m.P[m.Winner] != lead.Player {
				continue
			}
			if g := m.Delta[m.Winner]; g > bestGain {
				bestGain, out.Decisive = g, i
			}
		}
	}
}

// nilToEmpty - пустые списки уходят в JSON как [], а не null: странице не нужно проверять каждый.
func nilToEmpty(out *models.SeasonRecap) {
	if out.Players == nil {
		out.Players = []models.RecapPlayer{}
	}
	if out.Matches == nil {
		out.Matches = []models.RecapMatch{}
	}
	if out.Days == nil {
		out.Days = []models.RecapDay{}
	}
	if out.Leaders == nil {
		out.Leaders = []models.RecapLeader{}
	}
	if out.Rounds == nil {
		out.Rounds = []models.RecapRoundStat{}
	}
	if out.Knockers == nil {
		out.Knockers = []models.RecapKnocker{}
	}
	if out.Comebacks == nil {
		out.Comebacks = []models.RecapComeback{}
	}
	if out.Upsets == nil {
		out.Upsets = []models.RecapUpset{}
	}
	if out.Rivals == nil {
		out.Rivals = []models.RecapRival{}
	}
	if out.Summary.Corrections == nil {
		out.Summary.Corrections = []models.RecapCorrection{}
	}
	if out.Tasks.Easy == nil {
		out.Tasks.Easy = []models.RecapTask{}
	}
	if out.Tasks.Hard == nil {
		out.Tasks.Hard = []models.RecapTask{}
	}
	if out.Tasks.Doers == nil {
		out.Tasks.Doers = []models.RecapTaskDoer{}
	}
	for i := range out.Players {
		if out.Players[i].Curve == nil {
			out.Players[i].Curve = [][3]int64{}
		}
	}
}
