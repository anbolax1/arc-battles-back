package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// handleListTournamentPenalties — все применённые усложнения по раундам турнира (для кабинета).
// GET /api/tournaments/{id}/penalties. Organizer-only.
func (s *Server) handleListTournamentPenalties(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListTournamentPenalties(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleAdjustRoundPenalty меняет счётчик применений усложнения участнику в раунде на delta.
// POST /api/rounds/{id}/penalties/count {participantId, complicationId, delta}. Organizer-only.
func (s *Server) handleAdjustRoundPenalty(w http.ResponseWriter, r *http.Request) {
	roundID := chi.URLParam(r, "id")
	if st, err := s.Store.StatusByRound(r.Context(), roundID); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var b struct {
		ParticipantID  string `json:"participantId"`
		ComplicationID string `json:"complicationId"`
		Delta          int    `json:"delta"`
	}
	if err := readJSON(r, &b); err != nil || strings.TrimSpace(b.ParticipantID) == "" || strings.TrimSpace(b.ComplicationID) == "" {
		writeError(w, http.StatusBadRequest, "укажите participantId и complicationId")
		return
	}
	if b.Delta == 0 {
		b.Delta = 1
	}
	times, err := s.Store.AdjustRoundPenaltyCount(r.Context(), roundID, b.ParticipantID, b.ComplicationID, b.Delta)
	if err != nil {
		writeError(w, http.StatusBadRequest, "не удалось применить штраф: "+err.Error())
		return
	}
	total, err := s.Store.RecomputeParticipantPoints(r.Context(), b.ParticipantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"times": times, "participantTotalPoints": total})
}
