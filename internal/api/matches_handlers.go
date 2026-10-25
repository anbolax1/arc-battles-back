package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/battle-for-respect/backend/internal/media"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// GET /api/maps - справочник карт с превью.
func (s *Server) handleListMaps(w http.ResponseWriter, r *http.Request) {
	maps, err := s.Store.ListMaps(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, maps)
}

// GET /api/matches/current - идущий матч для главной; если никто не играет - последний сыгранный.
func (s *Server) handleCurrentMatch(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"current": nil, "last": nil}
	if id, err := s.Store.CurrentMatchID(r.Context()); err == nil && id != "" {
		if st, err := s.Store.GetMatchState(r.Context(), id); err == nil {
			s.withRaidSide(r.Context(), &st)
			s.withChance(r.Context(), &st)
			out["current"] = st
		}
	} else if id, err := s.Store.LastFinishedMatchID(r.Context()); err == nil && id != "" {
		if st, err := s.Store.GetMatchState(r.Context(), id); err == nil {
			s.withChance(r.Context(), &st)
			out["last"] = st
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/tournaments/{id}/match - матч целиком (публично: задания на эфире и так видны зрителям).
func (s *Server) handleGetMatch(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GetMatchState(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "матч не найден")
		return
	}
	s.withRaidSide(r.Context(), &st)
	s.withChance(r.Context(), &st)
	writeJSON(w, http.StatusOK, st)
}

// GET /api/tournaments/{id}/chance - как менялся шанс по ходу матча: для графика на странице матча.
func (s *Server) handleMatchChance(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GetMatchState(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "матч не найден")
		return
	}
	s.withRaidSide(r.Context(), &st)
	trail, err := s.Store.MatchChanceTrail(r.Context(), st, st.Focus)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if trail == nil {
		trail = &models.ChanceTrail{Points: []models.ChancePoint{}}
	}
	writeJSON(w, http.StatusOK, trail)
}

// withChance дописывает шансы на победу: перед матчем и сейчас.
func (s *Server) withChance(ctx context.Context, st *models.MatchState) {
	mc, err := s.Store.MatchChance(ctx, *st, st.Focus)
	if err != nil {
		log.Printf("шанс матча: %v", err)
		return
	}
	st.Chance = mc
}

// GET /api/tournaments/{id}/matchup - стороны для страницы матча: рейтинг, что на кону, личные встречи.
func (s *Server) handleMatchup(w http.ResponseWriter, r *http.Request) {
	m, err := s.Store.Matchup(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "матч не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ids := []string{}
	for _, sd := range m.Sides {
		for _, p := range sd.Players {
			ids = append(ids, p.UserID)
		}
	}
	tags := s.siteTags(r.Context(), ids)
	for i := range m.Sides {
		for j := range m.Sides[i].Players {
			m.Sides[i].Players[j].Tags = tags[m.Sides[i].Players[j].UserID]
		}
	}
	writeJSON(w, http.StatusOK, m)
}

// writeMatch отвечает свежим состоянием матча и обновляет оверлей. Запланированный шоу-матч оверлей
// не трогает: там остаётся текущий матч. Завершённый - только если оверлей показывает его же: правка
// старого матча не должна подменить собой эфир.
func (s *Server) writeMatch(w http.ResponseWriter, r *http.Request, tournamentID string, status int) {
	st, err := s.Store.GetMatchState(r.Context(), tournamentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "матч не найден")
		return
	}
	if st.Stage != "scheduled" && (st.Stage != "finished" || s.overlayShows(r.Context(), tournamentID)) {
		s.publishMatchState(r.Context(), st)
	}
	s.withRaidSide(r.Context(), &st)
	s.withChance(r.Context(), &st)
	writeJSON(w, status, st)
}

