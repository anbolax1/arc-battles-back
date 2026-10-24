package store

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// Пояс лиги переходит к тому, кто обыграл носителя в обычном матче 1×1, и не сбрасывается между
// сезонами. Он выводится из сыгранных матчей заново, как нашивки: руками его никто не правит.

const (
	// BeltRustDays - столько игровых дней подряд без матча носителя, и пояс выпадает.
	BeltRustDays = 8
	// BeltHonorDefenses - защит пояса за всё время для тега «Почётный носитель».
	BeltHonorDefenses = 10
)

type beltSide struct {
	user   string
	points int
	rated  bool // у матча есть изменение MMR этой стороны
	after  int
}

type beltMatch struct {
	id     string
	at     time.Time
	season string
	show   bool
	double bool
	games  int
	winner int // сторона-победитель; -1 - ничья
	sides  [2]beltSide
}

type beltFix struct {
	user, season string
	at           time.Time
	after        int
}

// beltInput - все завершённые матчи 1×1 по времени со сверками рейтинга. Шоу-матчи пояс не трогают,
// но двигают таблицу сезона, а по ней ищется лидер, который разыграет ничей пояс.
type beltInput struct {
	matches []beltMatch
	fixes   []beltFix
	current string // идущий сезон: в нём ищется лидер, пока пояс ничей
}

type beltDef struct {
	at     time.Time
	match  string
	season string
	opp    string
	score  []int
	double bool
}

type beltReign struct {
	holder string
	from   time.Time
	to     time.Time // нулевое - держит сейчас
	how    string    // first | take | pick
	match  string
	season string
	opp    string
	score  []int
	defs   []beltDef
	end    string // lost | drop
	lostTo string
}

type beltState struct {
	reigns  []beltReign
	rust    int                  // игровых дней подряд без матча нынешнего носителя
	leaders []string             // пояс ничей: лидеры таблицы идущего сезона
	first   map[string]time.Time // первый обычный матч каждого сезона
}

// beltScore - счёт матча со стороны side; у матчей из таблицы организатора очков нет.
func beltScore(m *beltMatch, side int) []int {
	own, opp := m.sides[side].points, m.sides[1-side].points
	if own == 0 && opp == 0 {
		return nil
	}
	return []int{own, opp}
}

