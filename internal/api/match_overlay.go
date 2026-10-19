package api

import (
	"context"
	"encoding/json"
	"log"
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
	s.publishMatchState(ctx, st)
}

func (s *Server) publishMatchState(ctx context.Context, st models.MatchState) {
	ls := buildMatchLiveState(st, s.storedLiveState(ctx))
	s.addStandingRatings(ctx, st, &ls)
	if belt, err := s.Store.MatchBelt(ctx, st); err == nil {
		ls.Belt = belt
	} else {
		log.Printf("пояс для оверлея: %v", err)
	}
	norm, err := json.Marshal(ls)
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

// storedLiveState - состояние оверлея из базы; в нём же сторона в рейде, общая для всех устройств пульта.
func (s *Server) storedLiveState(ctx context.Context) models.LiveState {
	data, _ := s.Store.GetLiveState(ctx)
	var stored models.LiveState
	_ = json.Unmarshal(data, &stored)
	return stored
}

// storeRaidSide запоминает сторону в рейде; оверлей и пульт возьмут её при следующей публикации матча.
func (s *Server) storeRaidSide(ctx context.Context, tournamentID, participantID string) {
	stored := s.storedLiveState(ctx)
	stored.TournamentID = &tournamentID
	stored.CurrentParticipantID = &participantID
	if norm, err := json.Marshal(stored); err == nil {
		_ = s.Store.SetLiveState(ctx, norm)
	}
}

// withRaidSide дописывает в идущий раунд сторону в рейде: по ней пульт подписывает кнопку хода матча.
func (s *Server) withRaidSide(ctx context.Context, st *models.MatchState) {
	if st.Stage != "round" {
		return
	}
	if p := raidSide(sidesBySeed(st.Tournament), st.Tournament.ID, s.storedLiveState(ctx)); p != nil {
		st.Focus = p.ID
	}
}

// addStandingRatings добавляет сторонам табло MMR и место в таблице сезона; у завершённого матча
// MMR берётся из его итога, чтобы рядом показать изменение.
func (s *Server) addStandingRatings(ctx context.Context, st models.MatchState, ls *models.LiveState) {
	users := map[string]string{}
	for _, p := range st.Tournament.Participants {
		if p.UserID != nil {
			users[p.ID] = *p.UserID
		}
	}
	changes := map[string]models.ParticipantMmr{}
	if st.Stage == "finished" {
		for _, c := range st.Tournament.MmrChanges {
			changes[c.ParticipantID] = c
		}
	}
	for i := range ls.Standings {
		sd := &ls.Standings[i]
		uid := users[sd.ParticipantID]
		if uid == "" {
			continue
		}
		if c, ok := changes[sd.ParticipantID]; ok {
			sd.Mmr, sd.MmrDelta = c.After, c.Delta
		} else if mmr, err := s.Store.GetUserMmr(ctx, uid, "1x1"); err == nil {
			sd.Mmr = mmr
		}
		if place, err := s.Store.Player1x1Place(ctx, uid); err == nil {
			sd.Place, sd.IsNew = place, place == 0
		}
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

// sidesBySeed - стороны матча по порядку: A, потом B.
func sidesBySeed(t models.Tournament) []models.Participant {
	parts := append([]models.Participant(nil), t.Participants...)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Seed < parts[j].Seed })
	return parts
}

// raidSide - сторона в рейде: выбранная в пульте, а пока не выбрана - сторона A.
func raidSide(parts []models.Participant, tournamentID string, stored models.LiveState) *models.Participant {
	if stored.TournamentID != nil && *stored.TournamentID == tournamentID && stored.CurrentParticipantID != nil {
		for i := range parts {
			if parts[i].ID == *stored.CurrentParticipantID {
				return &parts[i]
			}
		}
	}
	if len(parts) > 0 {
		return &parts[0]
	}
	return nil
}

func buildMatchLiveState(st models.MatchState, stored models.LiveState) models.LiveState {
	t := st.Tournament
	parts := sidesBySeed(t)
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

	focus := raidSide(parts, t.ID, stored)

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
	if stored.TournamentID != nil && *stored.TournamentID == t.ID {
		ls.PatchFlashes = stored.PatchFlashes
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
			// Протокол выполняет только его владелец, поэтому оверлей показывает протокол стороны в рейде.
			if task.ParticipantID != focus.ID {
				continue
			}
			c := models.LiveComplication{
				Who: nameOf[task.ParticipantID], Text: overlayTaskText(task), ValueType: models.ValueFixed,
				Reward: task.Points, Done: done, Tag: task.Tag,
			}
			ls.Complications = append(ls.Complications, c)
			ls.Complication = &c
			continue
		}
		b := models.LiveBonus{
			Text: overlayTaskText(task), ValueType: models.ValueFixed, Who: nameOf[task.ParticipantID], Category: "task", Tag: task.Tag,
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
