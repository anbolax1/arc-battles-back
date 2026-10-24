package store

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
)

// trailRaid - рейд стороны в раунде по ходу повтора журнала.
type trailRaid struct {
	points, knocks int
	ownDone        int // очки своих выполненных заданий
	state          int // raidAhead | raidOn | raidDone
	since          time.Time
	events         int
}

const (
	raidAhead = iota
	raidOn
	raidDone
)

type trailTask struct {
	round, side int
	kind        int
	points      int
	done        bool
}

type trailEvent struct {
	at    time.Time
	start bool // начало рейда, иначе действие журнала
	round int
	side  int // -1 - без стороны
	kind  string
	text  string
	delta int
	asg   string
	prev  *string
	tgt   *string
}

// MatchChanceTrail - как менялся шанс по ходу матча: перед ним, после каждого действия в пульте и когда
// закрывались рейды. Для идущего матча последняя точка - текущее состояние, как в оверлее.
func (s *Store) MatchChanceTrail(ctx context.Context, st models.MatchState, focus string) (*models.ChanceTrail, error) {
	t := st.Tournament
	parts, users, ok := chanceSides(t)
	if !ok {
		return nil, nil
	}
	snap, err := s.chances(ctx, false)
	if err != nil {
		return nil, err
	}
	setup := chanceSetupFor(snap.model, t, users)
	if h, ok := snap.matches[t.ID]; ok && t.Status == "finished" && len(h.Setup.Rounds) == len(setup.Rounds) {
		setup = h.Setup
	}
	sideOf := map[string]int{parts[0].ID: 0, parts[1].ID: 1}
	names := [2]string{parts[0].Name, parts[1].Name}

	tasks := map[string]*trailTask{}
	for _, task := range st.Tasks {
		if i, ok := sideOf[task.ParticipantID]; ok {
			tasks[task.ID] = &trailTask{round: task.RoundNumber, side: i, kind: chanceTaskKind(task.Category, task.MapCode != ""), points: task.Points}
		}
	}

	events, err := s.trailEvents(ctx, t.ID, sideOf)
	if err != nil {
		return nil, err
	}
	withStarts := false
	for _, e := range events {
		if e.start {
			withStarts = true
			break
		}
	}

	rounds := len(setup.Rounds)
	raids := make([][2]trailRaid, rounds+1)
	// У старых матчей начала рейдов нет: рейд считается закрытым, когда соперник начал свой и больше
	// в этом рейде ничего не было.
	lastRaidEvent := map[[2]int]time.Time{}
	if !withStarts {
		for _, e := range events {
			if e.side >= 0 && (e.kind == "knock" || e.kind == "task") && e.round >= 1 && e.round <= rounds {
				lastRaidEvent[[2]int{e.round, e.side}] = e.at
			}
		}
	}

	score := func() [2]int {
		var out [2]int
		for r := 1; r <= rounds; r++ {
			out[0] += raids[r][0].points
			out[1] += raids[r][1].points
		}
		return out
	}
	chance := func() models.Chance {
		var sides [2]sideRaids
		done := 0
		for r := 1; r <= rounds; r++ {
			rs := setup.Rounds[r-1]
			for side := 0; side < 2; side++ {
				x := raids[r][side]
				switch x.state {
				case raidDone:
					sides[side].base += x.points
					done++
				case raidOn:
					params := rs.Sides[side]
					live := &liveRaid{params: params, knocks: max(0, x.knocks)}
					for _, task := range tasks {
						if task.round != r || task.side != side {
							continue
						}
						p := params.Rates[task.kind]
						if task.done {
							p = 1
						}
						live.tasks = append(live.tasks, raidTask{p: p, points: task.points})
					}
					if len(live.tasks) == 0 {
						live.tasks = standardTasks(params)
					}
					sides[side].current = live
					sides[side].base += x.points - x.ownDone - KnockPoints*live.knocks
				default:
					sides[side].left = append(sides[side].left, rs.Sides[side])
				}
			}
		}
		played := 0.0
		if rounds > 0 {
			played = float64(done) / float64(2*rounds)
		}
		return splitChance(sides[0], sides[1], setup, played)
	}

	out := &models.ChanceTrail{Points: []models.ChancePoint{}, Live: t.Status != "finished"}
	add := func(at time.Time, kind string, round, side int, text string, delta int) {
		out.Points = append(out.Points, models.ChancePoint{
			At: at, Kind: kind, Round: round, Side: side, Text: text, Delta: delta, Score: score(), Chance: chance(),
		})
	}
	var startAt time.Time
	if t.StartsAt != nil {
		startAt = *t.StartsAt
	}
	add(startAt, "start", 1, -1, "до матча", 0)

	closeRaid := func(r, side int) bool {
		if r < 1 || r > rounds || raids[r][side].state == raidDone {
			return false
		}
		raids[r][side].state = raidDone
		return true
	}
	// closeBefore закрывает рейды прошлых раундов.
	closeBefore := func(round int, at time.Time) {
		for r := 1; r < round && r <= rounds; r++ {
			for side := 0; side < 2; side++ {
				if closeRaid(r, side) {
					add(at, "raid", r, side, "рейд "+names[side]+" окончен", 0)
				}
			}
		}
	}

	for _, e := range events {
		if e.round < 1 || e.round > rounds {
			continue
		}
		if e.start {
			closeBefore(e.round, e.at)
			other := 1 - e.side
			if o := &raids[e.round][other]; o.state == raidOn {
				if e.at.Sub(o.since) < raidMisclick && o.events == 0 {
					o.state = raidAhead
				} else if closeRaid(e.round, other) {
					add(e.at, "raid", e.round, other, "рейд "+names[other]+" окончен", 0)
				}
			}
			raids[e.round][e.side].state = raidOn
			raids[e.round][e.side].since = e.at
			continue
		}
		if !withStarts && e.side >= 0 && (e.kind == "knock" || e.kind == "task") {
			closeBefore(e.round, e.at)
			// По порядку пульта первой рейдит A: если B начал, а у A в раунде ничего не было, рейд A закрыт.
			if e.side == 1 && raids[e.round][0].state == raidAhead && closeRaid(e.round, 0) {
				add(e.at, "raid", e.round, 0, "рейд "+names[0]+" окончен", 0)
			}
			other := 1 - e.side
			if raids[e.round][other].state == raidOn && !lastRaidEvent[[2]int{e.round, other}].After(e.at) && closeRaid(e.round, other) {
				add(e.at, "raid", e.round, other, "рейд "+names[other]+" окончен", 0)
			}
		}
		if e.side >= 0 {
			x := &raids[e.round][e.side]
			if x.state == raidAhead {
				x.state, x.since = raidOn, e.at
			}
			x.events++
		}
		switch e.kind {
		case "knock":
			x := &raids[e.round][e.side]
			x.points += e.delta
			if e.delta > 0 {
				x.knocks++
			} else if e.delta < 0 {
				x.knocks--
			}
		case "points", "legendary":
			if e.side >= 0 {
				raids[e.round][e.side].points += e.delta
			}
		case "task":
			applyTaskLog(raids, tasks, sideOf, e)
		default:
			continue
		}
		add(e.at, e.kind, e.round, e.side, e.text, e.delta)
	}

	if t.Status == "finished" {
		var end time.Time
		if n := len(events); n > 0 {
			end = events[n-1].at
		}
		for r := 1; r <= rounds; r++ {
			for side := 0; side < 2; side++ {
				raids[r][side].state = raidDone
			}
		}
		totals := [2]int{}
		for _, sc := range st.Scores {
			if i, ok := sideOf[sc.ParticipantID]; ok {
				totals[i] += sc.Points
			}
		}
		out.Points = append(out.Points, models.ChancePoint{At: end, Kind: "end", Round: rounds, Side: -1, Text: "итог", Score: totals, Chance: finalChance(totals[0], totals[1])})
		return out, nil
	}
	// Идущий матч: в конце - то, что сейчас показывает оверлей, если повтор журнала с ним разошёлся.
	if mc, err := s.MatchChance(ctx, st, focus); err == nil && mc != nil {
		last := out.Points[len(out.Points)-1].Chance
		if math.Abs(last.A-mc.Now.A) > 0.005 || math.Abs(last.B-mc.Now.B) > 0.005 {
			totals := [2]int{}
			for _, sc := range st.Scores {
				if i, ok := sideOf[sc.ParticipantID]; ok {
					totals[i] += sc.Points
				}
			}
			out.Points = append(out.Points, models.ChancePoint{At: time.Now(), Kind: "now", Round: max(st.CurrentRound, 1), Side: -1, Text: "сейчас", Score: totals, Chance: mc.Now})
		}
	}
	return out, nil
}

