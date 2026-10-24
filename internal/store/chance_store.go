package store

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
)

// chanceTTL - сколько живёт посчитанная история без проверки; пересчёт нашивок обновляет её сразу.
const chanceTTL = 5 * time.Minute

type chanceCache struct {
	mu      sync.Mutex
	at      time.Time
	version string
	snap    *chanceSnapshot
}

type chanceSnapshot struct {
	matches map[string]matchChances // сыгранные матчи 1×1 лиги
	model   *chanceModel            // всё, что известно сейчас: для идущего и будущих матчей
}

// chances - история шансов; fresh - сверить с базой сейчас, а не ждать chanceTTL. Пересчёт - только если
// сыгранные матчи изменились.
func (s *Store) chances(ctx context.Context, fresh bool) (*chanceSnapshot, error) {
	c := s.chanceCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !fresh && c.snap != nil && time.Since(c.at) < chanceTTL {
		return c.snap, nil
	}
	ver, err := s.chanceVersion(ctx)
	if err != nil {
		return nil, err
	}
	if c.snap != nil && ver == c.version {
		c.at = time.Now()
		return c.snap, nil
	}
	recs, err := s.loadChanceHistory(ctx)
	if err != nil {
		return nil, err
	}
	matches, model := chanceHistory(recs)
	c.snap = &chanceSnapshot{matches: matches, model: model}
	c.at, c.version = time.Now(), ver
	return c.snap, nil
}

// chanceVersion - отпечаток сыгранных матчей: меняется, когда матч завершили, исправили или удалили.
func (s *Store) chanceVersion(ctx context.Context) (string, error) {
	var v string
	err := s.Pool.QueryRow(ctx, `
		WITH t AS (SELECT id, updated_at, voided FROM tournaments WHERE mode = '1x1' AND status = 'finished' AND season_id IS NOT NULL)
		SELECT concat_ws('|',
			(SELECT count(*) || ':' || count(*) FILTER (WHERE voided) || ':' || COALESCE(max(updated_at)::text, '') FROM t),
			(SELECT count(*) || ':' || COALESCE(sum(re.points), 0) || ':' || COALESCE(sum(re.knocks), 0)
			 FROM round_entries re JOIN rounds r ON r.id = re.round_id JOIN t ON t.id = r.tournament_id),
			(SELECT count(*) || ':' || count(b.completed_by) FROM round_bonus_tasks b JOIN rounds r ON r.id = b.round_id JOIN t ON t.id = r.tournament_id),
			(SELECT count(*) FROM legendary_contract_completions l JOIN rounds r ON r.id = l.round_id JOIN t ON t.id = r.tournament_id))`).Scan(&v)
	return v, err
}

// chanceKit - на каком наборе играется раунд: первый раунд обычного матча - на бесплатном.
func chanceKit(format string, number int) int {
	if number == 1 && format == "match" {
		return kitFree
	}
	return kitOwn
}

func chanceTaskKind(category string, onMap bool) int {
	switch {
	case category == "protocol":
		return taskProtocol
	case onMap:
		return taskOnMap
	}
	return taskGeneral
}

