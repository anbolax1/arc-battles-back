package store

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
)

// recapTTL - сколько живут итоги идущего сезона без проверки базы; завершённый сезон не меняется.
const recapTTL = time.Minute

type recapEntry struct {
	version string
	checked time.Time
	data    []byte
}

type recapCache struct {
	mu    sync.Mutex
	items map[string]recapEntry
}

// SeasonRecapJSON - итоги сезона готовым JSON. Пересчитываются, только когда в сезоне что-то изменилось:
// сыгран или перенесён матч, пересчитан MMR, сезон закрыт.
func (s *Store) SeasonRecapJSON(ctx context.Context, sn models.Season) ([]byte, error) {
	recaps := s.recaps
	recaps.mu.Lock()
	e, ok := recaps.items[sn.ID]
	recaps.mu.Unlock()
	if ok && time.Since(e.checked) < recapTTL {
		return e.data, nil
	}
	ver, err := s.recapVersion(ctx, sn)
	if err != nil {
		return nil, err
	}
	if ok && e.version == ver {
		e.checked = time.Now()
		recaps.mu.Lock()
		recaps.items[sn.ID] = e
		recaps.mu.Unlock()
		return e.data, nil
	}
	in, err := s.loadRecap(ctx, sn)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(buildRecap(in))
	if err != nil {
		return nil, err
	}
	recaps.mu.Lock()
	recaps.items[sn.ID] = recapEntry{version: ver, checked: time.Now(), data: data}
	recaps.mu.Unlock()
	return data, nil
}

// recapVersion - отпечаток данных сезона: меняется при любом матче, пересчёте MMR и закрытии сезона.
func (s *Store) recapVersion(ctx context.Context, sn models.Season) (string, error) {
	var v string
	err := s.Pool.QueryRow(ctx, `
		SELECT concat_ws('|',
			(SELECT status || COALESCE(ended_at::text, '') FROM seasons WHERE id = $1),
			(SELECT count(*) || ':' || COALESCE(max(updated_at)::text, '') FROM tournaments WHERE season_id = $1),
			(SELECT count(*) || ':' || COALESCE(sum(delta), 0) || ':' || COALESCE(sum(mmr_after), 0) FROM mmr_history
			 WHERE season_key = $1 AND mode = '1x1'),
			(SELECT count(*) FROM user_tags))`, sn.ID).Scan(&v)
	return v, err
}