// writeLiveConflict - другой матч уже в эфире: в ответе его id, чтобы пульт дал на него ссылку.
func (s *Server) writeLiveConflict(w http.ResponseWriter, r *http.Request) {
	cur, _ := s.Store.CurrentMatchID(r.Context())
	writeJSON(w, http.StatusConflict, map[string]string{
		"error":   "уже идёт другой матч — завершите или отмените его",
		"matchId": cur,
	})
}

// matchGuard не даёт править завершённый матч.
func (s *Server) matchGuard(w http.ResponseWriter, r *http.Request, tournamentID string) bool {
	st, err := s.Store.TournamentStatus(r.Context(), tournamentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "матч не найден")
		return false
	}
	return !s.blockIfFinished(w, st)
}

// matchFinished - завершён ли матч: счёт завершённого можно исправить, но тогда итог подводится заново.
func (s *Server) matchFinished(w http.ResponseWriter, r *http.Request, tournamentID string) (finished, ok bool) {
	st, err := s.Store.TournamentStatus(r.Context(), tournamentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "матч не найден")
		return false, false
	}
	return st == "finished", true
}

// rescore заново подводит итог завершённого матча после правки счёта: победитель, MMR и нашивки.
func (s *Server) rescore(w http.ResponseWriter, r *http.Request, tournamentID string) bool {
	recomputed, err := s.Store.RescoreFinishedMatch(r.Context(), tournamentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	// Пересчёт MMR сам пересчитывает и нашивки.
	if !recomputed {
		s.syncPatches(r.Context())
	}
	return true
}

// GET /api/match-players - игроки для выбора сторон (MMR и счёт текущего сезона).
func (s *Server) handleListMatchPlayers(w http.ResponseWriter, r *http.Request) {
	players, err := s.Store.ListMatchPlayers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, players)
}

