package api

import (
	"net/http"
	"strings"
)

// handleListSeasons — список сезонов (публично; для селектора на /rating).
func (s *Server) handleListSeasons(w http.ResponseWriter, r *http.Request) {
	seasons, err := s.Store.ListSeasons(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, seasons)
}

// handleStartSeason (superadmin) — завершить текущий активный сезон и начать новый.
func (s *Server) handleStartSeason(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "укажите название сезона")
		return
	}
	sn, err := s.Store.StartNewSeason(r.Context(), strings.TrimSpace(body.Name))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sn)
}
