package api

import (
	"net/http"
)

func (s *Server) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	if mode != "2x2" {
		mode = "1x1"
	}
	rows, err := s.Store.Leaderboard(r.Context(), mode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "rows": rows})
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Store.ListCatalogTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	complications, err := s.Store.ListCatalogComplications(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":         tasks,
		"complications": complications,
	})
}