// applyTaskLog применяет зачёт или снятие задания: кто выполнял до и кто теперь.
func applyTaskLog(raids [][2]trailRaid, tasks map[string]*trailTask, sideOf map[string]int, e trailEvent) {
	task := tasks[e.asg]
	if task == nil || task.round < 1 || task.round >= len(raids) || (e.prev == nil && e.tgt == nil) {
		if e.side >= 0 {
			raids[e.round][e.side].points += e.delta
		}
		return
	}
	credit := func(pid *string, sign int) {
		if pid == nil {
			return
		}
		side, ok := sideOf[*pid]
		if !ok {
			return
		}
		x := &raids[task.round][side]
		if side == task.side {
			x.points += sign * task.points
			x.ownDone += sign * task.points
			task.done = sign > 0
		} else {
			x.points += sign * ContractCrossPoints
		}
	}
	credit(e.prev, -1)
	credit(e.tgt, 1)
}

// trailEvents - начала рейдов и действия журнала матча по времени.
func (s *Store) trailEvents(ctx context.Context, tournamentID string, sideOf map[string]int) ([]trailEvent, error) {
	var out []trailEvent
	starts, err := s.raidStarts(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	for _, x := range starts {
		if i, ok := sideOf[x.pid]; ok {
			out = append(out, trailEvent{at: x.at, start: true, round: x.round, side: i})
		}
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT round_number, COALESCE(participant_id, ''), kind, text, delta, undo, created_at
		FROM match_log WHERE tournament_id = $1 ORDER BY created_at, id`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e trailEvent
		var pid string
		var undo []byte
		if err := rows.Scan(&e.round, &pid, &e.kind, &e.text, &e.delta, &undo, &e.at); err != nil {
			return nil, err
		}
		e.side = -1
		if i, ok := sideOf[pid]; ok {
			e.side = i
		}
		if e.kind == "task" {
			var u struct {
				AssignmentID string  `json:"assignmentId"`
				Prev         *string `json:"prev"`
				Target       *string `json:"target"`
			}
			_ = json.Unmarshal(undo, &u)
			e.asg, e.prev, e.tgt = u.AssignmentID, u.Prev, u.Target
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Начало рейда в ту же секунду, что и действие, идёт раньше него.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].at.Equal(out[j].at) {
			return out[i].at.Before(out[j].at)
		}
		return out[i].start && !out[j].start
	})
	return out, nil
}
