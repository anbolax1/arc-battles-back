package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// Нашивки 1×1 выдаются сами по сыгранным матчам. Сезон пересчитывается целиком: правила идут по матчам
// в порядке времени, поэтому дата и матч нашивки от пересчёта к пересчёту не меняются.

// PatchCodes - все нашивки в порядке показа; названия и рисунки по этим кодам держит фронт.
var PatchCodes = []string{
	"top1", "belt", "regicide",
	"streak", "flawless", "david", "comeback", "double", "photo", "shutout", "revenge",
	"hunter", "clear", "topknock", "pacifist", "king",
	"first", "final", "marathon", "veteran",
}

const (
	patchStreakWins   = 5   // «Неудержимый»: побед подряд
	patchFlawlessWins = 5   // «Без поражений»: побед за сезон без единого поражения
	patchUnderdog     = 0.4 // «Против шансов»: шанс победы по рейтингу не выше этого
	patchClearKnocks  = 8   // «Зачистка»: ноков за один рейд
	patchKingWins     = 3   // «Король карты»: меньше побед на карте - не король
	patchMarathon     = 20  // «Марафонец»: матчей за сезон
)

// patchHunterTiers - ступени «Охотника»: ноков за сезон.
var patchHunterTiers = []int{10, 25, 50}

// День матча - по Москве: по московскому времени идут эфиры и расписание.
var patchZone = time.FixedZone("MSK", 3*60*60)

type patchSide struct {
	pid, user, name string
	total           int
	rated           bool // у матча есть изменение MMR этой стороны
	before, after   int
	knocks          map[int]int // ноки по номерам раундов
	round1          int
	hasRound1       bool
}

type patchMatch struct {
	id          string
	at          time.Time
	live        bool // матч идёт: в зачёт только ноки
	mult, games int
	winner      int // сторона-победитель; -1 - ничья
	sides       [2]patchSide
	maps        []string       // карты матча для «Короля карты»
	roundMaps   map[int]string // карта каждого раунда
}

// patchMmrFix - сверка рейтинга между матчами: от неё зависит, кто лидер таблицы.
type patchMmrFix struct {
	user  string
	at    time.Time
	after int
}

// patchInput - всё, из чего считаются нашивки одного сезона.
type patchInput struct {
	season    models.Season
	matches   []patchMatch // по времени
	fixes     []patchMmrFix
	prevGames map[string]int // матчей в прошлом сезоне
	prevChamp string         // чемпион прошлого сезона: с ним сезон начинает пояс
	leader    string         // первое место таблицы сезона
	leaderMmr int
}

type patchAward struct {
	user, code  string
	tier        int
	match       string // пусто - нашивка за весь сезон
	at          time.Time
	provisional bool
	detail      map[string]any
}

type patchPlayer struct {
	wins, losses, games, played int
	streak, bestStreak          int
	knocks                      int
	david, comeback, double     int
	reigns, defenses            int
	lostTo                      map[string]bool
	mapWins                     map[string]int
	bestRound                   map[string]any
	bestRoundKnocks             int
}

func patchDay(t time.Time) string { return t.In(patchZone).Format("2006-01-02") }

func patchKey(user, code string, tier int) string {
	return user + "|" + code + "|" + strconv.Itoa(tier)
}

func sumKnocks(k map[int]int) int {
	n := 0
	for _, v := range k {
		n += v
	}
	return n
}

