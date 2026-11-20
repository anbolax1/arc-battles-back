package api

import (
	"errors"
	"net/http"

	"github.com/battle-for-respect/backend/internal/store"
)

// GET /api/task-settings - правила раздачи заданий: на время вкл/выкл, очки и число обычных заданий на раунд.
func (s *Server) handleGetTaskSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Store.TaskDealSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, cfg)
}

// PUT /api/task-settings {timedEnabled, maxPoints, maxTasks} - действует со следующей раздачи, выданное не меняется.
func (s *Server) handleSetTaskSettings(w http.ResponseWriter, r *http.Request) {
	var b struct {
		TimedEnabled *bool `json:"timedEnabled"`
		MaxPoints    *int  `json:"maxPoints"`
		MaxTasks     *int  `json:"maxTasks"`
	}
	if err := readJSON(r, &b); err != nil || b.TimedEnabled == nil || b.MaxPoints == nil || b.MaxTasks == nil {
		writeError(w, http.StatusBadRequest, "укажите timedEnabled, maxPoints и maxTasks")
		return
	}
	err := s.Store.SaveTaskDealSettings(r.Context(), store.TaskDealSettings{
		TimedEnabled: *b.TimedEnabled, MaxPoints: *b.MaxPoints, MaxTasks: *b.MaxTasks,
	})
	if errors.Is(err, store.ErrBadTaskSettings) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.handleGetTaskSettings(w, r)
}
