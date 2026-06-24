package api

import (
	"net/http"
)

func (s *Server) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	if mode != "2x2" {
		mode = "1x1"
	}
	// season: "all" — за всё время; пусто — текущий активный сезон; иначе конкретный id.
	season := r.URL.Query().Get("season")
	seasonID := season
	if season == "all" {
		seasonID = ""
	} else if season == "" {
		if active, err := s.Store.ActiveSeason(r.Context()); err == nil {
			seasonID = active.ID
		} else {
			seasonID = "" // активного нет — показываем за всё время
		}
	}
	rows, err := s.Store.Leaderboard(r.Context(), mode, seasonID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "seasonId": seasonID, "rows": rows})
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
	legendary, err := s.Store.ListLegendary(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":         tasks,         // контракты
		"complications": complications, // протоколы
		"legendary":     legendary,     // легендарные контракты
	})
}
