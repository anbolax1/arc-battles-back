package api

import (
	"net/http"

	"github.com/battle-for-respect/backend/internal/store"
)

type siteSettings struct {
	Design string `json:"design"`
}

// handleGetSite - общие настройки сайта (публично: по ним фронт выбирает дизайн страниц).
func (s *Server) handleGetSite(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.SiteDesign(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, siteSettings{Design: d})
}

// handleSetSiteDesign (superadmin) - переключает дизайн сайта для всех посетителей.
func (s *Server) handleSetSiteDesign(w http.ResponseWriter, r *http.Request) {
	var body siteSettings
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if !store.ValidDesign(body.Design) {
		writeError(w, http.StatusBadRequest, "неизвестный дизайн: нужен classic или surface")
		return
	}
	if err := s.Store.SetSiteDesign(r.Context(), body.Design); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}
