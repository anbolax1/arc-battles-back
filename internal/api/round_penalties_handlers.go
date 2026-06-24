package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// handleListTournamentPenalties — протоколы сторон по раундам турнира (для кабинета).
// GET /api/tournaments/{id}/penalties. Organizer-only.
func (s *Server) handleListTournamentPenalties(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListTournamentPenalties(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleSetProtocol назначает участнику протокол в раунде (ровно один; complicationId="" — снять).
// POST /api/rounds/{id}/protocol {participantId, complicationId}. Organizer-only.
func (s *Server) handleSetProtocol(w http.ResponseWriter, r *http.Request) {
	roundID := chi.URLParam(r, "id")
	if st, err := s.Store.StatusByRound(r.Context(), roundID); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var b struct {
		ParticipantID  string `json:"participantId"`
		ComplicationID string `json:"complicationId"`
	}
	if err := readJSON(r, &b); err != nil || strings.TrimSpace(b.ParticipantID) == "" {
		writeError(w, http.StatusBadRequest, "укажите participantId")
		return
	}
	err := s.Store.SetParticipantProtocol(r.Context(), roundID, b.ParticipantID, strings.TrimSpace(b.ComplicationID))
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "этот протокол уже действует на другую сторону (без повторов)")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdjustProtocolViolations меняет число нарушений (= минут штрафа) протокола стороны.
// POST /api/rounds/{id}/protocol/violations {participantId, delta}. Organizer-only.
func (s *Server) handleAdjustProtocolViolations(w http.ResponseWriter, r *http.Request) {
	roundID := chi.URLParam(r, "id")
	if st, err := s.Store.StatusByRound(r.Context(), roundID); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var b struct {
		ParticipantID string `json:"participantId"`
		Delta         int    `json:"delta"`
	}
	if err := readJSON(r, &b); err != nil || strings.TrimSpace(b.ParticipantID) == "" {
		writeError(w, http.StatusBadRequest, "укажите participantId")
		return
	}
	if b.Delta == 0 {
		b.Delta = 1
	}
	times, err := s.Store.AdjustProtocolViolations(r.Context(), roundID, b.ParticipantID, b.Delta)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "у стороны не назначен протокол")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Протоколы на очки НЕ влияют — пересчёт total_points не нужен. times = минуты штрафа.
	writeJSON(w, http.StatusOK, map[string]any{"times": times, "minutes": times})
}
