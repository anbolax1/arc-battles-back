package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// B2: записать/обновить результат участника в раунде и пересчитать его очки.
// PUT /api/rounds/{id}/entries/{participantId}. Organizer-only.
func (s *Server) handleUpsertRoundEntry(w http.ResponseWriter, r *http.Request) {
	roundID := chi.URLParam(r, "id")
	participantID := chi.URLParam(r, "participantId")
	if st, err := s.Store.StatusByRound(r.Context(), roundID); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var body struct {
		Points        int             `json:"points"`
		Tasks         json.RawMessage `json:"tasks"`
		Bonus         json.RawMessage `json:"bonus"`
		Complications json.RawMessage `json:"complications"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	// База раунда — заработанное (≥0). Вычитания только через счётчики штрафов.
	if body.Points < 0 {
		body.Points = 0
	}
	entry, err := s.Store.UpsertRoundEntry(r.Context(), models.RoundEntry{
		RoundID:       roundID,
		ParticipantID: participantID,
		Points:        body.Points,
		Tasks:         body.Tasks,
		Bonus:         body.Bonus,
		Complications: body.Complications,
	})
	if err != nil {
		// чаще всего — несуществующий round/participant (нарушение FK)
		writeError(w, http.StatusBadRequest, "не удалось сохранить результат раунда (проверьте round и participant): "+err.Error())
		return
	}
	total, err := s.Store.RecomputeParticipantPoints(r.Context(), participantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entry":                  entry,
		"participantTotalPoints": total,
	})
}

// GET /api/rounds/{id}/entries — результаты всех участников в раунде. Organizer-only.
func (s *Server) handleListRoundEntries(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Store.ListRoundEntries(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// PATCH /api/rounds/{id} — статус/карта/номер раунда. Organizer-only.
func (s *Server) handleUpdateRound(w http.ResponseWriter, r *http.Request) {
	roundID := chi.URLParam(r, "id")
	if st, err := s.Store.StatusByRound(r.Context(), roundID); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var body struct {
		Status *string `json:"status"`
		Map    *string `json:"map"`
		Number *int    `json:"number"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	round, err := s.Store.UpdateRound(r.Context(), roundID, body.Status, body.Map, body.Number)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "раунд не найден")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "раунд с таким номером уже есть")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, round)
}

// DELETE /api/rounds/{id} — удалить раунд (и каскадом его результаты/задания/штрафы). Organizer-only.
func (s *Server) handleDeleteRound(w http.ResponseWriter, r *http.Request) {
	roundID := chi.URLParam(r, "id")
	if st, err := s.Store.StatusByRound(r.Context(), roundID); err == nil && s.blockIfFinished(w, st) {
		return
	}
	if err := s.Store.DeleteRound(r.Context(), roundID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "раунд не найден")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
