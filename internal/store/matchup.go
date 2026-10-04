package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// headToHeadShown - сколько последних личных встреч показывать списком.
const headToHeadShown = 5

// Matchup собирает для страницы матча стороны с рейтингом, ставку MMR и личные встречи.
func (s *Store) Matchup(ctx context.Context, tournamentID string) (models.Matchup, error) {
	out := models.Matchup{Sides: []models.MatchupSide{}, HeadToHead: models.HeadToHead{Matches: []models.HeadToHeadMatch{}}}
	t, err := s.GetTournament(ctx, tournamentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	rule, err := s.tournamentSeasonRule(ctx, tournamentID)
	if err != nil {
		return out, err
	}
	// Шоу-матч уходит в зачёт того сезона, который идёт, когда матч начинают.
	if t.Status == "upcoming" {
		rule = s.activeSeasonRule(ctx)
	}
	if rule.Key != "" {
		if sn, err := s.GetSeason(ctx, rule.Key); err == nil {
			out.Season = &models.SeasonRef{ID: sn.ID, Name: sn.Name, KFactor: sn.KFactor}
		}
	}

	parts := append([]models.Participant(nil), t.Participants...)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Seed < parts[j].Seed })
	standing, err := s.seasonStanding(ctx, t, rule)
	if err != nil {
		return out, err
	}
	before := map[string]int{}
	for _, c := range t.MmrChanges {
		before[c.ParticipantID] = c.Before
	}
	for _, p := range parts {
		side := s.matchupSide(ctx, t.Mode, p)
		if t.Status == "finished" {
			side.Mmr = before[p.ID]
		} else if key := sideKey(t.Mode, p); key != "" {
			st, ok := standing[key]
			if !ok {
				st = sideStanding{mmr: rule.Start, isNew: true}
			}
			side.Mmr, side.Place, side.Wins, side.Losses, side.IsNew = st.mmr, st.place, st.wins, st.losses, st.isNew
		}
		out.Sides = append(out.Sides, side)
	}
	if len(out.Sides) != 2 {
		return out, nil
	}

	a, b := &out.Sides[0], &out.Sides[1]
	if a.Mmr > 0 && b.Mmr > 0 {
		a.WinChance = int(math.Round(expectedScore(a.Mmr, b.Mmr) * 100))
		b.WinChance = 100 - a.WinChance
		if t.Status != "finished" {
			mult := max(t.RatingMultiplier, 1)
			a.WinGain = eloWinMagnitude(a.Mmr, b.Mmr, mult, t.Games, rule.K)
			b.WinGain = eloWinMagnitude(b.Mmr, a.Mmr, mult, t.Games, rule.K)
			a.LossDrop, b.LossDrop = b.WinGain, a.WinGain
		}
	}
	out.HeadToHead, err = s.headToHead(ctx, t, parts[0], parts[1])
	return out, err
}

// matchupSide - сторона с игроками: у 2×2 оба из состава, у 1×1 один.
func (s *Store) matchupSide(ctx context.Context, mode string, p models.Participant) models.MatchupSide {
	side := models.MatchupSide{ParticipantID: p.ID, Players: []models.TeamMember{}}
	if mode == "2x2" {
		side.TeamKey = sideKey(mode, p)
	}
	for _, id := range participantUsers(p) {
		if u, err := s.GetUser(ctx, id); err == nil {
			side.Players = append(side.Players, models.TeamMember{
				UserID: u.ID, Login: u.Login, DisplayName: u.DisplayName, AvatarURL: u.AvatarURL,
			})
		}
	}
	return side
}

// sideKey - чей рейтинг у стороны: игрока (1×1) или команды (2×2); пусто - рейтинга нет.
func sideKey(mode string, p models.Participant) string {
	if mode == "2x2" {
		key, _, _, _ := teamKeyFromParticipant(p)
		return key
	}
	if p.UserID == nil {
		return ""
	}
	return *p.UserID
}

type sideStanding struct {
	mmr, place, wins, losses int
	isNew                    bool
}

// seasonStanding - строки таблицы сезона матча по ключу стороны: место - как на странице рейтинга.
func (s *Store) seasonStanding(ctx context.Context, t models.Tournament, rule seasonRule) (map[string]sideStanding, error) {
	out := map[string]sideStanding{}
	if t.Status == "finished" {
		return out, nil
	}
	if t.Mode == "2x2" {
		rows, err := s.TeamLeaderboard(ctx, rule.Key)
		if err != nil {
			return nil, err
		}
		for i, r := range rows {
			out[r.TeamKey] = sideStanding{mmr: r.Mmr, place: i + 1, wins: r.Wins, losses: r.Losses}
		}
		return out, nil
	}
	rows, err := s.Leaderboard(ctx, "1x1", rule.Key)
	if err != nil {
		return nil, err
	}
	for i, r := range rows {
		out[r.UserID] = sideStanding{mmr: r.Mmr, place: i + 1, wins: r.Wins, losses: r.Tournaments - r.Wins}
	}
	return out, nil
}

// headToHead - другие сыгранные матчи тех же сторон: тот же игрок (1×1) или тот же состав (2×2).
func (s *Store) headToHead(ctx context.Context, t models.Tournament, a, b models.Participant) (models.HeadToHead, error) {
	out := models.HeadToHead{Matches: []models.HeadToHeadMatch{}}
	var argA, argB any
	cond := "%s.user_id = $%d"
	if t.Mode == "2x2" {
		ma, okA := membersFilter(a)
		mb, okB := membersFilter(b)
		if !okA || !okB {
			return out, nil
		}
		cond, argA, argB = "%s.members @> $%d::jsonb", ma, mb
	} else {
		ka, kb := sideKey(t.Mode, a), sideKey(t.Mode, b)
		if ka == "" || kb == "" {
			return out, nil
		}
		argA, argB = ka, kb
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT t.id, t.starts_at, t.games,
		       CASE t.winner_participant_id WHEN pa.id THEN 0 WHEN pb.id THEN 1 ELSE -1 END,
		       COALESCE(NULLIF(r.map, ''), t.maps->>0, '')
		FROM tournaments t
		JOIN participants pa ON pa.tournament_id = t.id AND `+fmt.Sprintf(cond, "pa", 2)+`
		JOIN participants pb ON pb.tournament_id = t.id AND pb.id <> pa.id AND `+fmt.Sprintf(cond, "pb", 3)+`
		LEFT JOIN LATERAL (SELECT map FROM rounds WHERE tournament_id = t.id ORDER BY number LIMIT 1) r ON true
		WHERE t.id <> $1 AND t.mode = $4 AND t.status = 'finished'
		ORDER BY COALESCE(t.starts_at, t.created_at) DESC`, t.ID, argA, argB, t.Mode)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var m models.HeadToHeadMatch
		if err := rows.Scan(&m.TournamentID, &m.Date, &m.Games, &m.Winner, &m.Map); err != nil {
			return out, err
		}
		m.Games = max(m.Games, 1)
		if m.Winner < 0 {
			out.Draws += m.Games
		} else {
			out.Wins[m.Winner] += m.Games
		}
		if len(out.Matches) < headToHeadShown {
			out.Matches = append(out.Matches, m)
		}
	}
	return out, rows.Err()
}

// membersFilter - состав 2×2 для поиска по participants.members; у неполного состава встреч не ищем.
func membersFilter(p models.Participant) ([]byte, bool) {
	users := participantUsers(p)
	if len(users) != 2 {
		return nil, false
	}
	raw, err := json.Marshal([]map[string]string{{"userId": users[0]}, {"userId": users[1]}})
	return raw, err == nil
}
