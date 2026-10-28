package api

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/battle-for-respect/backend/internal/store"
)

// Название роли подписчика стоит в профиле и в кабинете одной строкой.
const (
	minRoleName = 2
	maxRoleName = 24
)

type siteSettings struct {
	Design         string `json:"design"`
	SubscriberRole string `json:"subscriberRole"` // как на сайте называется роль подписчика
}

func (s *Server) siteSettings(r *http.Request) (siteSettings, error) {
	d, err := s.Store.SiteDesign(r.Context())
	if err != nil {
		return siteSettings{}, err
	}
	name, err := s.Store.SubscriberRoleName(r.Context())
	return siteSettings{Design: d, SubscriberRole: name}, err
}

// handleGetSite - общие настройки сайта (публично: по ним фронт выбирает дизайн страниц).
func (s *Server) handleGetSite(w http.ResponseWriter, r *http.Request) {
	s.writeSite(w, r)
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
	s.writeSite(w, r)
}

// handleSetSubscriberRole (superadmin) - PUT /api/site/subscriber-role {name}: переименовать роль подписчика.
func (s *Server) handleSetSubscriberRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	name := strings.Join(strings.Fields(body.Name), " ")
	if n := utf8.RuneCountInString(name); n < minRoleName || n > maxRoleName {
		writeError(w, http.StatusBadRequest, "название роли - от 2 до 24 символов")
		return
	}
	if err := s.Store.SetSubscriberRoleName(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeSite(w, r)
}

func (s *Server) writeSite(w http.ResponseWriter, r *http.Request) {
	out, err := s.siteSettings(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
