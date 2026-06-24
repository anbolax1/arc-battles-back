package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// recomputeIfSet пересчитывает очки участника, если указатель не nil (молча игнорирует ошибки).
func (s *Server) recomputeIfSet(r *http.Request, participantID *string) {
	if participantID != nil && *participantID != "" {
		_, _ = s.Store.RecomputeParticipantPoints(r.Context(), *participantID)
	}
}

// ---- Пул стартовых заданий (organizer-only, скрыт от публики) ----

func (s *Server) handleListStarterTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Store.ListStarterTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

type starterTaskBody struct {
	Text   string `json:"text"`
	Points int    `json:"points"`
	Kind   string `json:"kind"`
}

// нормализует вид задания: pve | pvp | pvpve (по умолчанию pvpve; legacy mixed → pvpve).
func starterKind(k string) string {
	switch k {
	case "pve", "pvp", "pvpve":
		return k
	case "mixed":
		return "pvpve"
	default:
		return "pvpve"
	}
}

func (s *Server) handleCreateStarterTask(w http.ResponseWriter, r *http.Request) {
	var b starterTaskBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(b.Text) == "" {
		writeError(w, http.StatusBadRequest, "укажите текст задания")
		return
	}
	if b.Points < 0 {
		writeError(w, http.StatusBadRequest, "баллы не могут быть отрицательными")
		return
	}
	created, err := s.Store.CreateStarterTask(r.Context(), models.StarterTask{Text: strings.TrimSpace(b.Text), Points: b.Points, Kind: starterKind(b.Kind)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateStarterTask(w http.ResponseWriter, r *http.Request) {
	var b starterTaskBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(b.Text) == "" {
		writeError(w, http.StatusBadRequest, "укажите текст задания")
		return
	}
	updated, err := s.Store.UpdateStarterTask(r.Context(), chi.URLParam(r, "id"),
		models.StarterTask{Text: strings.TrimSpace(b.Text), Points: b.Points, Kind: starterKind(b.Kind)})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "задание не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteStarterTask(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteStarterTask(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Распределение по раундам и зачёт в эфире ----

func (s *Server) handleListTournamentStarterTasks(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListTournamentStarterTasks(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleAssignRoundTask(w http.ResponseWriter, r *http.Request) {
	roundID := chi.URLParam(r, "id")
	if st, err := s.Store.StatusByRound(r.Context(), roundID); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var b struct {
		StarterTaskID string `json:"starterTaskId"`
	}
	if err := readJSON(r, &b); err != nil || strings.TrimSpace(b.StarterTaskID) == "" {
		writeError(w, http.StatusBadRequest, "укажите starterTaskId")
		return
	}
	item, err := s.Store.AssignTaskToRound(r.Context(), roundID, b.StarterTaskID)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "это задание уже назначено на другой раунд турнира")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "не удалось назначить задание: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) handleUnassignRoundTask(w http.ResponseWriter, r *http.Request) {
	if st, err := s.Store.StatusByStarterAssignment(r.Context(), chi.URLParam(r, "id")); err == nil && s.blockIfFinished(w, st) {
		return
	}
	affected, err := s.Store.UnassignRoundTask(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "назначение не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recomputeMany(r, affected)
	w.WriteHeader(http.StatusNoContent)
}

// handleAdjustRoundTaskCount меняет счётчик зачётов стартового задания на delta (+1/−1)
// для участника. POST /round-starter-tasks/{id}/count {participantId, delta}.
func (s *Server) handleAdjustRoundTaskCount(w http.ResponseWriter, r *http.Request) {
	if st, err := s.Store.StatusByStarterAssignment(r.Context(), chi.URLParam(r, "id")); err == nil && s.blockIfFinished(w, st) {
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
	times, err := s.Store.AdjustRoundTaskCount(r.Context(), chi.URLParam(r, "id"), b.ParticipantID, b.Delta)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "назначение не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "не удалось зачесть задание: "+err.Error())
		return
	}
	total, err := s.Store.RecomputeParticipantPoints(r.Context(), b.ParticipantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"times": times, "participantTotalPoints": total})
}
