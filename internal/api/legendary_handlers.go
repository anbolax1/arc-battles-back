package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// GET /api/legendary — публичный список легендарных контрактов (со статусом и журналом выполнений).
func (s *Server) handleListLegendary(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListLegendary(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

type legendaryBody struct {
	Text   string `json:"text"`
	Points int    `json:"points"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Author string `json:"author"`
	Title  string `json:"title"`
}

func (b legendaryBody) toModel() models.CatalogLegendary {
	return models.CatalogLegendary{
		Text:   strings.TrimSpace(b.Text),
		Points: b.Points,
		Kind:   store.NormalizePlayerType(b.Kind),
		Source: defaultStr(b.Source, "official"),
		Author: strings.TrimSpace(b.Author),
		Title:  strings.TrimSpace(b.Title),
	}
}

func (s *Server) handleCreateLegendary(w http.ResponseWriter, r *http.Request) {
	var b legendaryBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	l := b.toModel()
	if l.Text == "" {
		writeError(w, http.StatusBadRequest, "укажите текст легендарного контракта")
		return
	}
	created, err := s.Store.CreateLegendary(r.Context(), l)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateLegendary(w http.ResponseWriter, r *http.Request) {
	var b legendaryBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	l := b.toModel()
	if l.Text == "" {
		writeError(w, http.StatusBadRequest, "укажите текст легендарного контракта")
		return
	}
	updated, err := s.Store.UpdateLegendary(r.Context(), chi.URLParam(r, "id"), l)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "легендарный контракт не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteLegendary(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteLegendary(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/legendary/{id}/complete {nickname, participantId?, userId?, tournamentId?, map}
// — отметить легендарный контракт выполненным (навсегда). +10 баллов участнику (если задан).
func (s *Server) handleCompleteLegendary(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Nickname      string  `json:"nickname"`
		ParticipantID *string `json:"participantId"`
		UserID        *string `json:"userId"`
		TournamentID  *string `json:"tournamentId"`
		Map           string  `json:"map"`
	}
	if err := readJSON(r, &b); err != nil || strings.TrimSpace(b.Nickname) == "" {
		writeError(w, http.StatusBadRequest, "укажите nickname игрока")
		return
	}
	pid, err := s.Store.CompleteLegendary(r.Context(), chi.URLParam(r, "id"), models.LegendaryCompletion{
		Nickname:      strings.TrimSpace(b.Nickname),
		ParticipantID: emptyToNil(b.ParticipantID),
		UserID:        emptyToNil(b.UserID),
		TournamentID:  emptyToNil(b.TournamentID),
		Map:           strings.TrimSpace(b.Map),
	})
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "этот легендарный контракт уже выполнен")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recomputeIfSet(r, pid)
	updated, err := s.Store.GetLegendary(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// POST /api/legendary/{id}/reopen — снять выполнение (вернуть в пул доступных).
func (s *Server) handleUncompleteLegendary(w http.ResponseWriter, r *http.Request) {
	pid, err := s.Store.UncompleteLegendary(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recomputeIfSet(r, pid)
	updated, err := s.Store.GetLegendary(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// emptyToNil превращает пустую строку-указатель в nil (необязательные ссылки в теле запроса).
func emptyToNil(v *string) *string {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil
	}
	t := strings.TrimSpace(*v)
	return &t
}
