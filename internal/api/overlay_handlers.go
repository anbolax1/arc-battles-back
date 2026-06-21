package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/coder/websocket"
)

// stateEnvelope оборачивает сырое состояние оверлея в конверт для WS/клиента.
func stateEnvelope(stateJSON []byte) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type":  "state",
		"state": json.RawMessage(stateJSON),
	})
}

// overlayStateBytes — что показывать оверлею (источник правды — БД):
//   - нет live-турнира → "{}" (плашка «никто не в эфире»);
//   - есть live-турнир и эфир уже прислал для НЕГО состояние → отдаём пуш эфира (богатый: фокус/раунд/усложнение);
//   - иначе строим табло live-турнира из БД (чтобы оверлей не зависел от того, открыта ли страница эфира).
func (s *Server) overlayStateBytes(ctx context.Context) []byte {
	data, _ := s.Store.GetLiveState(ctx)
	var stored models.LiveState
	_ = json.Unmarshal(data, &stored)

	live, _ := s.Store.ListTournaments(ctx, "live")
	if len(live) == 0 {
		return []byte("{}")
	}
	lt := live[0]
	if stored.Status == "live" && stored.TournamentID != nil && *stored.TournamentID == lt.ID && stored.TournamentName != "" {
		return data
	}
	t, err := s.Store.GetTournament(ctx, lt.ID)
	if err != nil {
		return []byte("{}")
	}
	if b, err := json.Marshal(deriveOverlayState(t)); err == nil {
		return b
	}
	return []byte("{}")
}

// deriveOverlayState строит дефолтное табло оверлея из live-турнира (стороны по seed,
// текущий раунд = live-раунд либо 1, фокус — первая сторона).
func deriveOverlayState(t models.Tournament) models.LiveState {
	parts := append([]models.Participant(nil), t.Participants...)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Seed < parts[j].Seed })

	standings := make([]models.LiveStanding, 0, len(parts))
	for _, p := range parts {
		standings = append(standings, models.LiveStanding{ParticipantID: p.ID, Name: p.Name, Points: p.TotalPoints})
	}
	curRound := 1
	for _, r := range t.Rounds {
		if r.Status == "live" {
			curRound = r.Number
			break
		}
	}
	ls := models.LiveState{
		TournamentID:   &t.ID,
		TournamentName: t.Title,
		Status:         "live",
		Mode:           t.Mode,
		CurrentRound:   curRound,
		TotalRounds:    t.TotalRounds,
		Tasks:          []models.Task{},
		Standings:      standings,
	}
	if len(parts) > 0 {
		ls.CurrentParticipantID = &parts[0].ID
		ls.CurrentName = parts[0].Name
		ls.CurrentPoints = parts[0].TotalPoints
	}
	return ls
}

func (s *Server) handleGetOverlayState(w http.ResponseWriter, r *http.Request) {
	writeRaw(w, http.StatusOK, s.overlayStateBytes(r.Context()))
}

// handleGetOverlayLayout — общая раскладка оверлея для редактора (источник правды — БД).
// В отличие от /overlay/state отдаёт СЫРОЙ сохранённый layout из live_state и не зависит
// от того, есть ли сейчас live-турнир, — чтобы организатор грузил один и тот же оверлей с
// любого устройства. Нет сохранённой раскладки → "{}" (фронт подставит дефолт).
func (s *Server) handleGetOverlayLayout(w http.ResponseWriter, r *http.Request) {
	data, _ := s.Store.GetLiveState(r.Context())
	var stored models.LiveState
	if err := json.Unmarshal(data, &stored); err == nil && stored.Layout != nil {
		writeJSON(w, http.StatusOK, stored.Layout)
		return
	}
	writeRaw(w, http.StatusOK, []byte("{}"))
}

func (s *Server) handlePutOverlayState(w http.ResponseWriter, r *http.Request) {
	var ls models.LiveState
	if err := readJSON(r, &ls); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if ls.Tasks == nil {
		ls.Tasks = []models.Task{}
	}
	norm, err := json.Marshal(ls)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Store.SetLiveState(r.Context(), norm); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if env, err := stateEnvelope(norm); err == nil {
		s.Hub.Broadcast(env)
	}
	writeRaw(w, http.StatusOK, norm)
}

// handleOverlayWS — поток живого состояния для OBS-оверлея.
func (s *Server) handleOverlayWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"}, // OBS browser source присылает разные origin
	})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// CloseRead обрабатывает ping/pong и закрытие; прикладные сообщения от клиента не ждём.
	ctx := c.CloseRead(r.Context())

	client := s.Hub.Register()
	defer s.Hub.Unregister(client)

	// Отправляем актуальное состояние сразу при подключении (с учётом live-турнира из БД).
	if env, err := stateEnvelope(s.overlayStateBytes(r.Context())); err == nil {
		_ = c.Write(ctx, websocket.MessageText, env)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-client.Send:
			if !ok {
				return
			}
			if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
				return
			}
		}
	}
}