// POST /api/players/placeholder {nickname} - игрок по нику прямо из формы матча.
func (s *Server) handleCreatePlaceholder(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Nickname string `json:"nickname"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	nick := strings.Join(strings.Fields(b.Nickname), " ")
	if n := utf8.RuneCountInString(nick); n < 2 || n > 32 {
		writeError(w, http.StatusBadRequest, "ник — от 2 до 32 символов")
		return
	}
	u, created, err := s.Store.CreatePlaceholderUser(r.Context(), nick)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, u)
}

// POST /api/matches - новый матч; обычный сразу становится текущим (rounds: 2 или 3), шоу-матч уходит
// в расписание и всегда идёт три раунда.
func (s *Server) handleCreateMatch(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Mode             string     `json:"mode"`
		PlayerType       string     `json:"playerType"`
		RatingMultiplier int        `json:"ratingMultiplier"`
		Format           string     `json:"format"`
		Rounds           int        `json:"rounds"`
		StartsAt         *time.Time `json:"startsAt"`
		Prize            string     `json:"prize"`
		Sides            []struct {
			UserID  string `json:"userId"`
			TeamKey string `json:"teamKey"`
		} `json:"sides"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if len(b.Sides) != 2 {
		writeError(w, http.StatusBadRequest, "нужны две стороны")
		return
	}
	if b.Format == store.FormatShow && b.StartsAt == nil {
		writeError(w, http.StatusBadRequest, "укажите дату и время шоу-матча")
		return
	}
	prize, ok := checkPrize(w, b.Prize)
	if !ok {
		return
	}
	in := store.NewMatch{
		Mode: b.Mode, PlayerType: b.PlayerType, RatingMultiplier: b.RatingMultiplier, Format: b.Format, Rounds: b.Rounds, StartsAt: b.StartsAt,
		Prize: prize,
	}
	teams := b.Mode == "2x2"
	for i, sd := range b.Sides {
		side := store.MatchSide{UserID: strings.TrimSpace(sd.UserID), TeamKey: strings.TrimSpace(sd.TeamKey)}
		if teams && side.TeamKey == "" {
			writeError(w, http.StatusBadRequest, "выберите команды обеих сторон")
			return
		}
		if !teams && side.UserID == "" {
			writeError(w, http.StatusBadRequest, "выберите игроков обеих сторон")
			return
		}
		in.Sides[i] = side
	}
	t, err := s.Store.CreateMatch(r.Context(), in)
	if errors.Is(err, store.ErrLiveMatchExists) {
		s.writeLiveConflict(w, r)
		return
	}
	if errors.Is(err, store.ErrConflict) {
		msg := "стороны должны быть разными игроками"
		if teams {
			msg = "у команд не должно быть общих игроков"
		}
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		msg := "игрок не найден"
		if teams {
			msg = "команда не найдена"
		}
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeMatch(w, r, t.ID, http.StatusCreated)
}

// POST /api/tournaments/{id}/veto {mapCode} - следующий ход пиков-банов.
func (s *Server) handleVeto(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.matchGuard(w, r, id) {
		return
	}
	var b struct {
		MapCode string `json:"mapCode"`
	}
	if err := readJSON(r, &b); err != nil || b.MapCode == "" {
		writeError(w, http.StatusBadRequest, "укажите карту")
		return
	}
	if err := s.Store.VetoMap(r.Context(), id, b.MapCode); err != nil {
		writeVetoError(w, err)
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// POST /api/tournaments/{id}/veto/undo - отменить последний ход пиков-банов.
func (s *Server) handleVetoUndo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.matchGuard(w, r, id) {
		return
	}
	if err := s.Store.VetoUndo(r.Context(), id); err != nil {
		writeVetoError(w, err)
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// POST /api/tournaments/{id}/maps {maps: [code, ...]} - карты раундов вручную, если пики-баны прошли вне эфира.
func (s *Server) handleSetMatchMaps(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.matchGuard(w, r, id) {
		return
	}
	var b struct {
		Maps []string `json:"maps"`
	}
	if err := readJSON(r, &b); err != nil || len(b.Maps) == 0 || len(b.Maps) > 3 {
		writeError(w, http.StatusBadRequest, "укажите карты раундов")
		return
	}
	if err := s.Store.SetMatchMaps(r.Context(), id, b.Maps); err != nil {
		writeVetoError(w, err)
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// POST /api/tournaments/{id}/first {participantId} - кто первым ходит в пиках-банах: ведущий ставит
// сторону по жребию на эфире.
func (s *Server) handleFirstSide(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.matchGuard(w, r, id) {
		return
	}
	var b struct {
		ParticipantID string `json:"participantId"`
	}
	if err := readJSON(r, &b); err != nil || b.ParticipantID == "" {
		writeError(w, http.StatusBadRequest, "укажите сторону")
		return
	}
	switch err := s.Store.SetFirstSide(r.Context(), id, b.ParticipantID); {
	case errors.Is(err, store.ErrMatchStarted):
		writeError(w, http.StatusConflict, "раунд уже начался — очерёдность не меняется")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusBadRequest, "сторона не из этого матча")
		return
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "в матче не две стороны")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

func writeVetoError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrMatchStarted):
		writeError(w, http.StatusConflict, "раунд уже начался — карты не меняются")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusBadRequest, "нет такой карты")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "эта карта уже выбрана или пики-баны закончены")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// POST /api/tournaments/{id}/rounds/next - начать первый раунд или перейти к следующему.
func (s *Server) handleNextRound(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.matchGuard(w, r, id) {
		return
	}
	if _, err := s.Store.StartNextRound(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, store.ErrNoMap):
			writeError(w, http.StatusConflict, "у раунда нет карты — сначала пики-баны")
		case errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, "это последний раунд — завершите матч")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	// Рейды в раунде идут по очереди, первой - сторона A: оверлей переключается на неё.
	if t, err := s.Store.GetTournament(r.Context(), id); err == nil {
		if parts := sidesBySeed(t); len(parts) > 0 {
			s.storeRaidSide(r.Context(), id, parts[0].ID)
			if rd, ok := s.liveRound(r, id); ok {
				s.recordRaidStart(r.Context(), id, rd.Number, parts[0].ID)
			}
		}
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// POST /api/tournaments/{id}/start - вывести запланированный шоу-матч в эфир: он становится текущим.
func (s *Server) handleStartMatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.matchGuard(w, r, id) {
		return
	}
	switch err := s.Store.StartShowMatch(r.Context(), id); {
	case errors.Is(err, store.ErrLiveMatchExists):
		s.writeLiveConflict(w, r)
		return
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "матч уже начался")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// POST /api/tournaments/{id}/schedule {startsAt} - перенести шоу-матч, пока он не начался.
func (s *Server) handleRescheduleMatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var b struct {
		StartsAt *time.Time `json:"startsAt"`
	}
	if err := readJSON(r, &b); err != nil || b.StartsAt == nil {
		writeError(w, http.StatusBadRequest, "укажите дату и время")
		return
	}
	switch err := s.Store.RescheduleShowMatch(r.Context(), id, *b.StartsAt); {
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "перенести можно только матч, который ещё не начался")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// maxPrize - приз стоит крупно в анонсе, поэтому строка короткая.
const maxPrize = 120

// showPreviewDir - папка картинок-превью шоу-матчей в хранилище медиа.
const showPreviewDir = "shows"

func checkPrize(w http.ResponseWriter, raw string) (string, bool) {
	prize := strings.TrimSpace(raw)
	if utf8.RuneCountInString(prize) > maxPrize {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("приз — до %d символов", maxPrize))
		return "", false
	}
	return prize, true
}

// POST /api/tournaments/{id}/prize {prize} - приз шоу-матча; пусто - без приза.
func (s *Server) handleSetPrize(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var b struct {
		Prize string `json:"prize"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	prize, ok := checkPrize(w, b.Prize)
	if !ok {
		return
	}
	switch err := s.Store.SetShowPrize(r.Context(), id, prize); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "матч не найден")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// PUT /api/tournaments/{id}/preview - картинка-превью шоу-матча: файл в теле запроса либо {url} -
// тогда сервер скачивает картинку к себе.
func (s *Server) handleSetPreview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.Store.TournamentStatus(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "матч не найден")
		return
	}
	// Новое имя у каждой картинки: браузер не покажет прежнюю из кэша.
	name := fmt.Sprintf("%s-%d", id, time.Now().UnixNano())
	var rel string
	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var b struct {
			URL string `json:"url"`
		}
		if err := readJSON(r, &b); err != nil {
			writeError(w, http.StatusBadRequest, "некорректный JSON")
			return
		}
		rel, err = s.Media.FetchImage(r.Context(), b.URL, showPreviewDir, name)
	} else {
		rel, err = s.Media.SaveImage(showPreviewDir, name, http.MaxBytesReader(w, r.Body, media.MaxImageBytes+1))
	}
	if err != nil {
		writePreviewError(w, err)
		return
	}
	old := s.Store.ShowPreviewPath(r.Context(), id)
	if err := s.Store.SetShowPreview(r.Context(), id, rel); err != nil {
		s.Media.Remove(rel)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Media.Remove(old)
	s.writeMatch(w, r, id, http.StatusOK)
}

// DELETE /api/tournaments/{id}/preview - убрать картинку-превью.
func (s *Server) handleDeletePreview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	old := s.Store.ShowPreviewPath(r.Context(), id)
	switch err := s.Store.SetShowPreview(r.Context(), id, ""); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "матч не найден")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Media.Remove(old)
	s.writeMatch(w, r, id, http.StatusOK)
}

func writePreviewError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	switch {
	case errors.Is(err, media.ErrImageTooLarge), errors.As(err, &tooBig):
		writeError(w, http.StatusBadRequest, media.ErrImageTooLarge.Error())
	case errors.Is(err, media.ErrNotImage), errors.Is(err, media.ErrBadImageURL), errors.Is(err, media.ErrImageFetch):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "не удалось сохранить картинку")
	}
}

// POST /api/tournaments/{id}/finish - завершить матч (досрочно - тоже): победитель и MMR сразу.
func (s *Server) handleFinishMatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.matchGuard(w, r, id) {
		return
	}
	if _, err := s.Store.FinishMatch(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.flashPatches(r.Context(), id, s.syncPatches(r.Context()))
	s.writeMatch(w, r, id, http.StatusOK)
}

// POST /api/tournaments/{id}/cancel - отменить матч: он удаляется, рейтинг не меняется.
func (s *Server) handleCancelMatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.matchGuard(w, r, id) {
		return
	}
	preview := s.Store.ShowPreviewPath(r.Context(), id)
	if err := s.Store.DeleteTournament(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Media.Remove(preview)
	s.syncPatches(r.Context())
	if env, err := s.stateEnvelope(s.overlayStateBytes(r.Context())); err == nil {
		s.Hub.Broadcast(env)
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxVoidReason - причина стоит на странице матча одной строкой.
const maxVoidReason = 120

// POST /api/tournaments/{id}/void {voided, reason} - не засчитать исход завершённого матча (проигравшему
// возвращается MMR) или засчитать его снова.
func (s *Server) handleVoidMatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var b struct {
		Voided bool   `json:"voided"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	reason := strings.TrimSpace(b.Reason)
	if utf8.RuneCountInString(reason) > maxVoidReason {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("причина — до %d символов", maxVoidReason))
		return
	}
	t, err := s.Store.GetTournament(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "матч не найден")
		return
	}
	if t.Status != "finished" {
		writeError(w, http.StatusConflict, "матч ещё не завершён")
		return
	}
	if b.Voided && (t.WinnerParticipantID == nil || *t.WinnerParticipantID == "") {
		writeError(w, http.StatusConflict, "в ничьей MMR не менялся — возвращать нечего")
		return
	}
	if err := s.Store.SetMatchVoided(r.Context(), id, b.Voided, reason); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// POST /api/tournaments/{id}/focus {participantId} - чья сторона сейчас в рейде (только для оверлея).
func (s *Server) handleMatchFocus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var b struct {
		ParticipantID string `json:"participantId"`
	}
	if err := readJSON(r, &b); err != nil || b.ParticipantID == "" {
		writeError(w, http.StatusBadRequest, "укажите сторону")
		return
	}
	p, err := s.Store.GetParticipant(r.Context(), b.ParticipantID)
	if err != nil || p.TournamentID != id {
		writeError(w, http.StatusBadRequest, "сторона не из этого матча")
		return
	}
	prev := s.storedLiveState(r.Context())
	s.storeRaidSide(r.Context(), id, p.ID)
	// Сторона в рейде сменилась - начался её рейд; повторное нажатие на ту же сторону рейд не начинает.
	if prev.TournamentID == nil || *prev.TournamentID != id || prev.CurrentParticipantID == nil || *prev.CurrentParticipantID != p.ID {
		if rd, ok := s.liveRound(r, id); ok {
			s.recordRaidStart(r.Context(), id, rd.Number, p.ID)
		}
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

func (s *Server) recordRaidStart(ctx context.Context, tournamentID string, round int, participantID string) {
	if err := s.Store.RecordRaidStart(ctx, tournamentID, round, participantID); err != nil {
		log.Printf("начало рейда: %v", err)
	}
}

// teamPlayer - игрок команды 2×2, которому идёт зачёт: у команды он обязателен и должен быть из её состава, у
// стороны из одного игрока не нужен (пусто). label - чей зачёт в журнале: «Команда · Ник» или ник игрока.
func (s *Server) teamPlayer(w http.ResponseWriter, r *http.Request, participantID, userID string) (player, label string, ok bool) {
	p, err := s.Store.GetParticipant(r.Context(), participantID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "сторона не из этого матча")
		return "", "", false
	}
	if p.Kind != "team" {
		return "", p.Name, true
	}
	if !store.IsTeamMember(p, userID) {
		writeError(w, http.StatusBadRequest, "укажите игрока команды")
		return "", "", false
	}
	u, err := s.Store.GetUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "игрок не найден")
		return "", "", false
	}
	nick := u.DisplayName
	if strings.TrimSpace(nick) == "" {
		nick = u.Login
	}
	return u.ID, p.Name + " · " + nick, true
}

// POST /api/round-bonus-tasks/{id}/mark {by: owner|opponent|none, userId} - зачёт задания в пульте; задание команды
// 2×2 засчитывается её игроку userId - тому, кто его выполнил.
func (s *Server) handleMarkTask(w http.ResponseWriter, r *http.Request) {
	asgID := chi.URLParam(r, "id")
	task, tid, err := s.Store.GetBonusAssignment(r.Context(), asgID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "задание не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	finished, ok := s.matchFinished(w, r, tid)
	if !ok {
		return
	}
	var b struct {
		By     string `json:"by"`
		UserID string `json:"userId"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	names := s.participantNames(r, tid)
	player, owner := "", names[task.ParticipantID]
	if b.By == "owner" {
		if player, owner, ok = s.teamPlayer(w, r, task.ParticipantID, strings.TrimSpace(b.UserID)); !ok {
			return
		}
	}
	res, err := s.Store.MarkTask(r.Context(), asgID, b.By, player)
	if errors.Is(err, store.ErrTaskTaken) {
		writeError(w, http.StatusConflict, "задание уже засчитано другому игроку команды — сначала отмените зачёт")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "это задание противнику не засчитать")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recomputeMany(r, res.Affected)

	title := task.Name
	if title == "" {
		title = task.Text
	}
	var text string
	var delta int
	var who *string
	switch {
	case res.Target == nil:
		text = fmt.Sprintf("Зачёт снят: «%s»", title)
	case *res.Target == task.ParticipantID:
		text, delta, who = fmt.Sprintf("%s: «%s»", owner, title), task.Points, res.Target
	default:
		text, delta, who = fmt.Sprintf("%s: задание соперника «%s»", names[*res.Target], title), store.ContractCrossPoints, res.Target
	}
	_ = s.Store.AddMatchLog(r.Context(), tid, task.RoundNumber, who, "task", text, delta, map[string]any{
		"type": "task", "assignmentId": asgID, "prev": res.Prev, "prevUser": res.PrevUser, "target": res.Target,
	})
	if finished && !s.rescore(w, r, tid) {
		return
	}
	s.writeMatch(w, r, tid, http.StatusOK)
}

// POST /api/round-bonus-tasks/{id}/reroll - заменить невыполненное задание другим того же вида.
func (s *Server) handleRerollTask(w http.ResponseWriter, r *http.Request) {
	asgID := chi.URLParam(r, "id")
	_, tid, err := s.Store.GetBonusAssignment(r.Context(), asgID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "задание не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.matchGuard(w, r, tid) {
		return
	}
	if err := s.Store.RerollTask(r.Context(), asgID); err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, "выполненное задание не перебрасывается")
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusConflict, "замены нет — все задания этого вида уже выпадали")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	s.writeMatch(w, r, tid, http.StatusOK)
}

// liveRound - идущий раунд матча (ручные очки и легендарки идут в него).
func (s *Server) liveRound(r *http.Request, tournamentID string) (models.Round, bool) {
	rounds, err := s.Store.ListRounds(r.Context(), tournamentID)
	if err != nil {
		return models.Round{}, false
	}
	for _, rd := range rounds {
		if rd.Status == "live" {
			return rd, true
		}
	}
	return models.Round{}, false
}

func (s *Server) participantNames(r *http.Request, tournamentID string) map[string]string {
	out := map[string]string{}
	if parts, err := s.Store.ListParticipants(r.Context(), tournamentID); err == nil {
		for _, p := range parts {
			out[p.ID] = p.Name
		}
	}
	return out
}

// scoreRound - раунд, в который идёт правка счёта: в идущем матче - текущий, в завершённом - сыгранный
// раунд с номером number (исправление итога после эфира).
func (s *Server) scoreRound(w http.ResponseWriter, r *http.Request, tournamentID string, number int) (rd models.Round, finished, ok bool) {
	if finished, ok = s.matchFinished(w, r, tournamentID); !ok {
		return rd, false, false
	}
	if !finished {
		if rd, ok = s.liveRound(r, tournamentID); !ok {
			writeError(w, http.StatusConflict, "раунд не идёт")
		}
		return rd, false, ok
	}
	rounds, err := s.Store.ListRounds(r.Context(), tournamentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return rd, true, false
	}
	for _, x := range rounds {
		if x.Number == number && x.Status == "finished" {
			return x, true, true
		}
	}
	writeError(w, http.StatusConflict, "этот раунд не сыгран")
	return rd, true, false
}

// scoreSide - раунд правки и чей зачёт: сторона матча, а у команды 2×2 - ещё и её игрок; если раунда нет, сторона
// чужая или игрок не из команды, отвечает ошибкой.
func (s *Server) scoreSide(w http.ResponseWriter, r *http.Request, tournamentID, participantID, userID string, number int) (rd models.Round, player, who string, finished, ok bool) {
	if rd, finished, ok = s.scoreRound(w, r, tournamentID, number); !ok {
		return rd, "", "", finished, false
	}
	if _, in := s.participantNames(r, tournamentID)[participantID]; !in {
		writeError(w, http.StatusBadRequest, "сторона не из этого матча")
		return rd, "", "", finished, false
	}
	player, who, ok = s.teamPlayer(w, r, participantID, strings.TrimSpace(userID))
	return rd, player, who, finished, ok
}

// POST /api/tournaments/{id}/points {participantId, userId, delta, label, round} - ручные очки за идущий раунд, у
// команды 2×2 - её игроку userId; round - номер сыгранного раунда, когда исправляют завершённый матч.
func (s *Server) handleMatchPoints(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var b struct {
		ParticipantID string `json:"participantId"`
		UserID        string `json:"userId"`
		Delta         int    `json:"delta"`
		Label         string `json:"label"`
		Round         int    `json:"round"`
	}
	if err := readJSON(r, &b); err != nil || b.ParticipantID == "" || b.Delta == 0 || b.Delta > 50 || b.Delta < -50 {
		writeError(w, http.StatusBadRequest, "укажите сторону и очки")
		return
	}
	rd, player, who, finished, ok := s.scoreSide(w, r, id, b.ParticipantID, b.UserID, b.Round)
	if !ok {
		return
	}
	var applied int
	var err error
	if player != "" {
		applied, err = s.Store.AdjustMemberPoints(r.Context(), rd.ID, b.ParticipantID, player, b.Delta)
	} else {
		applied, err = s.Store.AdjustRoundPoints(r.Context(), rd.ID, b.ParticipantID, b.Delta)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if applied != 0 {
		s.recomputeMany(r, []string{b.ParticipantID})
		label := strings.TrimSpace(b.Label)
		if label == "" {
			label = "ручные очки"
		}
		pid := b.ParticipantID
		_ = s.Store.AddMatchLog(r.Context(), id, rd.Number, &pid, "points", who+": "+label, applied, map[string]any{
			"type": "points", "roundId": rd.ID, "participantId": pid, "userId": player, "applied": applied,
		})
		if finished && !s.rescore(w, r, id) {
			return
		}
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// POST /api/tournaments/{id}/knocks {participantId, userId, delta: 1|-1, round} - нок стороне в идущем раунде (у
// команды 2×2 - её игроку userId) или снять ошибочный; round - номер сыгранного раунда, когда исправляют
// завершённый матч.
func (s *Server) handleMatchKnock(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var b struct {
		ParticipantID string `json:"participantId"`
		UserID        string `json:"userId"`
		Delta         int    `json:"delta"`
		Round         int    `json:"round"`
	}
	if err := readJSON(r, &b); err != nil || b.ParticipantID == "" || (b.Delta != 1 && b.Delta != -1) {
		writeError(w, http.StatusBadRequest, "укажите сторону и нок")
		return
	}
	rd, player, who, finished, ok := s.scoreSide(w, r, id, b.ParticipantID, b.UserID, b.Round)
	if !ok {
		return
	}
	var applied int
	var err error
	if player != "" {
		applied, err = s.Store.AdjustMemberKnocks(r.Context(), rd.ID, b.ParticipantID, player, b.Delta)
	} else {
		applied, err = s.Store.AdjustKnocks(r.Context(), rd.ID, b.ParticipantID, b.Delta)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if applied != 0 {
		s.recomputeMany(r, []string{b.ParticipantID})
		text := "нок"
		if applied < 0 {
			text = "нок снят"
		}
		pid := b.ParticipantID
		_ = s.Store.AddMatchLog(r.Context(), id, rd.Number, &pid, "knock", who+": "+text, applied*store.KnockPoints, map[string]any{
			"type": "knock", "roundId": rd.ID, "participantId": pid, "userId": player, "applied": applied,
		})
		// Плашки новых нашивок - только на эфире: исправление после матча их не показывает.
		if !finished {
			s.flashPatches(r.Context(), id, s.syncPatches(r.Context()))
		} else if !s.rescore(w, r, id) {
			return
		}
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// POST /api/tournaments/{id}/legendary {legendaryId, participantId, userId, round} - легендарка стороне в идущем
// раунде, у команды 2×2 - её игроку userId; round - номер сыгранного раунда, когда исправляют завершённый матч.
func (s *Server) handleMatchLegendary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var b struct {
		LegendaryID   string `json:"legendaryId"`
		ParticipantID string `json:"participantId"`
		UserID        string `json:"userId"`
		Round         int    `json:"round"`
	}
	if err := readJSON(r, &b); err != nil || b.LegendaryID == "" || b.ParticipantID == "" {
		writeError(w, http.StatusBadRequest, "укажите легендарку и сторону")
		return
	}
	rd, player, who, finished, ok := s.scoreSide(w, r, id, b.ParticipantID, b.UserID, b.Round)
	if !ok {
		return
	}
	leg, err := s.Store.GetLegendary(r.Context(), b.LegendaryID)
	if err != nil {
		writeError(w, http.StatusNotFound, "легендарка не найдена")
		return
	}
	if err := s.Store.CompleteLegendaryInMatch(r.Context(), b.LegendaryID, id, b.ParticipantID, rd.Number, player); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "эта легендарка уже выполнена")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pid := b.ParticipantID
	_ = s.Store.AddMatchLog(r.Context(), id, rd.Number, &pid, "legendary",
		fmt.Sprintf("%s: легендарка «%s»", who, legendaryTitle(leg.Text)), leg.Points, map[string]any{
			"type": "legendary", "legendaryId": b.LegendaryID,
		})
	if finished && !s.rescore(w, r, id) {
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// legendaryTitle - название легендарки из текста вида «Название»: условие.
func legendaryTitle(text string) string {
	if strings.HasPrefix(text, "«") {
		if end := strings.Index(text, "»"); end > 0 {
			return strings.TrimPrefix(text[:end], "«")
		}
	}
	return text
}

// POST /api/tournaments/{id}/undo - отменить последнее действие журнала.
func (s *Server) handleMatchUndo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	finished, ok := s.matchFinished(w, r, id)
	if !ok {
		return
	}
	affected, err := s.Store.UndoLastMatchLog(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusConflict, "отменять нечего")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recomputeMany(r, affected)
	if !finished {
		s.syncPatches(r.Context())
	} else if !s.rescore(w, r, id) {
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}
