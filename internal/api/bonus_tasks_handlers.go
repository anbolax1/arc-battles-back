package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// recomputeMany пересчитывает очки нескольких участников (молча игнорирует ошибки отдельных).
func (s *Server) recomputeMany(r *http.Request, ids []string) {
	for _, id := range ids {
		if id != "" {
			_, _ = s.Store.RecomputeParticipantPoints(r.Context(), id)
		}
	}
}

// GET /api/tournaments/{id}/bonus-tasks — контракты участников по раундам. Organizer-only.
func (s *Server) handleListTournamentBonusTasks(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListTournamentBonusTasks(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// POST /api/rounds/{id}/bonus-tasks {participantId, taskId} — выдать участнику контракт (вручную).
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
		writeError(w, http.StatusConflict, "этот контракт у участника уже есть")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "не удалось выдать контракт: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

// POST /api/rounds/{id}/contracts/deal {participantId, count?} — раздать случайные контракты (по умолчанию 2).
func (s *Server) handleDealContracts(w http.ResponseWriter, r *http.Request) {
	roundID := chi.URLParam(r, "id")
	if st, err := s.Store.StatusByRound(r.Context(), roundID); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var b struct {
		ParticipantID string `json:"participantId"`
		Count         int    `json:"count"`
	}
	if err := readJSON(r, &b); err != nil || strings.TrimSpace(b.ParticipantID) == "" {
		writeError(w, http.StatusBadRequest, "укажите participantId")
		return
	}
	if b.Count <= 0 {
		b.Count = 2
	}
	items, err := s.Store.DealContracts(r.Context(), roundID, b.ParticipantID, b.Count)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// POST /api/round-bonus-tasks/{id}/complete {by: owner|opponent|none} — отметить исполнителя контракта.
func (s *Server) handleMarkContract(w http.ResponseWriter, r *http.Request) {
	if st, err := s.Store.StatusByBonusAssignment(r.Context(), chi.URLParam(r, "id")); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var b struct {
		By string `json:"by"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if b.By != "owner" && b.By != "opponent" && b.By != "none" && b.By != "" {
		writeError(w, http.StatusBadRequest, "by должен быть owner, opponent или none")
		return
	}
	affected, err := s.Store.MarkContract(r.Context(), chi.URLParam(r, "id"), b.By)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "контракт не найден")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "владелец уже выполнил этот контракт — балл противнику недоступен")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recomputeMany(r, affected)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// DELETE /api/round-bonus-tasks/{id} — убрать контракт у участника.
func (s *Server) handleRemoveBonusTask(w http.ResponseWriter, r *http.Request) {
	if st, err := s.Store.StatusByBonusAssignment(r.Context(), chi.URLParam(r, "id")); err == nil && s.blockIfFinished(w, st) {
		return
	}
	affected, err := s.Store.RemoveBonusTask(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "контракт не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recomputeMany(r, affected)
	w.WriteHeader(http.StatusNoContent)
}