// computeBelt проходит матчи по времени: пояс у победителя первого матча лиги, дальше переходит к тому,
// кто обыграл носителя. Носитель, пропустивший BeltRustDays игровых дней подряд, теряет пояс в полночь
// после последнего из них, и ничей пояс разыгрывает ближайший обычный матч лидера таблицы сезона.
func computeBelt(in beltInput, now time.Time) beltState {
	st := beltState{first: map[string]time.Time{}}
	// Игровой день - день, когда в лиге был хотя бы один обычный матч: между сезонами часы пояса стоят.
	var days []string
	for _, m := range in.matches {
		if m.show {
			continue
		}
		if d := patchDay(m.at); len(days) == 0 || days[len(days)-1] != d {
			days = append(days, d)
		}
		if _, ok := st.first[m.season]; !ok {
			st.first[m.season] = m.at
		}
	}
	// Игровые дни после дня last и до дня upto: в сам день upto носитель ещё может сыграть.
	idle := func(last, upto string) int {
		i := sort.Search(len(days), func(k int) bool { return days[k] > last })
		j := sort.Search(len(days), func(k int) bool { return days[k] >= upto })
		return max(0, j-i)
	}
	dropAt := func(last string) time.Time {
		i := sort.Search(len(days), func(k int) bool { return days[k] > last })
		d, _ := time.ParseInLocation("2006-01-02", days[i+BeltRustDays-1], patchZone)
		return d.AddDate(0, 0, 1)
	}

	mmr := map[string]map[string]int{}
	wins := map[string]map[string]int{}
	table := func(season string) (map[string]int, map[string]int) {
		if mmr[season] == nil {
			mmr[season], wins[season] = map[string]int{}, map[string]int{}
		}
		return mmr[season], wins[season]
	}
	// Лидер - как в таблице сезона: больше MMR, при равном - больше побед.
	leaders := func(season string) []string {
		m, w := table(season)
		best, bestWins := math.MinInt, math.MinInt
		var out []string
		for u, v := range m {
			switch {
			case v > best || v == best && w[u] > bestWins:
				best, bestWins, out = v, w[u], []string{u}
			case v == best && w[u] == bestWins:
				out = append(out, u)
			}
		}
		sort.Strings(out)
		return out
	}

	holder, last := "", ""
	open := func(m *beltMatch, side int, how string) {
		st.reigns = append(st.reigns, beltReign{
			holder: m.sides[side].user, from: m.at, how: how, match: m.id, season: m.season,
			opp: m.sides[1-side].user, score: beltScore(m, side),
		})
		holder, last = m.sides[side].user, patchDay(m.at)
	}
	rust := func(day string) {
		if holder != "" && idle(last, day) >= BeltRustDays {
			r := &st.reigns[len(st.reigns)-1]
			r.to, r.end = dropAt(last), "drop"
			holder = ""
		}
	}

	// Сверка в один момент с матчем идёт раньше него, как в пересчёте MMR.
	type event struct {
		fix   *beltFix
		match *beltMatch
		at    time.Time
	}
	events := make([]event, 0, len(in.matches)+len(in.fixes))
	for i := range in.fixes {
		events = append(events, event{fix: &in.fixes[i], at: in.fixes[i].at})
	}
	for i := range in.matches {
		events = append(events, event{match: &in.matches[i], at: in.matches[i].at})
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].at.Equal(events[j].at) {
			return events[i].at.Before(events[j].at)
		}
		return events[i].fix != nil && events[j].fix == nil
	})

	for _, e := range events {
		if f := e.fix; f != nil {
			m, _ := table(f.season)
			m[f.user] = f.after
			continue
		}
		m := e.match
		a, b := m.sides[0].user, m.sides[1].user
		if !m.show && a != "" && b != "" {
			day := patchDay(m.at)
			rust(day)
			switch {
			case holder == a || holder == b:
				last = day
				if m.winner < 0 {
					break // ничья оставляет пояс у носителя
				}
				r := &st.reigns[len(st.reigns)-1]
				if m.sides[m.winner].user == holder {
					r.defs = append(r.defs, beltDef{at: m.at, match: m.id, season: m.season, opp: m.sides[1-m.winner].user,
						score: beltScore(m, m.winner), double: m.double})
				} else {
					r.to, r.end, r.lostTo = m.at, "lost", m.sides[m.winner].user
					open(m, m.winner, "take")
				}
			case holder == "" && m.winner >= 0:
				if len(st.reigns) == 0 {
					open(m, m.winner, "first")
				} else if ld := leaders(m.season); len(ld) == 0 || slices.Contains(ld, a) || slices.Contains(ld, b) {
					open(m, m.winner, "pick")
				}
			}
		}
		tm, tw := table(m.season)
		for _, sd := range m.sides {
			if sd.user != "" && sd.rated {
				tm[sd.user] = sd.after
			}
		}
		if m.winner >= 0 && m.sides[m.winner].user != "" {
			tw[m.sides[m.winner].user] += max(m.games, 1)
		}
	}

	today := patchDay(now)
	rust(today)
	if holder != "" {
		st.rust = idle(last, today)
	} else if len(st.reigns) > 0 {
		st.leaders = leaders(in.current)
	}
	return st
}

// beltSeason - пояс лиги глазами одного сезона: для нашивки «Носитель пояса».
type beltSeason struct {
	start    string            // носитель к началу сезона
	takes    map[string]string // матч сезона -> кто в нём взял пояс
	reigns   map[string]int    // сколько раз игрок держал пояс в сезоне
	defenses map[string]int    // защиты в матчах сезона
}

// forSeason - кто держал пояс в сезоне: кто вошёл в сезон с поясом и кто взял его в матчах сезона.
func (st beltState) forSeason(sn models.Season) beltSeason {
	out := beltSeason{takes: map[string]string{}, reigns: map[string]int{}, defenses: map[string]int{}}
	// Матчи сезона бывают и до даты его начала: сезон открыли раньше, чем подошла дата.
	begin := sn.StartedAt
	if f, ok := st.first[sn.ID]; ok && f.Before(begin) {
		begin = f
	}
	for _, r := range st.reigns {
		switch {
		case r.season == sn.ID:
			out.takes[r.match] = r.holder
			out.reigns[r.holder]++
		case r.from.Before(begin) && (r.to.IsZero() || r.to.After(begin)):
			out.start = r.holder
			out.reigns[r.holder]++
		}
		for _, d := range r.defs {
			if d.season == sn.ID {
				out.defenses[r.holder]++
			}
		}
	}
	return out
}

