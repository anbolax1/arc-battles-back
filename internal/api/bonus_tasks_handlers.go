package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// GET /api/tournaments/{id}/bonus-tasks — бонусные задания участников по раундам. Organizer-only.
func (s *Server) handleListTournamentBonusTasks(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListTournamentBonusTasks(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// POST /api/rounds/{id}/bonus-tasks {participantId, taskId} — добавить участнику бонусное на раунд.
func (s *Server) handleAssignBonusTask(w http.ResponseWriter, r *http.Request) {
	roundID := chi.URLParam(r, "id")
	if st, err := s.Store.StatusByRound(r.Context(), roundID); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var b struct {
		ParticipantID string `json:"participantId"`
		TaskID        string `json:"taskId"`
	}
	if err := readJSON(r, &b); err != nil || strings.TrimSpace(b.ParticipantID) == "" || strings.TrimSpace(b.TaskID) == "" {
		writeError(w, http.StatusBadRequest, "укажите participantId и taskId")
		return
	}
	item, err := s.Store.AssignBonusTask(r.Context(), roundID, b.ParticipantID, b.TaskID)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "это бонусное задание у участника уже есть")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "не удалось добавить бонусное задание: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

// POST /api/round-bonus-tasks/{id}/count {delta} — счётчик зачётов бонусного задания (+1/−1).
func (s *Server) handleAdjustBonusCount(w http.ResponseWriter, r *http.Request) {
	if st, err := s.Store.StatusByBonusAssignment(r.Context(), chi.URLParam(r, "id")); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var b struct {
		Delta   int    `json:"delta"`
		RoundID string `json:"roundId"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if b.Delta == 0 {
		b.Delta = 1
	}
	// При зачёте (delta>0) задание «переезжает» на раунд, где его зачли по факту.
	pid, times, err := s.Store.AdjustBonusTaskCount(r.Context(), chi.URLParam(r, "id"), b.Delta, b.RoundID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "бонусное задание не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := s.Store.RecomputeParticipantPoints(r.Context(), pid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"times": times, "participantTotalPoints": total})
}

// DELETE /api/round-bonus-tasks/{id} — убрать бонусное задание у участника.
func (s *Server) handleRemoveBonusTask(w http.ResponseWriter, r *http.Request) {
	if st, err := s.Store.StatusByBonusAssignment(r.Context(), chi.URLParam(r, "id")); err == nil && s.blockIfFinished(w, st) {
		return
	}
	pid, err := s.Store.RemoveBonusTask(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "бонусное задание не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.Store.RecomputeParticipantPoints(r.Context(), pid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
