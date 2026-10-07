package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// AddMatchLog пишет действие в журнал матча вместе с тем, что нужно для его отмены.
func (s *Store) AddMatchLog(ctx context.Context, tournamentID string, roundNumber int, participantID *string, kind, text string, delta int, undo map[string]any) error {
	raw, _ := json.Marshal(undo)
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO match_log (tournament_id, round_number, participant_id, kind, text, delta, undo)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, tournamentID, roundNumber, participantID, kind, text, delta, string(raw))
	return err
}

// ListMatchLog - последние действия журнала, свежие сверху.
func (s *Store) ListMatchLog(ctx context.Context, tournamentID string, limit int) ([]models.MatchLogEntry, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, round_number, participant_id, kind, text, delta, created_at
		FROM match_log WHERE tournament_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, tournamentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.MatchLogEntry{}
	for rows.Next() {
		var e models.MatchLogEntry
		if err := rows.Scan(&e.ID, &e.RoundNumber, &e.ParticipantID, &e.Kind, &e.Text, &e.Delta, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UndoLastMatchLog отменяет последнее действие журнала; отдаёт участников, чьи очки поменялись.
func (s *Store) UndoLastMatchLog(ctx context.Context, tournamentID string) ([]string, error) {
	var id string
	var raw []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT id, undo FROM match_log WHERE tournament_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
		tournamentID).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var u struct {
		Type          string  `json:"type"`
		AssignmentID  string  `json:"assignmentId"`
		Prev          *string `json:"prev"`
		Target        *string `json:"target"`
		RoundID       string  `json:"roundId"`
		ParticipantID string  `json:"participantId"`
		Applied       int     `json:"applied"`
		LegendaryID   string  `json:"legendaryId"`
	}
	_ = json.Unmarshal(raw, &u)

	affected := []string{}
	switch u.Type {
	case "task":
		if err := s.SetTaskCompletedBy(ctx, u.AssignmentID, u.Prev); err != nil {
			return nil, err
		}
		for _, p := range []*string{u.Prev, u.Target} {
			if p != nil {
				affected = append(affected, *p)
			}
		}
	case "points":
		if _, err := s.AdjustRoundPoints(ctx, u.RoundID, u.ParticipantID, -u.Applied); err != nil {
			return nil, err
		}
		affected = append(affected, u.ParticipantID)
	case "knock":
		if _, err := s.AdjustKnocks(ctx, u.RoundID, u.ParticipantID, -u.Applied); err != nil {
			return nil, err
		}
		affected = append(affected, u.ParticipantID)
	case "legendary":
		pid, err := s.UncompleteLegendary(ctx, u.LegendaryID)
		if err != nil {
			return nil, err
		}
		if pid != nil {
			affected = append(affected, *pid)
		}
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM match_log WHERE id = $1`, id); err != nil {
		return nil, err
	}
	return affected, nil
}

// GetMatchState собирает матч целиком: стороны, пики-баны, задания, очки по раундам и журнал.
func (s *Store) GetMatchState(ctx context.Context, tournamentID string) (models.MatchState, error) {
	var st models.MatchState
	t, err := s.GetTournament(ctx, tournamentID)
	if err != nil {
		return st, err
	}
	st.Tournament = t
	st.VetoOrder = VetoOrder(t.TotalRounds)
	if st.Veto, err = s.ListVeto(ctx, tournamentID); err != nil {
		return st, err
	}
	if st.Tasks, err = s.ListTournamentBonusTasks(ctx, tournamentID); err != nil {
		return st, err
	}
	if st.Legendary, err = s.ListMatchLegendary(ctx, tournamentID); err != nil {
		return st, err
	}
	if st.Log, err = s.ListMatchLog(ctx, tournamentID, 50); err != nil {
		return st, err
	}

	// Ручные очки с ноками (и основные задания матчей старого пульта) - по раундам.
	manual := map[int]map[string]int{}
	knocks := map[int]map[string]int{}
	add := func(m map[int]map[string]int, round int, pid string, v int) {
		if m[round] == nil {
			m[round] = map[string]int{}
		}
		m[round][pid] += v
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT r.number, re.participant_id, re.points, re.knocks
		FROM round_entries re JOIN rounds r ON r.id = re.round_id
		WHERE r.tournament_id = $1`, tournamentID)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var n, pts, k int
		var pid string
		if err := rows.Scan(&n, &pid, &pts, &k); err != nil {
			rows.Close()
			return st, err
		}
		add(manual, n, pid, pts)
		add(knocks, n, pid, k)
	}
	rows.Close()
	main := map[int]map[string]int{}
	rows, err = s.Pool.Query(ctx, `
		SELECT r.number, rstd.participant_id, SUM(rstd.times * stt.points)::int
		FROM round_starter_task_done rstd
		JOIN round_starter_tasks rst ON rst.id = rstd.round_starter_task_id
		JOIN starter_tasks stt ON stt.id = rst.starter_task_id
		JOIN rounds r ON r.id = rst.round_id
		WHERE r.tournament_id = $1
		GROUP BY r.number, rstd.participant_id`, tournamentID)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var n, pts int
		var pid string
		if err := rows.Scan(&n, &pid, &pts); err != nil {
			rows.Close()
			return st, err
		}
		add(main, n, pid, pts)
	}
	rows.Close()

	scores := map[int]map[string]int{}
	for _, r := range t.Rounds {
		for _, p := range t.Participants {
			add(scores, r.Number, p.ID, manual[r.Number][p.ID]+main[r.Number][p.ID])
		}
	}
	for _, task := range st.Tasks {
		if task.CompletedBy == nil {
			continue
		}
		if *task.CompletedBy == task.ParticipantID {
			add(scores, task.RoundNumber, task.ParticipantID, task.Points)
		} else {
			add(scores, task.RoundNumber, *task.CompletedBy, ContractCrossPoints)
		}
	}
	for _, l := range st.Legendary {
		if l.ParticipantID != nil {
			add(scores, l.RoundNumber, *l.ParticipantID, l.Points)
		}
	}
	for _, r := range t.Rounds {
		for _, p := range t.Participants {
			st.Scores = append(st.Scores, models.RoundScore{RoundNumber: r.Number, ParticipantID: p.ID, Points: scores[r.Number][p.ID]})
			st.Manual = append(st.Manual, models.RoundScore{RoundNumber: r.Number, ParticipantID: p.ID, Points: manual[r.Number][p.ID]})
			st.Knocks = append(st.Knocks, models.RoundKnocks{RoundNumber: r.Number, ParticipantID: p.ID, Knocks: knocks[r.Number][p.ID]})
		}
	}

	st.Stage = "veto"
	allMaps := len(t.Rounds) > 0
	for _, r := range t.Rounds {
		if r.Map == "" {
			allMaps = false
		}
		if r.Status == "live" {
			st.Stage, st.CurrentRound = "round", r.Number
		}
		if r.Status == "finished" && r.Number > st.CurrentRound {
			st.CurrentRound = r.Number
		}
	}
	switch {
	case t.Status == "finished":
		st.Stage = "finished"
		if st.CurrentRound == 0 && len(t.Rounds) > 0 {
			st.CurrentRound = t.Rounds[len(t.Rounds)-1].Number
		}
	case t.Status == "upcoming":
		st.Stage = "scheduled"
	case st.Stage == "round":
	case allMaps:
		st.Stage = "ready"
	}
	return st, nil
}