// honored - кто набрал защит пояса на тег «Почётный носитель».
func (st beltState) honored() []string {
	total := map[string]int{}
	for _, r := range st.reigns {
		total[r.holder] += len(r.defs)
	}
	out := []string{}
	for u, n := range total {
		if n >= BeltHonorDefenses {
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

// model - пояс для сайта: игроки с логинами и именами, у каждого владения - в который раз игрок взял пояс.
func (st beltState) model(names map[string]models.BeltPlayer) models.Belt {
	out := models.Belt{Rust: st.rust, RustLimit: BeltRustDays, HonorAt: BeltHonorDefenses, Reigns: []models.BeltReign{}}
	player := func(id string) models.BeltPlayer {
		if p, ok := names[id]; ok {
			return p
		}
		return models.BeltPlayer{UserID: id, Login: id, DisplayName: id}
	}
	times := map[string]int{}
	newcomers := 0
	for _, r := range st.reigns {
		times[r.holder]++
		mr := models.BeltReign{
			Holder: player(r.holder), From: r.from, How: r.how, MatchID: r.match, Score: r.score,
			Defenses: []models.BeltDefense{}, End: r.end, Time: times[r.holder],
		}
		if mr.Time == 1 {
			newcomers++
			mr.Newcomer = newcomers
		}
		if r.opp != "" {
			opp := player(r.opp)
			mr.Opponent = &opp
		}
		if !r.to.IsZero() {
			to := r.to
			mr.To = &to
		}
		if r.lostTo != "" {
			lt := player(r.lostTo)
			mr.LostTo = &lt
		}
		for _, d := range r.defs {
			mr.Defenses = append(mr.Defenses, models.BeltDefense{At: d.at, MatchID: d.match, Opponent: player(d.opp), Score: d.score, Double: d.double})
		}
		out.Reigns = append(out.Reigns, mr)
	}
	if n := len(st.reigns); n > 0 {
		switch r := st.reigns[n-1]; r.end {
		case "":
			h, since := player(r.holder), r.from
			out.Holder, out.Since, out.Defenses = &h, &since, len(r.defs)
		case "drop":
			at := r.to
			out.DroppedAt = &at
			for _, u := range st.leaders {
				out.Leaders = append(out.Leaders, player(u))
			}
		}
	}
	return out
}

// loadBeltInput - все завершённые матчи 1×1 со сторонами и MMR после матча, сверки рейтинга
// и имена игроков для сайта. Незасчитанный матч для пояса - как ничья: пояс он не забирает и не защищает.
func (s *Store) loadBeltInput(ctx context.Context) (beltInput, map[string]models.BeltPlayer, error) {
	in := beltInput{}
	names := map[string]models.BeltPlayer{}
	rows, err := s.Pool.Query(ctx, `
		SELECT t.id, COALESCE(t.starts_at, t.created_at), t.season_id, t.format = 'show', t.rating_multiplier, t.games,
		       CASE WHEN t.voided THEN '' ELSE COALESCE(t.winner_participant_id, '') END, p.id, COALESCE(p.user_id, ''), p.total_points,
		       COALESCE(u.login, ''), COALESCE(NULLIF(u.display_name, ''), u.login, p.name)
		FROM tournaments t
		JOIN participants p ON p.tournament_id = t.id AND p.kind = 'player'
		LEFT JOIN users u ON u.id = p.user_id
		WHERE t.mode = '1x1' AND t.status = 'finished' AND t.season_id IS NOT NULL
		ORDER BY COALESCE(t.starts_at, t.created_at), t.created_at, t.id, p.seed, p.id`)
	if err != nil {
		return in, nil, err
	}
	byID := map[string]int{}
	sides := map[string]int{}
	for rows.Next() {
		var id, season, winner, pid, user, login, name string
		var at time.Time
		var show bool
		var mult, games, points int
		if err := rows.Scan(&id, &at, &season, &show, &mult, &games, &winner, &pid, &user, &points, &login, &name); err != nil {
			rows.Close()
			return in, nil, err
		}
		mi, ok := byID[id]
		if !ok {
			mi = len(in.matches)
			byID[id] = mi
			in.matches = append(in.matches, beltMatch{id: id, at: at, season: season, show: show,
				double: mult > 1 || games > 1, games: max(games, 1), winner: -1})
		}
		k := sides[id]
		if k > 1 {
			continue // в 1×1 сторон две
		}
		sides[id]++
		m := &in.matches[mi]
		m.sides[k] = beltSide{user: user, points: points}
		if winner == pid {
			m.winner = k
		}
		if user != "" {
			names[user] = models.BeltPlayer{UserID: user, Login: login, DisplayName: name}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, nil, err
	}

	rows, err = s.Pool.Query(ctx, `
		SELECT COALESCE(tournament_id, ''), user_id, season_key, mmr_after, created_at
		FROM mmr_history WHERE mode = '1x1'
		ORDER BY created_at`)
	if err != nil {
		return in, nil, err
	}
	for rows.Next() {
		var tid, user, season string
		var after int
		var at time.Time
		if err := rows.Scan(&tid, &user, &season, &after, &at); err != nil {
			rows.Close()
			return in, nil, err
		}
		if tid == "" {
			in.fixes = append(in.fixes, beltFix{user: user, season: season, at: at, after: after})
			continue
		}
		if mi, ok := byID[tid]; ok {
			m := &in.matches[mi]
			for i := range m.sides {
				if m.sides[i].user == user {
					m.sides[i].rated, m.sides[i].after = true, after
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, nil, err
	}

	err = s.Pool.QueryRow(ctx, `SELECT id FROM seasons ORDER BY status = 'active' DESC, started_at DESC LIMIT 1`).Scan(&in.current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return in, nil, err
	}
	return in, names, nil
}

type beltCache struct {
	mu      sync.Mutex
	version string
	belt    models.Belt
}

// Belt - пояс лиги на сейчас. Пересчитывается, когда изменились матчи, рейтинг, сезоны или наступил
// новый день: в полночь пояс может выпасть сам.
func (s *Store) Belt(ctx context.Context) (models.Belt, error) {
	ver, err := s.beltVersion(ctx)
	if err != nil {
		return models.Belt{}, err
	}
	c := s.belt
	c.mu.Lock()
	if c.version == ver {
		b := c.belt
		c.mu.Unlock()
		return b, nil
	}
	c.mu.Unlock()
	in, names, err := s.loadBeltInput(ctx)
	if err != nil {
		return models.Belt{}, err
	}
	b := computeBelt(in, time.Now()).model(names)
	c.mu.Lock()
	c.version, c.belt = ver, b
	c.mu.Unlock()
	return b, nil
}

func (s *Store) beltVersion(ctx context.Context) (string, error) {
	var v string
	err := s.Pool.QueryRow(ctx, `
		SELECT concat_ws('|',
			(SELECT count(*) || ':' || COALESCE(max(updated_at)::text, '') FROM tournaments WHERE mode = '1x1'),
			(SELECT count(*) || ':' || COALESCE(sum(delta), 0) || ':' || COALESCE(sum(mmr_after), 0) FROM mmr_history WHERE mode = '1x1'),
			(SELECT string_agg(id || status, ',' ORDER BY started_at) FROM seasons),
			(SELECT COALESCE(max(updated_at)::text, '') FROM users))`).Scan(&v)
	return v + "|" + patchDay(time.Now()), err
}

// MatchBelt - идёт ли матч за пояс лиги, а после матча - чем кончилось; nil - матч пояс не трогает.
func (s *Store) MatchBelt(ctx context.Context, st models.MatchState) (*models.LiveBelt, error) {
	t := st.Tournament
	if t.Mode != "1x1" || t.Format == FormatShow {
		return nil, nil
	}
	b, err := s.Belt(ctx)
	if err != nil {
		return nil, err
	}
	pid := map[string]string{}
	for _, p := range t.Participants {
		if p.UserID != nil {
			pid[*p.UserID] = p.ID
		}
	}
	if st.Stage == "finished" {
		for _, r := range b.Reigns {
			if r.MatchID == t.ID {
				res := "take"
				if r.How == "pick" {
					res = "pick"
				}
				return &models.LiveBelt{HolderParticipantID: pid[r.Holder.UserID], Result: res}, nil
			}
			for i, d := range r.Defenses {
				if d.MatchID == t.ID {
					return &models.LiveBelt{HolderParticipantID: pid[r.Holder.UserID], Defenses: i + 1, Result: "defense"}, nil
				}
			}
		}
		return nil, nil
	}
	if b.Holder != nil {
		if p, ok := pid[b.Holder.UserID]; ok {
			return &models.LiveBelt{HolderParticipantID: p, Defenses: b.Defenses}, nil
		}
		return nil, nil
	}
	// Ничей пояс разыгрывает матч лидера, а пока в сезоне нет матчей - первый же матч.
	if len(b.Reigns) == 0 || len(b.Leaders) == 0 {
		return &models.LiveBelt{}, nil
	}
	for _, l := range b.Leaders {
		if _, ok := pid[l.UserID]; ok {
			return &models.LiveBelt{}, nil
		}
	}
	return nil, nil
}
