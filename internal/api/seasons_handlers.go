package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// handleListSeasons — список сезонов (публично; для селектора на /rating).
func (s *Server) handleListSeasons(w http.ResponseWriter, r *http.Request) {
	seasons, err := s.Store.ListSeasons(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, seasons)
}

// handleSeasonRecap - итоги сезона для страницы /season/{номер}; сезон - по номеру или id.
func (s *Server) handleSeasonRecap(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var sn models.Season
	var err error
	if n, convErr := strconv.Atoi(key); convErr == nil {
		sn, err = s.Store.SeasonByNumber(r.Context(), n)
	} else {
		sn, err = s.Store.GetSeason(r.Context(), key)
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "сезон не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	data, err := s.Store.SeasonRecapJSON(r.Context(), sn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeRaw(w, http.StatusOK, data)
}

// handleStartSeason (superadmin) — завершить текущий активный сезон и начать новый.
func (s *Server) handleStartSeason(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		KFactor  int    `json:"kFactor"`
		StartMmr int    `json:"startMmr"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "укажите название сезона")
		return
	}
	if msg := validSeasonRating(body.KFactor, body.StartMmr); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	sn, err := s.Store.StartNewSeason(r.Context(), strings.TrimSpace(body.Name), body.KFactor, body.StartMmr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Новый сезон - рейтинг у всех с нуля: кэш MMR переключается на него.
	_ = s.Store.RefreshMmrCaches(r.Context())
	// Прошлый сезон закрыт: его итоговые нашивки закрепляются.
	s.syncPatches(r.Context())
	writeJSON(w, http.StatusCreated, sn)
}

// handleUpdateSeason (superadmin) — изменить название и даты сезона.
// Тело: {name, startedAt, endedAt?}. endedAt пустой/null — сезон идёт. Статус не меняется.
func (s *Server) handleUpdateSeason(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Name      string     `json:"name"`
		StartedAt time.Time  `json:"startedAt"`
		EndedAt   *time.Time `json:"endedAt"`
		KFactor   int        `json:"kFactor"`
		StartMmr  int        `json:"startMmr"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "укажите название сезона")
		return
	}
	if body.StartedAt.IsZero() {
		writeError(w, http.StatusBadRequest, "укажите дату начала")
		return
	}
	if body.EndedAt != nil && body.EndedAt.Before(body.StartedAt) {
		writeError(w, http.StatusBadRequest, "дата окончания раньше даты начала")
		return
	}
	if msg := validSeasonRating(body.KFactor, body.StartMmr); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	prev, err := s.Store.GetSeason(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "сезон не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sn, err := s.Store.UpdateSeason(r.Context(), id, name, body.StartedAt, body.EndedAt, body.KFactor, body.StartMmr)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "сезон не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Сменились правила рейтинга - матчи сезона пересчитываются заново.
	if sn.KFactor != prev.KFactor || sn.StartMmr != prev.StartMmr {
		if err := s.Store.RecomputeAllMmr(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, sn)
}

// handleDeleteSeason (superadmin) — удалить сезон. Турниры сезона сохраняются и
// отвязываются (season_id → NULL); в другие сезоны они автоматически не попадают.
func (s *Server) handleDeleteSeason(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	err := s.Store.DeleteSeason(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "сезон не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Матчи сезона остались без сезона - их рейтинг считается по правилам «вне сезона».
	if err := s.Store.RecomputeAllMmr(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validSeasonRating проверяет правила рейтинга сезона; 0 - значение по умолчанию.
func validSeasonRating(k, start int) string {
	if k < 0 || k > 400 {
		return "K-фактор — от 1 до 400"
	}
	if start < 0 || start > 10000 {
		return "стартовый MMR — от 1 до 10000"
	}
	return ""
}