func (s *Store) loadRecap(ctx context.Context, sn models.Season) (recapInput, error) {
	in := recapInput{season: sn, start: sn.StartMmr, logins: map[string]string{}, tags: map[string][]string{}}
	if in.start <= 0 {
		in.start = DefaultSeasonStart
	}
	const seasonMatches = `t.season_id = $1 AND t.status = 'finished' AND t.mode = '1x1'`

	// Матчи и стороны: A - первый посев, B - второй.
	rows, err := s.Pool.Query(ctx, `
		SELECT t.id, COALESCE(t.starts_at, t.created_at), t.format = 'show', t.rating_multiplier, t.games,
		       COALESCE(t.winner_participant_id, ''), p.id, COALESCE(p.user_id, ''), p.total_points
		FROM tournaments t JOIN participants p ON p.tournament_id = t.id
		WHERE `+seasonMatches+`
		ORDER BY COALESCE(t.starts_at, t.created_at), t.created_at, t.id, p.seed, p.id`, sn.ID)
	if err != nil {
		return in, err
	}
	byID := map[string]int{}
	side := map[string][2]int{} // участник -> матч, сторона
	sides := map[string]int{}
	for rows.Next() {
		var id, winner, pid, user string
		var at time.Time
		var show bool
		var mult, games, total int
		if err := rows.Scan(&id, &at, &show, &mult, &games, &winner, &pid, &user, &total); err != nil {
			rows.Close()
			return in, err
		}
		mi, ok := byID[id]
		if !ok {
			mi = len(in.matches)
			byID[id] = mi
			in.matches = append(in.matches, recapRaw{id: id, at: at, show: show, mult: max(mult, 1), games: max(games, 1), winner: -1})
		}
		m := &in.matches[mi]
		s := sides[id]
		if s > 1 {
			continue // в 1×1 сторон две; лишние участники в итоги не идут
		}
		sides[id]++
		m.users[s], m.total[s] = user, total
		side[pid] = [2]int{mi, s}
		if winner == pid {
			m.winner = s
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	// Раунды и очки сторон в них.
	type roundRef struct{ match, round int }
	roundOf := map[string]roundRef{}
	rows, err = s.Pool.Query(ctx, `
		SELECT r.tournament_id, r.id, r.number, COALESCE(r.map_code, ''), r.status <> 'pending',
		       COALESCE(e.participant_id, ''), COALESCE(e.points, 0), COALESCE(e.knocks, 0)
		FROM rounds r JOIN tournaments t ON t.id = r.tournament_id
		LEFT JOIN round_entries e ON e.round_id = r.id
		WHERE `+seasonMatches+`
		ORDER BY r.tournament_id, r.number`, sn.ID)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var tid, rid, code, pid string
		var number, points, knocks int
		var played bool
		if err := rows.Scan(&tid, &rid, &number, &code, &played, &pid, &points, &knocks); err != nil {
			rows.Close()
			return in, err
		}
		mi, ok := byID[tid]
		if !ok {
			continue
		}
		ref, ok := roundOf[rid]
		if !ok {
			ref = roundRef{mi, len(in.matches[mi].rounds)}
			roundOf[rid] = ref
			in.matches[mi].rounds = append(in.matches[mi].rounds, recapRawRound{number: number, mapCode: code, played: played})
		}
		if sd, ok := side[pid]; ok && sd[0] == mi {
			r := &in.matches[mi].rounds[ref.round]
			r.points[sd[1]] += points
			r.knocks[sd[1]] += knocks
			r.entries = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	// Задания: своё выполненное - очки из каталога, выполненное соперником - балл ему.
	rows, err = s.Pool.Query(ctx, `
		SELECT b.round_id, b.participant_id, COALESCE(b.completed_by, ''), c.name, c.text, c.category, c.points,
		       COALESCE(c.map_code, '')
		FROM round_bonus_tasks b JOIN rounds r ON r.id = b.round_id JOIN tournaments t ON t.id = r.tournament_id
		JOIN catalog_tasks c ON c.id = b.task_id
		WHERE `+seasonMatches+`
		ORDER BY b.created_at, b.id`, sn.ID)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var rid, owner, doneBy, name, text, category, code string
		var points int
		if err := rows.Scan(&rid, &owner, &doneBy, &name, &text, &category, &points, &code); err != nil {
			rows.Close()
			return in, err
		}
		ref, ok := roundOf[rid]
		so, okOwner := side[owner]
		if !ok || !okOwner {
			continue
		}
		r := &in.matches[ref.match].rounds[ref.round]
		t := recapRawTask{side: so[1], name: name, text: text, category: category, mapCode: code, doneBy: -1}
		if sd, ok := side[doneBy]; ok {
			t.doneBy = sd[1]
			if sd[1] == so[1] {
				r.points[sd[1]] += points
			} else {
				r.points[sd[1]] += ContractCrossPoints
			}
		}
		r.tasks = append(r.tasks, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	// Легендарные контракты - очки раунда, в котором их выполнили.
	rows, err = s.Pool.Query(ctx, `
		SELECT l.round_id, l.participant_id, lc.points
		FROM legendary_contract_completions l JOIN legendary_contracts lc ON lc.id = l.legendary_contract_id
		JOIN rounds r ON r.id = l.round_id JOIN tournaments t ON t.id = r.tournament_id
		WHERE `+seasonMatches, sn.ID)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var rid, pid string
		var points int
		if err := rows.Scan(&rid, &pid, &points); err != nil {
			rows.Close()
			return in, err
		}
		if ref, ok := roundOf[rid]; ok {
			if sd, ok := side[pid]; ok {
				in.matches[ref.match].rounds[ref.round].points[sd[1]] += points
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	rows, err = s.Pool.Query(ctx, `
		SELECT v.tournament_id, v.action, v.map_code
		FROM match_veto v JOIN tournaments t ON t.id = v.tournament_id
		WHERE `+seasonMatches+`
		ORDER BY v.tournament_id, v.seq`, sn.ID)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var tid, action, code string
		if err := rows.Scan(&tid, &action, &code); err != nil {
			rows.Close()
			return in, err
		}
		if mi, ok := byID[tid]; ok {
			in.matches[mi].veto = append(in.matches[mi].veto, recapRawVeto{action, code})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	rows, err = s.Pool.Query(ctx, `
		SELECT user_id, COALESCE(tournament_id, ''), created_at, delta, mmr_before
		FROM mmr_history WHERE season_key = $1 AND mode = '1x1'
		ORDER BY created_at, tournament_id NULLS LAST, id`, sn.ID)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var h recapHist
		if err := rows.Scan(&h.user, &h.tournament, &h.at, &h.delta, &h.before); err != nil {
			rows.Close()
			return in, err
		}
		in.history = append(in.history, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	var ids []string
	for _, m := range in.matches {
		for _, u := range m.users {
			if u != "" {
				ids = append(ids, u)
			}
		}
	}
	rows, err = s.Pool.Query(ctx, `SELECT id, login FROM users WHERE id = ANY($1)`, ids)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var id, login string
		if err := rows.Scan(&id, &login); err != nil {
			rows.Close()
			return in, err
		}
		in.logins[id] = login
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}
	tags, err := s.TagsForUsers(ctx, ids, true)
	if err != nil {
		return in, err
	}
	for u, ts := range tags {
		for _, t := range ts {
			in.tags[u] = append(in.tags[u], t.Name)
		}
	}

	if in.maps, err = s.ListMaps(ctx); err != nil {
		return in, err
	}

	rows, err = s.Pool.Query(ctx, `
		SELECT id, title, starts_at, format, COALESCE(prize, '') FROM tournaments
		WHERE season_id = $1 AND mode = '1x1' AND status IN ('upcoming', 'live')
		ORDER BY starts_at NULLS LAST`, sn.ID)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var u models.RecapUpcoming
		if err := rows.Scan(&u.ID, &u.Title, &u.At, &u.Format, &u.Prize); err != nil {
			rows.Close()
			return in, err
		}
		in.upcoming = append(in.upcoming, u)
	}
	rows.Close()
	return in, rows.Err()
}