// computeSeasonPatches выдаёт нашивку в тот матч, где выполнилось её условие. Итоговые нашивки
// сезона выдаются по его концу, а пока сезон идёт - как предварительные.
func computeSeasonPatches(in patchInput) []patchAward {
	final := in.season.Status == "finished"
	players := map[string]*patchPlayer{}
	get := func(u string) *patchPlayer {
		p := players[u]
		if p == nil {
			p = &patchPlayer{lostTo: map[string]bool{}, mapWins: map[string]int{}}
			players[u] = p
		}
		return p
	}
	var out []patchAward
	got := map[string]bool{}
	award := func(user, code string, tier int, match string, at time.Time, detail map[string]any) {
		key := patchKey(user, code, tier)
		if got[key] {
			return
		}
		got[key] = true
		out = append(out, patchAward{user: user, code: code, tier: tier, match: match, at: at, detail: detail})
	}

	firstDay, lastDay := "", ""
	var lastAt time.Time
	for _, m := range in.matches {
		if m.live {
			continue
		}
		if firstDay == "" {
			firstDay = patchDay(m.at)
		}
		lastDay, lastAt = patchDay(m.at), m.at
	}

	holder := in.prevChamp
	if holder != "" {
		get(holder).reigns = 1
		award(holder, "belt", 1, "", in.season.StartedAt, nil)
	}

	// Сверка в один момент с матчем идёт раньше него, как в пересчёте MMR.
	type event struct {
		fix   *patchMmrFix
		match *patchMatch
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

	mmr := map[string]int{}
	leaders := func() map[string]bool {
		best, set := math.MinInt, map[string]bool{}
		for u, v := range mmr {
			switch {
			case v > best:
				best, set = v, map[string]bool{u: true}
			case v == best:
				set[u] = true
			}
		}
		return set
	}

	for _, e := range events {
		if e.fix != nil {
			mmr[e.fix.user] = e.fix.after
			continue
		}
		m := e.match
		// Ноки считаются и в идущем матче: «Зачистка» и «Охотник» выдаются прямо в эфире.
		for i, sd := range m.sides {
			if sd.user == "" {
				continue
			}
			p := get(sd.user)
			rounds := make([]int, 0, len(sd.knocks))
			for rn := range sd.knocks {
				rounds = append(rounds, rn)
			}
			sort.Ints(rounds)
			for _, rn := range rounds {
				k := sd.knocks[rn]
				before := p.knocks
				p.knocks += k
				for t, need := range patchHunterTiers {
					if before < need && p.knocks >= need {
						award(sd.user, "hunter", t+1, m.id, m.at, nil)
					}
				}
				if k > p.bestRoundKnocks {
					p.bestRoundKnocks = k
					p.bestRound = map[string]any{"knocks": k, "map": m.roundMaps[rn], "opp": m.sides[1-i].name}
				}
				if k >= patchClearKnocks {
					award(sd.user, "clear", 1, m.id, m.at, nil)
				}
			}
		}
		if m.live {
			continue
		}

		lead := leaders()
		day := patchDay(m.at)
		for _, sd := range m.sides {
			if sd.user == "" {
				continue
			}
			p := get(sd.user)
			if p.played == 0 && in.prevGames[sd.user] > 0 {
				award(sd.user, "veteran", 1, m.id, m.at, nil)
			}
			p.played++
			if day == firstDay {
				award(sd.user, "first", 1, m.id, m.at, nil)
			}
			before := p.games
			p.games += max(m.games, 1)
			if before < patchMarathon && p.games >= patchMarathon {
				award(sd.user, "marathon", 1, m.id, m.at, nil)
			}
		}

		if m.winner < 0 {
			for _, sd := range m.sides {
				if sd.user != "" {
					get(sd.user).streak = 0
				}
			}
		} else if w, l := m.sides[m.winner], m.sides[1-m.winner]; w.user != "" && l.user != "" {
			pw, pl := get(w.user), get(l.user)
			g := max(m.games, 1)
			pw.wins += g
			pl.losses += g
			pw.streak++
			pl.streak = 0
			pw.bestStreak = max(pw.bestStreak, pw.streak)
			if pw.streak == patchStreakWins {
				award(w.user, "streak", 1, m.id, m.at, nil)
			}
			if lead[l.user] && !lead[w.user] {
				award(w.user, "regicide", 1, m.id, m.at, map[string]any{"opp": l.name})
			}
			if w.rated && l.rated && expectedScore(w.before, l.before) <= patchUnderdog {
				pw.david++
				award(w.user, "david", 1, m.id, m.at, nil)
			}
			if m.mult > 1 || m.games > 1 {
				pw.double++
				award(w.user, "double", 1, m.id, m.at, nil)
			}
			// Матчи из таблицы перенесены без очков: у них 0:0, по счёту они не судятся.
			if w.total > 0 || l.total > 0 {
				score := []int{w.total, l.total}
				if w.total-l.total == 1 {
					award(w.user, "photo", 1, m.id, m.at, map[string]any{"opp": l.name, "score": score})
				}
				if l.total == 0 {
					award(w.user, "shutout", 1, m.id, m.at, map[string]any{"opp": l.name, "score": score})
				}
				if sumKnocks(l.knocks) > 0 && sumKnocks(w.knocks) == 0 {
					award(w.user, "pacifist", 1, m.id, m.at, map[string]any{"opp": l.name, "score": score})
				}
			}
			if w.hasRound1 && l.hasRound1 && w.round1 < l.round1 {
				pw.comeback++
				award(w.user, "comeback", 1, m.id, m.at, nil)
			}
			if pw.lostTo[l.user] {
				award(w.user, "revenge", 1, m.id, m.at, map[string]any{"opp": l.name})
			}
			pl.lostTo[w.user] = true
			switch holder {
			case w.user:
				pw.defenses++
			case l.user:
				holder = w.user
				pw.reigns++
				award(w.user, "belt", 1, m.id, m.at, nil)
			}
			for _, mp := range m.maps {
				pw.mapWins[mp]++
			}
		}
		for _, sd := range m.sides {
			if sd.user != "" && sd.rated {
				mmr[sd.user] = sd.after
			}
		}
	}

	// Итоговые нашивки сезона: без матча, датой конца сезона.
	if lastDay != "" {
		endAt := lastAt
		if final && in.season.EndedAt != nil && in.season.EndedAt.After(endAt) {
			endAt = *in.season.EndedAt
		}
		seasonal := func(user, code string, detail map[string]any) {
			if got[patchKey(user, code, 1)] {
				return
			}
			award(user, code, 1, "", endAt, detail)
			out[len(out)-1].provisional = !final
		}
		users := make([]string, 0, len(players))
		for u := range players {
			users = append(users, u)
		}
		sort.Strings(users)
		if in.leader != "" {
			seasonal(in.leader, "top1", map[string]any{"mmr": in.leaderMmr})
		}
		topKnocks := 0
		kings := map[string]map[string]any{}
		for _, u := range users {
			p := players[u]
			if p.wins >= patchFlawlessWins && p.losses == 0 {
				seasonal(u, "flawless", map[string]any{"wins": p.wins})
			}
			topKnocks = max(topKnocks, p.knocks)
		}
		for _, u := range users {
			if p := players[u]; topKnocks > 0 && p.knocks == topKnocks {
				seasonal(u, "topknock", map[string]any{"knocks": p.knocks})
			}
		}
		best := map[string]int{}
		for _, u := range users {
			for mp, n := range players[u].mapWins {
				best[mp] = max(best[mp], n)
			}
		}
		maps := make([]string, 0, len(best))
		for mp := range best {
			maps = append(maps, mp)
		}
		sort.Strings(maps)
		for _, mp := range maps {
			if best[mp] < patchKingWins {
				continue
			}
			for _, u := range users {
				if players[u].mapWins[mp] != best[mp] {
					continue
				}
				if kings[u] == nil {
					kings[u] = map[string]any{"maps": []string{}, "wins": 0}
				}
				kings[u]["maps"] = append(kings[u]["maps"].([]string), mp)
				kings[u]["wins"] = max(kings[u]["wins"].(int), best[mp])
			}
		}
		for _, u := range users {
			if kings[u] != nil {
				seasonal(u, "king", kings[u])
			}
		}
		if final {
			for _, m := range in.matches {
				if !m.live && m.winner >= 0 && patchDay(m.at) == lastDay {
					if w := m.sides[m.winner]; w.user != "" {
						award(w.user, "final", 1, m.id, m.at, nil)
					}
				}
			}
		}
	}

	// Подробности - по итогу сезона на сейчас: лучшая серия, число раз, все ноки.
	for i := range out {
		a := &out[i]
		p := players[a.user]
		if p == nil {
			p = get(a.user)
		}
		switch a.code {
		case "streak":
			a.detail = map[string]any{"best": p.bestStreak}
		case "david":
			a.detail = map[string]any{"n": p.david}
		case "comeback":
			a.detail = map[string]any{"n": p.comeback}
		case "double":
			a.detail = map[string]any{"n": p.double}
		case "hunter":
			a.detail = map[string]any{"knocks": p.knocks}
		case "clear":
			a.detail = p.bestRound
		case "marathon":
			a.detail = map[string]any{"games": p.games}
		case "veteran":
			a.detail = map[string]any{"games": in.prevGames[a.user]}
		case "belt":
			a.detail = map[string]any{"reigns": p.reigns, "defenses": p.defenses}
		}
		if a.detail == nil {
			a.detail = map[string]any{}
		}
	}
	return out
}

// patchMapKey - в старых сезонах карты набраны капсом и без «ё»: одна карта - один ключ.
func patchMapKey(name string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(name)), "Ё", "Е")
}

