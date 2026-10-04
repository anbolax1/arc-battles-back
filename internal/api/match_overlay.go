package api

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/battle-for-respect/backend/internal/models"
)

// publishMatchOverlay считает для оверлея счёт и задания, а раскладку и сторону «в рейде» берёт
// из сохранённого состояния - оверлей не зависит от того, с какого устройства ведут пульт.
func (s *Server) publishMatchOverlay(ctx context.Context, tournamentID string) {
	st, err := s.Store.GetMatchState(ctx, tournamentID)
	if err != nil {
		return
	}
	data, _ := s.Store.GetLiveState(ctx)
	var stored models.LiveState
	_ = json.Unmarshal(data, &stored)
	norm, err := json.Marshal(buildMatchLiveState(st, stored))
	if err != nil {
		return
	}
	if err := s.Store.SetLiveState(ctx, norm); err != nil {
		return
	}
	if env, err := s.stateEnvelope(norm); err == nil {
		s.Hub.Broadcast(env)
	}
}

// isMatchFlow - матч ведётся новым пультом (два раунда или пики-баны), его оверлей считает сервер.
func isMatchFlow(st models.MatchState) bool {
	return st.Tournament.TotalRounds > 1 || len(st.Veto) > 0
}

func overlayTaskText(t models.RoundBonusTask) string {
	if t.MapCode == "" && t.Name != "" {
		return t.Name + ": " + t.Text
	}
	return t.Text
}

func buildMatchLiveState(st models.MatchState, stored models.LiveState) models.LiveState {
	t := st.Tournament
	parts := append([]models.Participant(nil), t.Participants...)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Seed < parts[j].Seed })
	sides := map[string]string{}
	for i, p := range parts {
		if i == 0 {
			sides["A"] = p.Name
		} else if i == 1 {
			sides["B"] = p.Name
		}
	}

	round := st.CurrentRound
	if round == 0 {
		round = 1
	}
	total := map[string]int{}
	inRound := map[string]int{}
	for _, sc := range st.Scores {
		total[sc.ParticipantID] += sc.Points
		if sc.RoundNumber == round {
			inRound[sc.ParticipantID] += sc.Points
		}
	}

	var focus *models.Participant
	if stored.TournamentID != nil && *stored.TournamentID == t.ID && stored.CurrentParticipantID != nil {
		for i := range parts {
			if parts[i].ID == *stored.CurrentParticipantID {
				focus = &parts[i]
			}
		}
	}
	if focus == nil && len(parts) > 0 {
		focus = &parts[0]
	}

	ls := models.LiveState{
		TournamentID:   &t.ID,
		TournamentName: t.Title,
		Status:         "live",
		Mode:           t.Mode,
		CurrentRound:   round,
		TotalRounds:    len(t.Rounds),
		Tasks:          []models.Task{},
		Stage:          st.Stage,
		ShowStandings:  stored.ShowStandings,
		Layout:         stored.Layout,
	}
	for _, r := range t.Rounds {
		if r.Number == round {
			ls.CurrentMap = r.Map
		}
	}
	for _, p := range parts {
		ls.Standings = append(ls.Standings, models.LiveStanding{ParticipantID: p.ID, Name: p.Name, Points: total[p.ID], RoundPoints: inRound[p.ID]})
	}
	for _, v := range st.Veto {
		lv := models.LiveVeto{MapCode: v.MapCode, MapName: v.MapName, Action: v.Action, Side: v.Side, SideName: sides[v.Side]}
		if v.RoundNumber != nil {
			lv.Round = *v.RoundNumber
		}
		ls.Veto = append(ls.Veto, lv)
	}
	if focus == nil {
		return ls
	}
	ls.CurrentParticipantID = &focus.ID
	ls.CurrentName = focus.Name
	ls.CurrentPoints = total[focus.ID]

	nameOf := map[string]string{}
	for _, p := range parts {
		nameOf[p.ID] = p.Name
	}
	for _, task := range st.Tasks {
		if task.RoundNumber != round {
			continue
		}
		done := task.CompletedBy != nil && *task.CompletedBy == task.ParticipantID
		if task.Category == "protocol" {
			c := models.LiveComplication{
				Who: nameOf[task.ParticipantID], Text: overlayTaskText(task), ValueType: models.ValueFixed,
				Reward: task.Points, Done: done,
			}
			ls.Complications = append(ls.Complications, c)
			if task.ParticipantID == focus.ID {
				cc := c
				ls.Complication = &cc
			}
			continue
		}
		b := models.LiveBonus{
			Text: overlayTaskText(task), ValueType: models.ValueFixed, Who: nameOf[task.ParticipantID], Category: "task",
		}
		if task.MapCode != "" {
			b.Category = "map"
		}
		if task.ParticipantID == focus.ID {
			b.Points = task.Points
			if done {
				b.Times = 1
			}
		} else {
			b.Points = 1
			b.Opponent = true
			if task.CompletedBy != nil && *task.CompletedBy == focus.ID {
				b.Times = 1
			}
		}
		ls.BonusTasks = append(ls.BonusTasks, b)
	}
	return ls
}
