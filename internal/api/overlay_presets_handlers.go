package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Тело запроса на создание/обновление пресета: имя + произвольный JSON раскладки.
type presetBody struct {
	Name   string          `json:"name"`
	Layout json.RawMessage `json:"layout"`
}

func (s *Server) handleListOverlayPresets(w http.ResponseWriter, r *http.Request) {
	presets, err := s.Store.ListOverlayPresets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, presets)
}

func (s *Server) handleCreateOverlayPreset(w http.ResponseWriter, r *http.Request) {
	var b presetBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(b.Name) == "" {
		writeError(w, http.StatusBadRequest, "укажите название пресета")
		return
	}
	if len(b.Layout) == 0 {
		b.Layout = json.RawMessage("{}")
	}
	p, err := s.Store.CreateOverlayPreset(r.Context(), strings.TrimSpace(b.Name), b.Layout)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateOverlayPreset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var b presetBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(b.Name) == "" {
		writeError(w, http.StatusBadRequest, "укажите название пресета")
		return
	}
	if len(b.Layout) == 0 {
		b.Layout = json.RawMessage("{}")
	}
	p, err := s.Store.UpdateOverlayPreset(r.Context(), id, strings.TrimSpace(b.Name), b.Layout)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteOverlayPreset(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteOverlayPreset(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
