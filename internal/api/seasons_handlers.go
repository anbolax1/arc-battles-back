package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

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

// handleStartSeason (superadmin) — завершить текущий активный сезон и начать новый.
func (s *Server) handleStartSeason(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "укажите название сезона")
		return
	}
	sn, err := s.Store.StartNewSeason(r.Context(), strings.TrimSpace(body.Name))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	sn, err := s.Store.UpdateSeason(r.Context(), id, name, body.StartedAt, body.EndedAt)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "сезон не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
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
	w.WriteHeader(http.StatusNoContent)
}