// loadPatchInput собирает матчи сезона с рейтингом до и после, очками, ноками и картами.
func (s *Store) loadPatchInput(ctx context.Context, sn models.Season, prev *models.Season) (patchInput, error) {
	in := patchInput{season: sn, prevGames: map[string]int{}}
	const scope = `t.season_id = $1 AND t.mode = '1x1' AND t.status IN ('finished', 'live')`

	rows, err := s.Pool.Query(ctx, `
		SELECT t.id, COALESCE(t.starts_at, t.created_at), t.status, t.rating_multiplier, t.games,
		       COALESCE(t.winner_participant_id, ''), t.maps
		FROM tournaments t WHERE `+scope+`
		ORDER BY 2, t.created_at, t.id`, sn.ID)
	if err != nil {
		return in, err
	}
	byID := map[string]*patchMatch{}
	winners := map[string]string{}
	listMaps := map[string][]string{}
	var matches []*patchMatch
	for rows.Next() {
		m := &patchMatch{winner: -1, roundMaps: map[int]string{}}
		var status, winner string
		var maps []byte
		if err := rows.Scan(&m.id, &m.at, &status, &m.mult, &m.games, &winner, &maps); err != nil {
			rows.Close()
			return in, err
		}
		m.live = status == "live"
		var names []string
		_ = json.Unmarshal(maps, &names)
		listMaps[m.id] = names
		winners[m.id] = winner
		byID[m.id] = m
		matches = append(matches, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	rows, err = s.Pool.Query(ctx, `
		SELECT p.tournament_id, p.id, COALESCE(p.user_id, ''), p.total_points,
		       COALESCE(NULLIF(u.display_name, ''), u.login, p.name)
		FROM participants p
		JOIN tournaments t ON t.id = p.tournament_id
		LEFT JOIN users u ON u.id = p.user_id
		WHERE `+scope+` AND p.kind = 'player'
		ORDER BY p.tournament_id, p.seed, p.id`, sn.ID)
	if err != nil {
		return in, err
	}
	count := map[string]int{}
	side := map[string]*patchSide{}
	for rows.Next() {
		var tid string
		sd := patchSide{knocks: map[int]int{}}
		if err := rows.Scan(&tid, &sd.pid, &sd.user, &sd.total, &sd.name); err != nil {
			rows.Close()
			return in, err
		}
		m := byID[tid]
		if m == nil || count[tid] >= 2 {
			continue
		}
		i := count[tid]
		count[tid]++
		m.sides[i] = sd
		if sd.pid == winners[tid] {
			m.winner = i
		}
		side[sd.pid] = &m.sides[i]
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	rows, err = s.Pool.Query(ctx, `
		SELECT COALESCE(tournament_id, ''), user_id, mmr_before, mmr_after, created_at
		FROM mmr_history WHERE season_key = $1 AND mode = '1x1'
		ORDER BY created_at`, sn.ID)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var tid, user string
		var before, after int
		var at time.Time
		if err := rows.Scan(&tid, &user, &before, &after, &at); err != nil {
			rows.Close()
			return in, err
		}
		if tid == "" {
			in.fixes = append(in.fixes, patchMmrFix{user: user, at: at, after: after})
			continue
		}
		if m := byID[tid]; m != nil {
			for i := range m.sides {
				if m.sides[i].user == user {
					m.sides[i].rated, m.sides[i].before, m.sides[i].after = true, before, after
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	rows, err = s.Pool.Query(ctx, `
		SELECT r.tournament_id, r.number, r.map
		FROM rounds r JOIN tournaments t ON t.id = r.tournament_id
		WHERE `+scope+` AND r.map <> '' AND r.status <> 'pending'
		ORDER BY r.tournament_id, r.number`, sn.ID)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var tid, mp string
		var n int
		if err := rows.Scan(&tid, &n, &mp); err != nil {
			rows.Close()
			return in, err
		}
		if m := byID[tid]; m != nil {
			m.roundMaps[n] = mp
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	rows, err = s.Pool.Query(ctx, `
		SELECT re.participant_id, r.number, re.knocks
		FROM round_entries re
		JOIN rounds r ON r.id = re.round_id
		JOIN tournaments t ON t.id = r.tournament_id
		WHERE `+scope+` AND re.knocks > 0`, sn.ID)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var pid string
		var n, k int
		if err := rows.Scan(&pid, &n, &k); err != nil {
			rows.Close()
			return in, err
		}
		if sd := side[pid]; sd != nil {
			sd.knocks[n] += k
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	// Очки первого раунда - из тех же частей, что счёт раунда в матче: ручные с ноками, задания
	// старого пульта, контракты (свои и чужие) и легендарки.
	rows, err = s.Pool.Query(ctx, `
		SELECT x.pid, SUM(x.pts)::int
		FROM rounds r
		JOIN tournaments t ON t.id = r.tournament_id
		JOIN LATERAL (
			SELECT re.participant_id AS pid, re.points AS pts FROM round_entries re WHERE re.round_id = r.id
			UNION ALL
			SELECT rstd.participant_id, rstd.times * st.points
			FROM round_starter_tasks rst
			JOIN round_starter_task_done rstd ON rstd.round_starter_task_id = rst.id
			JOIN starter_tasks st ON st.id = rst.starter_task_id
			WHERE rst.round_id = r.id
			UNION ALL
			SELECT rbt.completed_by, CASE WHEN rbt.completed_by = rbt.participant_id THEN ct.points ELSE $2::int END
			FROM round_bonus_tasks rbt JOIN catalog_tasks ct ON ct.id = rbt.task_id
			WHERE rbt.round_id = r.id AND rbt.completed_by IS NOT NULL
			UNION ALL
			SELECT lcc.participant_id, lc.points
			FROM legendary_contract_completions lcc
			JOIN legendary_contracts lc ON lc.id = lcc.legendary_contract_id
			WHERE lcc.participant_id IS NOT NULL
			  AND (lcc.round_id = r.id OR (lcc.round_id IS NULL AND lcc.tournament_id = r.tournament_id))
		) x ON true
		WHERE `+scope+` AND r.number = 1 AND t.status = 'finished'
		GROUP BY x.pid`, sn.ID, ContractCrossPoints)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var pid string
		var pts int
		if err := rows.Scan(&pid, &pts); err != nil {
			rows.Close()
			return in, err
		}
		if sd := side[pid]; sd != nil {
			sd.round1, sd.hasRound1 = pts, true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}
	// Раунд без очков у обеих сторон - матч перенесён без раундов, по первому раунду не судится.
	for _, m := range matches {
		if m.sides[0].round1 == 0 && m.sides[1].round1 == 0 {
			m.sides[0].hasRound1, m.sides[1].hasRound1 = false, false
		}
	}

	// Карты для «Короля карты» - как в разборе по картам в профиле: сыгранные раунды, иначе список матча.
	names := map[string]string{}
	for _, m := range matches {
		src := make([]string, 0, len(m.roundMaps))
		for _, mp := range m.roundMaps {
			src = append(src, mp)
		}
		if len(src) == 0 {
			src = listMaps[m.id]
		}
		seen := map[string]bool{}
		for _, mp := range src {
			key := patchMapKey(mp)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			if cur, ok := names[key]; !ok || (cur == strings.ToUpper(cur) && mp != strings.ToUpper(mp)) {
				names[key] = mp
			}
			m.maps = append(m.maps, key)
		}
	}
	for _, m := range matches {
		for i, key := range m.maps {
			m.maps[i] = names[key]
		}
		sort.Strings(m.maps)
		if m.sides[0].pid != "" && m.sides[1].pid != "" {
			in.matches = append(in.matches, *m)
		}
	}

	if prev != nil {
		rows, err := s.Leaderboard(ctx, "1x1", prev.ID)
		if err != nil {
			return in, err
		}
		for _, r := range rows {
			in.prevGames[r.UserID] = r.Tournaments
		}
		if prev.Status == "finished" && len(rows) > 0 {
			in.prevChamp = rows[0].UserID
		}
	}
	cur, err := s.Leaderboard(ctx, "1x1", sn.ID)
	if err != nil {
		return in, err
	}
	if len(cur) > 0 {
		in.leader, in.leaderMmr = cur[0].UserID, cur[0].Mmr
	}
	return in, nil
}

// PatchNew - нашивка, которой не было до пересчёта.
type PatchNew struct {
	UserID, SeasonID, Code, MatchID string
	Tier                            int
	Detail                          json.RawMessage
}

// patchSyncLock - номер блокировки пересчёта нашивок: два пересчёта сразу столкнулись бы на вставке.
const patchSyncLock = 7461

// SyncPatches пересчитывает нашивки всех сезонов и возвращает новые - их не было до пересчёта и они
// не предварительные: по ним оверлей показывает плашку.
func (s *Store) SyncPatches(ctx context.Context) ([]PatchNew, error) {
	seasons, err := s.ListSeasons(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(seasons, func(i, j int) bool { return seasons[i].StartedAt.Before(seasons[j].StartedAt) })

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, patchSyncLock); err != nil {
		return nil, err
	}

	awards := map[string][]patchAward{}
	for i, sn := range seasons {
		var prev *models.Season
		if i > 0 {
			prev = &seasons[i-1]
		}
		in, err := s.loadPatchInput(ctx, sn, prev)
		if err != nil {
			return nil, fmt.Errorf("сезон %s: %w", sn.Name, err)
		}
		awards[sn.ID] = computeSeasonPatches(in)
	}

	old := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT user_id, season_id, code, tier FROM player_patches WHERE NOT provisional`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u, sid, code string
		var tier int
		if err := rows.Scan(&u, &sid, &code, &tier); err != nil {
			rows.Close()
			return nil, err
		}
		old[sid+"|"+patchKey(u, code, tier)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM player_patches`); err != nil {
		return nil, err
	}
	batch := &pgx.Batch{}
	var fresh []PatchNew
	for _, sn := range seasons {
		for _, a := range awards[sn.ID] {
			detail, err := json.Marshal(a.detail)
			if err != nil {
				return nil, err
			}
			var match *string
			if a.match != "" {
				match = &a.match
			}
			batch.Queue(`
				INSERT INTO player_patches (user_id, season_id, code, tier, tournament_id, earned_at, provisional, detail)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				a.user, sn.ID, a.code, a.tier, match, a.at, a.provisional, detail)
			if !a.provisional && !old[sn.ID+"|"+patchKey(a.user, a.code, a.tier)] {
				fresh = append(fresh, PatchNew{UserID: a.user, SeasonID: sn.ID, Code: a.code, MatchID: a.match, Tier: a.tier, Detail: detail})
			}
		}
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return fresh, nil
}

func patchOrder(code string) int {
	for i, c := range PatchCodes {
		if c == code {
			return i
		}
	}
	return len(PatchCodes)
}

// seasonPlayers - сколько рейдеров сыграло в сезоне хотя бы один завершённый матч 1×1.
func (s *Store) seasonPlayers(ctx context.Context) (map[string]int, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT t.season_id, COUNT(DISTINCT p.user_id)::int
		FROM participants p JOIN tournaments t ON t.id = p.tournament_id
		WHERE t.season_id IS NOT NULL AND t.mode = '1x1' AND t.status = 'finished'
		  AND p.kind = 'player' AND p.user_id IS NOT NULL
		GROUP BY t.season_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var sid string
		var n int
		if err := rows.Scan(&sid, &n); err != nil {
			return nil, err
		}
		out[sid] = n
	}
	return out, rows.Err()
}

// PlayerPatches - нашивки игрока по сезонам; у «Охотника» - высшая ступень.
func (s *Store) PlayerPatches(ctx context.Context, userID string) (map[string]models.PatchSeason, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT pp.season_id, sn.status = 'finished', pp.code, pp.tier, pp.tournament_id, pp.earned_at,
		       pp.provisional, pp.detail,
		       (SELECT COUNT(DISTINCT x.user_id) FROM player_patches x
		        WHERE x.season_id = pp.season_id AND x.code = pp.code)::int
		FROM player_patches pp JOIN seasons sn ON sn.id = pp.season_id
		WHERE pp.user_id = $1
		ORDER BY pp.season_id, pp.code, pp.tier`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]models.PatchSeason{}
	for rows.Next() {
		var p models.PlayerPatch
		var final bool
		if err := rows.Scan(&p.SeasonID, &final, &p.Code, &p.Tier, &p.MatchID, &p.EarnedAt, &p.Provisional, &p.Detail, &p.Holders); err != nil {
			return nil, err
		}
		ps := out[p.SeasonID]
		ps.Final = final
		if n := len(ps.Items); n > 0 && ps.Items[n-1].Code == p.Code {
			ps.Items[n-1] = p
		} else {
			ps.Items = append(ps.Items, p)
		}
		out[p.SeasonID] = ps
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	players, err := s.seasonPlayers(ctx)
	if err != nil {
		return nil, err
	}
	for sid, ps := range out {
		ps.Players = players[sid]
		sort.SliceStable(ps.Items, func(i, j int) bool { return patchOrder(ps.Items[i].Code) < patchOrder(ps.Items[j].Code) })
		out[sid] = ps
	}
	return out, nil
}

// PatchCatalogFor - каталог нашивок сезона: владельцы каждой по месту в таблице сезона.
func (s *Store) PatchCatalogFor(ctx context.Context, sn models.Season) (models.PatchCatalog, error) {
	cat := models.PatchCatalog{Season: sn, Patches: []models.PatchStat{}}
	board, err := s.Leaderboard(ctx, "1x1", sn.ID)
	if err != nil {
		return cat, err
	}
	cat.Players = len(board)
	place := map[string]int{}
	for i, r := range board {
		place[r.UserID] = i + 1
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT pp.user_id, u.login, COALESCE(NULLIF(u.display_name, ''), u.login), pp.code, MAX(pp.tier)::int,
		       bool_and(pp.provisional)
		FROM player_patches pp JOIN users u ON u.id = pp.user_id
		WHERE pp.season_id = $1
		GROUP BY pp.user_id, u.login, u.display_name, pp.code`, sn.ID)
	if err != nil {
		return cat, err
	}
	defer rows.Close()
	type holder struct {
		user string
		h    models.PatchHolder
	}
	by := map[string][]holder{}
	for rows.Next() {
		var x holder
		var code string
		if err := rows.Scan(&x.user, &x.h.Login, &x.h.DisplayName, &code, &x.h.Tier, &x.h.Provisional); err != nil {
			return cat, err
		}
		by[code] = append(by[code], x)
	}
	if err := rows.Err(); err != nil {
		return cat, err
	}
	rank := func(u string) int {
		if p, ok := place[u]; ok {
			return p
		}
		return math.MaxInt
	}
	for _, code := range PatchCodes {
		hs := by[code]
		sort.SliceStable(hs, func(i, j int) bool {
			if hs[i].h.Tier != hs[j].h.Tier {
				return hs[i].h.Tier > hs[j].h.Tier
			}
			return rank(hs[i].user) < rank(hs[j].user)
		})
		st := models.PatchStat{Code: code, Holders: len(hs), Top: []models.PatchHolder{}}
		if code == "hunter" {
			st.Tiers = make([]int, len(patchHunterTiers))
		}
		for _, x := range hs {
			st.Top = append(st.Top, x.h)
			if st.Tiers != nil && x.h.Tier >= 1 && x.h.Tier <= len(st.Tiers) {
				st.Tiers[x.h.Tier-1]++
			}
		}
		cat.Patches = append(cat.Patches, st)
	}
	return cat, nil
}

// MatchPatches - нашивки, полученные сторонами в этом матче.
func (s *Store) MatchPatches(ctx context.Context, tournamentID string) ([]models.MatchPatch, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT p.id, pp.code, pp.tier, pp.detail
		FROM player_patches pp
		JOIN participants p ON p.tournament_id = pp.tournament_id AND p.user_id = pp.user_id
		WHERE pp.tournament_id = $1 AND NOT pp.provisional
		ORDER BY p.seed, pp.tier`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.MatchPatch{}
	for rows.Next() {
		var m models.MatchPatch
		if err := rows.Scan(&m.ParticipantID, &m.Code, &m.Tier, &m.Detail); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ParticipantID != out[j].ParticipantID {
			return false
		}
		return patchOrder(out[i].Code) < patchOrder(out[j].Code)
	})
	return out, rows.Err()
}

// SeasonPatchTops - для таблицы лидеров: у каждого игрока до трёх самых редких нашивок сезона и сколько
// их всего. Предварительные не считаются: их ещё нет.
func (s *Store) SeasonPatchTops(ctx context.Context, seasonID string) (map[string][]string, map[string]int, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT user_id, code FROM player_patches WHERE season_id = $1 AND NOT provisional`, seasonID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	holders := map[string]int{}
	byUser := map[string][]string{}
	for rows.Next() {
		var u, code string
		if err := rows.Scan(&u, &code); err != nil {
			return nil, nil, err
		}
		holders[code]++
		byUser[u] = append(byUser[u], code)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	tops, counts := map[string][]string{}, map[string]int{}
	for u, codes := range byUser {
		sort.SliceStable(codes, func(i, j int) bool {
			if holders[codes[i]] != holders[codes[j]] {
				return holders[codes[i]] < holders[codes[j]]
			}
			return patchOrder(codes[i]) < patchOrder(codes[j])
		})
		counts[u] = len(codes)
		tops[u] = codes[:min(3, len(codes))]
	}
	return tops, counts, nil
}