// loadChanceHistory - сыгранные матчи 1×1 сезонов лиги по времени: рейды сторон с очками, ноками и заданиями.
func (s *Store) loadChanceHistory(ctx context.Context) ([]chanceMatchRec, error) {
	const scope = `t.mode = '1x1' AND t.status = 'finished' AND t.season_id IS NOT NULL`
	rows, err := s.Pool.Query(ctx, `
		SELECT t.id, t.format, t.games, COALESCE(t.winner_participant_id, ''), t.voided
		FROM tournaments t WHERE `+scope+`
		ORDER BY COALESCE(t.starts_at, t.created_at), t.created_at, t.id`)
	if err != nil {
		return nil, err
	}
	var recs []chanceMatchRec
	idx := map[string]int{}
	format := map[string]string{}
	winners := map[string]string{}
	for rows.Next() {
		var id, f, winner string
		var games int
		var voided bool
		if err := rows.Scan(&id, &f, &games, &winner, &voided); err != nil {
			rows.Close()
			return nil, err
		}
		idx[id] = len(recs)
		format[id], winners[id] = f, winner
		recs = append(recs, chanceMatchRec{ID: id, Games: games, Winner: -1, Voided: voided})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type sideRef struct{ match, side int }
	sides := map[string]sideRef{}
	count := map[string]int{}
	rows, err = s.Pool.Query(ctx, `
		SELECT p.tournament_id, p.id, COALESCE(p.user_id, '')
		FROM participants p JOIN tournaments t ON t.id = p.tournament_id
		WHERE `+scope+` AND p.kind = 'player'
		ORDER BY p.tournament_id, p.seed, p.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tid, pid, user string
		if err := rows.Scan(&tid, &pid, &user); err != nil {
			rows.Close()
			return nil, err
		}
		mi, ok := idx[tid]
		if !ok || count[tid] >= 2 {
			continue
		}
		side := count[tid]
		count[tid]++
		if user == "" {
			user = pid // без аккаунта рейдер известен только в этом матче
		}
		recs[mi].Users[side] = user
		sides[pid] = sideRef{mi, side}
		if winners[tid] == pid {
			recs[mi].Winner = side
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type roundRef struct{ match, round int }
	roundOf := map[string]roundRef{}
	numbers := map[roundRef]int{}
	rows, err = s.Pool.Query(ctx, `
		SELECT r.tournament_id, r.id, r.number, COALESCE(r.map_code, ''), r.status <> 'pending'
		FROM rounds r JOIN tournaments t ON t.id = r.tournament_id
		WHERE `+scope+`
		ORDER BY r.tournament_id, r.number`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tid, rid, code string
		var number int
		var played bool
		if err := rows.Scan(&tid, &rid, &number, &code, &played); err != nil {
			rows.Close()
			return nil, err
		}
		mi, ok := idx[tid]
		if !ok {
			continue
		}
		ref := roundRef{mi, len(recs[mi].Rounds)}
		roundOf[rid] = ref
		numbers[ref] = number
		recs[mi].Rounds = append(recs[mi].Rounds, chanceRoundRec{Map: code, Kit: chanceKit(format[tid], number), Played: played})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Ручные очки вместе с ноками.
	rows, err = s.Pool.Query(ctx, `
		SELECT re.round_id, re.participant_id, re.points, re.knocks
		FROM round_entries re JOIN rounds r ON r.id = re.round_id JOIN tournaments t ON t.id = r.tournament_id
		WHERE `+scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rid, pid string
		var points, knocks int
		if err := rows.Scan(&rid, &pid, &points, &knocks); err != nil {
			rows.Close()
			return nil, err
		}
		ref, ok := roundOf[rid]
		sd, okSide := sides[pid]
		if !ok || !okSide || sd.match != ref.match {
			continue
		}
		raid := &recs[ref.match].Rounds[ref.round].Raids[sd.side]
		raid.Points += points
		raid.Knocks += knocks
		raid.Adj += points - KnockPoints*knocks
		recs[ref.match].Detailed = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Задания: своё выполненное - очки из каталога, выполненное соперником - балл ему.
	rows, err = s.Pool.Query(ctx, `
		SELECT b.round_id, b.participant_id, COALESCE(b.completed_by, ''), ct.category, cardinality(ct.map_codes) > 0, ct.points
		FROM round_bonus_tasks b JOIN rounds r ON r.id = b.round_id JOIN tournaments t ON t.id = r.tournament_id
		JOIN catalog_tasks ct ON ct.id = b.task_id
		WHERE `+scope+`
		ORDER BY b.created_at, b.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rid, owner, doneBy, category string
		var onMap bool
		var points int
		if err := rows.Scan(&rid, &owner, &doneBy, &category, &onMap, &points); err != nil {
			rows.Close()
			return nil, err
		}
		ref, ok := roundOf[rid]
		so, okOwner := sides[owner]
		if !ok || !okOwner || so.match != ref.match {
			continue
		}
		rd := &recs[ref.match].Rounds[ref.round]
		done := doneBy == owner
		rd.Raids[so.side].Tasks = append(rd.Raids[so.side].Tasks, chanceTaskRec{Kind: chanceTaskKind(category, onMap), Done: done})
		if done {
			rd.Raids[so.side].Points += points
		} else if sd, ok := sides[doneBy]; ok && sd.match == ref.match {
			rd.Raids[sd.side].Points += ContractCrossPoints
		}
		recs[ref.match].Detailed = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.Pool.Query(ctx, `
		SELECT l.round_id, l.participant_id, lc.points
		FROM legendary_contract_completions l JOIN legendary_contracts lc ON lc.id = l.legendary_contract_id
		JOIN rounds r ON r.id = l.round_id JOIN tournaments t ON t.id = r.tournament_id
		WHERE `+scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rid, pid string
		var points int
		if err := rows.Scan(&rid, &pid, &points); err != nil {
			rows.Close()
			return nil, err
		}
		if ref, ok := roundOf[rid]; ok {
			if sd, ok := sides[pid]; ok && sd.match == ref.match {
				recs[ref.match].Rounds[ref.round].Raids[sd.side].Points += points
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Матч с пиками-банами тоже подробный, даже если в раундах пусто.
	rows, err = s.Pool.Query(ctx, `
		SELECT DISTINCT v.tournament_id FROM match_veto v JOIN tournaments t ON t.id = v.tournament_id WHERE `+scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tid string
		if err := rows.Scan(&tid); err != nil {
			rows.Close()
			return nil, err
		}
		if mi, ok := idx[tid]; ok {
			recs[mi].Detailed = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Кто рейдил первым в раунде: по отметкам начала рейдов, а у старых матчей - по первому действию в журнале.
	type markKey struct{ match, number int }
	marks := map[markKey][]raidMark{}
	withStarts := map[int]bool{}
	for src, q := range []string{
		`SELECT x.tournament_id, x.round_number, x.participant_id, x.started_at
		 FROM raid_starts x JOIN tournaments t ON t.id = x.tournament_id WHERE ` + scope + `
		 ORDER BY x.started_at, x.id`,
		`SELECT x.tournament_id, x.round_number, x.participant_id, x.created_at
		 FROM match_log x JOIN tournaments t ON t.id = x.tournament_id
		 WHERE ` + scope + ` AND x.participant_id IS NOT NULL
		 ORDER BY x.created_at, x.id`,
	} {
		rows, err = s.Pool.Query(ctx, q)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var tid, pid string
			var number int
			var at time.Time
			if err := rows.Scan(&tid, &number, &pid, &at); err != nil {
				rows.Close()
				return nil, err
			}
			if sd, ok := sides[pid]; ok && sd.match == idx[tid] {
				k := markKey{sd.match, number}
				marks[k] = append(marks[k], raidMark{at: at, side: sd.side, start: src == 0})
				if src == 0 {
					withStarts[sd.match] = true
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for ref, number := range numbers {
		ms := marks[markKey{ref.match, number}]
		sortRaidMarks(ms)
		pick := firstActor
		if withStarts[ref.match] {
			pick = firstRaider
		}
		if first, ok := pick(ms); ok {
			recs[ref.match].Rounds[ref.round].First = first
		}
	}
	return recs, nil
}

// raidMark - отметка раунда: начало рейда стороны или её действие в журнале.
type raidMark struct {
	at    time.Time
	side  int
	start bool
}

// sortRaidMarks - по времени, как в повторе журнала: начало рейда в ту же секунду, что и действие, раньше него.
func sortRaidMarks(ms []raidMark) {
	sort.SliceStable(ms, func(i, j int) bool {
		if !ms[i].at.Equal(ms[j].at) {
			return ms[i].at.Before(ms[j].at)
		}
		return ms[i].start && !ms[j].start
	})
}

// firstRaider - чей рейд в раунде первым закрыл соперник, начав свой, по правилам повтора журнала для графика.
// Пульт сам отмечает начало рейда стороне A в начале раунда, поэтому первая отметка - ещё не первый рейд.
func firstRaider(ms []raidMark) (int, bool) {
	var state, events [2]int
	var since [2]time.Time
	first := -1 // кто первым ушёл в рейд; случайное переключение не в счёт
	for _, x := range ms {
		if x.start {
			other := 1 - x.side
			if state[other] == raidOn {
				if x.at.Sub(since[other]) >= raidMisclick || events[other] > 0 {
					return other, true
				}
				state[other] = raidAhead
				if first == other {
					first = -1
				}
			}
			state[x.side], since[x.side] = raidOn, x.at
		} else {
			if state[x.side] == raidAhead {
				state[x.side], since[x.side] = raidOn, x.at
			}
			events[x.side]++
		}
		if first < 0 && state[x.side] == raidOn {
			first = x.side
		}
	}
	return first, first >= 0
}

// firstActor - у матча без отметок начала рейдов первым считается B, только если оба действовали и B раньше.
func firstActor(ms []raidMark) (int, bool) {
	var seen [2]bool
	for _, x := range ms {
		if x.start || seen[x.side] {
			continue
		}
		seen[x.side] = true
		if seen[1-x.side] {
			return 1 - x.side, true
		}
	}
	return 0, false
}

// chanceSetupFor - стороны матча глазами модели: чего ждать от каждого раунда этого матча.
func chanceSetupFor(m *chanceModel, t models.Tournament, users [2]string) matchSetup {
	rounds := append([]models.Round(nil), t.Rounds...)
	sort.Slice(rounds, func(i, j int) bool { return rounds[i].Number < rounds[j].Number })
	recs := make([]chanceRoundRec, 0, len(rounds))
	for _, r := range rounds {
		recs = append(recs, chanceRoundRec{Map: r.MapCode, Kit: chanceKit(t.Format, r.Number), Played: true})
	}
	if len(recs) == 0 {
		for n := 1; n <= max(t.TotalRounds, 1); n++ {
			recs = append(recs, chanceRoundRec{Kit: chanceKit(t.Format, n), Played: true})
		}
	}
	return m.setup(users, recs)
}

// chanceSides - стороны 1×1 по порядку A, B и их аккаунты; ok=false - шанс для матча не считается.
func chanceSides(t models.Tournament) (parts [2]models.Participant, users [2]string, ok bool) {
	if t.Mode != "1x1" || len(t.Participants) != 2 {
		return parts, users, false
	}
	ps := append([]models.Participant(nil), t.Participants...)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Seed < ps[j].Seed })
	for i, p := range ps {
		parts[i] = p
		users[i] = p.ID
		if p.UserID != nil && *p.UserID != "" {
			users[i] = *p.UserID
		}
	}
	return parts, users, true
}

// finalChance - итог сыгранного матча в виде шансов; равный счёт без дуэли - пополам.
func finalChance(a, b int) models.Chance {
	switch {
	case a > b:
		return models.Chance{A: 1}
	case b > a:
		return models.Chance{B: 1}
	}
	return models.Chance{A: 0.5, Draw: 1, B: 0.5}
}

// MatchChance - шансы матча перед ним и сейчас. focus - сторона в рейде; nil - для матча шанс не считается (2×2).
func (s *Store) MatchChance(ctx context.Context, st models.MatchState, focus string) (*models.MatchChance, error) {
	t := st.Tournament
	parts, users, ok := chanceSides(t)
	if !ok {
		return nil, nil
	}
	snap, err := s.chances(ctx, false)
	if err != nil {
		return nil, err
	}
	totals := [2]int{}
	for _, sc := range st.Scores {
		for i, p := range parts {
			if sc.ParticipantID == p.ID {
				totals[i] += sc.Points
			}
		}
	}
	if t.Status == "finished" {
		out := &models.MatchChance{Now: finalChance(totals[0], totals[1])}
		if h, ok := snap.matches[t.ID]; ok {
			out.Pre = h.Pre
		} else {
			out.Pre = chanceSetupFor(snap.model, t, users).prematch()
		}
		return out, nil
	}
	setup := chanceSetupFor(snap.model, t, users)
	pre := setup.prematch()
	out := &models.MatchChance{Pre: pre, Now: pre}
	if st.Stage != "round" {
		return out, nil
	}
	starts, err := s.raidStarts(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	out.Now = liveChance(st, parts, setup, focus, starts)
	return out, nil
}

// raidStart - начало рейда стороны в раунде.
type raidStart struct {
	round int
	pid   string
	at    time.Time
}

func (s *Store) raidStarts(ctx context.Context, tournamentID string) ([]raidStart, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT round_number, participant_id, started_at FROM raid_starts WHERE tournament_id = $1 ORDER BY started_at, id`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []raidStart
	for rows.Next() {
		var x raidStart
		if err := rows.Scan(&x.round, &x.pid, &x.at); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// RecordRaidStart отмечает начало рейда стороны: так видно, когда закрылся рейд соперника.
func (s *Store) RecordRaidStart(ctx context.Context, tournamentID string, round int, participantID string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO raid_starts (tournament_id, round_number, participant_id) VALUES ($1, $2, $3)`, tournamentID, round, participantID)
	return err
}

// DuelLogKind - отметка в журнале матча: счёт после всех рейдов равный, идёт дуэль до первого нока.
const DuelLogKind = "duel"

// MatchDuel - идёт ли дуэль: рейды сыграны, исход решит первый нок.
func MatchDuel(st models.MatchState) bool {
	for _, l := range st.Log {
		if l.Kind == DuelLogKind {
			return true
		}
	}
	return false
}

// raidTally - что сторона набрала в раунде: очки целиком, ноки и задания.
type raidTally struct {
	points, knocks int
	tasks          []models.RoundBonusTask // свои задания раунда
	active         bool                    // в рейде уже что-то было
}

// liveChance - шансы идущего раунда: рейды прошлых раундов закрыты, сторона в рейде ещё может добрать,
// а рейд соперника в этом раунде закрыт или впереди - по отметкам начала рейдов.
func liveChance(st models.MatchState, parts [2]models.Participant, setup matchSetup, focus string, starts []raidStart) models.Chance {
	sideOf := map[string]int{parts[0].ID: 0, parts[1].ID: 1}
	tallies := map[[2]int]*raidTally{}
	get := func(round, side int) *raidTally {
		k := [2]int{round, side}
		if tallies[k] == nil {
			tallies[k] = &raidTally{}
		}
		return tallies[k]
	}
	for _, sc := range st.Scores {
		if i, ok := sideOf[sc.ParticipantID]; ok {
			get(sc.RoundNumber, i).points += sc.Points
			if sc.Points != 0 {
				get(sc.RoundNumber, i).active = true
			}
		}
	}
	for _, k := range st.Knocks {
		if i, ok := sideOf[k.ParticipantID]; ok {
			get(k.RoundNumber, i).knocks += k.Knocks
		}
	}
	for _, task := range st.Tasks {
		if i, ok := sideOf[task.ParticipantID]; ok {
			x := get(task.RoundNumber, i)
			x.tasks = append(x.tasks, task)
			if task.CompletedBy != nil {
				x.active = true
			}
		}
	}

	cur := st.CurrentRound
	// Дуэль: рейды сыграны все, шанс - кто выиграет её.
	if MatchDuel(st) {
		cur = len(setup.Rounds) + 1
	}
	focusSide := 0
	if i, ok := sideOf[focus]; ok {
		focusSide = i
	}
	var roundStarts []raidStart
	for _, x := range starts {
		if x.round == cur {
			roundStarts = append(roundStarts, x)
		}
	}
	active := [2]bool{get(cur, 0).active, get(cur, 1).active}
	states, ok := raidStates(roundStarts, sideOf, active)
	if !ok {
		// Отметок нет: по порядку пульта первой рейдит сторона A.
		other := 1 - focusSide
		states[other] = raidAhead
		switch {
		case focusSide == 1:
			states[other] = raidDone
		case active[other]:
			states[other] = raidOn
		}
	}
	states[focusSide] = raidOn

	var sides [2]sideRaids
	done := 0
	total := 2 * len(setup.Rounds)
	for ri, rs := range setup.Rounds {
		number := ri + 1
		for side := 0; side < 2; side++ {
			x := get(number, side)
			switch {
			case number < cur, number == cur && states[side] == raidDone:
				sides[side].base += x.points
				done++
			case number == cur && states[side] == raidOn:
				params := rs.Sides[side]
				raid := &liveRaid{params: params, knocks: max(0, x.knocks)}
				own := 0
				for _, task := range x.tasks {
					kind := chanceTaskKind(task.Category, task.MapCode != "")
					p := params.Rates[kind]
					if task.CompletedBy != nil && *task.CompletedBy == task.ParticipantID {
						p = 1
						own += task.Points
					}
					raid.tasks = append(raid.tasks, raidTask{p: p, points: task.Points})
				}
				if len(x.tasks) == 0 {
					raid.tasks = standardTasks(params)
				}
				sides[side].current = raid
				sides[side].base += x.points - own - KnockPoints*raid.knocks
			default:
				sides[side].left = append(sides[side].left, rs.Sides[side])
			}
		}
	}
	played := 0.0
	if total > 0 {
		played = float64(done) / float64(total)
	}
	return splitChance(sides[0], sides[1], setup, played)
}

// raidMisclick - рейд короче этого и без очков считается случайным переключением стороны в пульте.
const raidMisclick = 2 * time.Minute

// raidStates - рейды сторон в раунде по отметкам начала: начавший после соперника закрывает его рейд.
// ok=false - отметок в раунде нет.
func raidStates(starts []raidStart, sideOf map[string]int, active [2]bool) (states [2]int, ok bool) {
	var since [2]time.Time
	for _, x := range starts {
		side, known := sideOf[x.pid]
		if !known {
			continue
		}
		ok = true
		other := 1 - side
		if states[other] == raidOn {
			if x.at.Sub(since[other]) < raidMisclick && !active[other] {
				states[other] = raidAhead
			} else {
				states[other] = raidDone
			}
		}
		states[side] = raidOn
		since[side] = x.at
	}
	return states, ok
}

// PrematchChance - шансы перед матчем: у сыгранного - как их видела модель до него, у остальных - по тому,
// что известно сейчас. nil - для матча шанс не считается (2×2).
func (s *Store) PrematchChance(ctx context.Context, t models.Tournament) (*models.Chance, error) {
	_, users, ok := chanceSides(t)
	if !ok {
		return nil, nil
	}
	snap, err := s.chances(ctx, false)
	if err != nil {
		return nil, err
	}
	if h, ok := snap.matches[t.ID]; ok && t.Status == "finished" {
		return &h.Pre, nil
	}
	pre := chanceSetupFor(snap.model, t, users).prematch()
	return &pre, nil
}

// ChancePercents - шансы сторон целыми процентами, в сумме ровно 100, и шанс ничьей отдельно.
func ChancePercents(c models.Chance) (a, draw, b int) {
	a = int(math.Round(c.A * 100))
	return a, int(math.Round(c.Draw * 100)), 100 - a
}
