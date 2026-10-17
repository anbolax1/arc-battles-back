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
// presetsRev — счётчик правок пресетов: по его смене страницы /overlay/<slug>
// перечитывают свою раскладку (у них она не из состояния, а из пресета).
func (s *Server) stateEnvelope(stateJSON []byte) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type":       "state",
		"state":      json.RawMessage(stateJSON),
		"presetsRev": s.presetsRev.Load(),
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
		// Итог завершённого матча остаётся на экране, пока не начнётся следующий.
		if stored.Stage == "finished" {
			return data
		}
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
	ls := deriveOverlayState(t)
	ls.Layout = stored.Layout // выбранная сцена, а не раскладка по умолчанию
	if b, err := json.Marshal(ls); err == nil {
		return b
	}
	return []byte("{}")
}

// republishOverlay рассылает текущее состояние оверлея; матч нового пульта сервер пересобирает сам.
func (s *Server) republishOverlay(ctx context.Context) {
	if id, err := s.Store.CurrentMatchID(ctx); err == nil && id != "" {
		if st, err := s.Store.GetMatchState(ctx, id); err == nil && isMatchFlow(st) {
			s.publishMatchState(ctx, st)
			return
		}
	}
	if env, err := s.stateEnvelope(s.overlayStateBytes(ctx)); err == nil {
		s.Hub.Broadcast(env)
	}
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

// handleGetOverlayLayout — раскладка выбранной сцены (её показывает /overlay) с её id в activePresetId.
// В отличие от /overlay/state не зависит от того, есть ли сейчас live-турнир, — редактор с любого
// устройства открывает ту же сцену. Сцена не выбрана → "{}" (фронт подставит дефолт).
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
	// Раскладку задаёт выбранная сцена: копия, присланная пультом, устаревает после первой правки сцены.
	data, _ := s.Store.GetLiveState(r.Context())
	var stored models.LiveState
	_ = json.Unmarshal(data, &stored)
	active := ""
	if stored.Layout != nil {
		active = stored.Layout.ActivePresetID
	}
	lay, err := s.Store.SceneLayout(r.Context(), active)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ls.Layout = lay
	norm, err := json.Marshal(ls)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Store.SetLiveState(r.Context(), norm); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Матч нового пульта сервер пересобирает сам: счёт и задания из присланного не берутся.
	if id, err := s.Store.CurrentMatchID(r.Context()); err == nil && id != "" {
		if st, err := s.Store.GetMatchState(r.Context(), id); err == nil && isMatchFlow(st) {
			s.publishMatchOverlay(r.Context(), id)
			writeRaw(w, http.StatusOK, s.overlayStateBytes(r.Context()))
			return
		}
	}
	if env, err := s.stateEnvelope(norm); err == nil {
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
	if env, err := s.stateEnvelope(s.overlayStateBytes(r.Context())); err == nil {
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
