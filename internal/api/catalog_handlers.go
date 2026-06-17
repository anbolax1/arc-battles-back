package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// validValue проверяет тип значения и диапазон (для процента — 0..100).
func validValue(valueType string, value int) (string, bool) {
	switch valueType {
	case "", models.ValueFixed:
		return models.ValueFixed, value >= 0
	case models.ValuePercent:
		return models.ValuePercent, value >= 0 && value <= 100
	default:
		return "", false
	}
}

func defaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// ---- Задания ----

type taskBody struct {
	Text      string `json:"text"`
	Points    int    `json:"points"`
	ValueType string `json:"valueType"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Author    string `json:"author"`
	Title     string `json:"title"`
}

func (b taskBody) toModel() (models.CatalogTask, string, bool) {
	vt, ok := validValue(b.ValueType, b.Points)
	if !ok {
		return models.CatalogTask{}, "", false
	}
	return models.CatalogTask{
		Text:      strings.TrimSpace(b.Text),
		Points:    b.Points,
		ValueType: vt,
		Kind:      defaultStr(b.Kind, "mixed"),
		Source:    defaultStr(b.Source, "official"),
		Author:    strings.TrimSpace(b.Author),
		Title:     strings.TrimSpace(b.Title),
	}, vt, true
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var b taskBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	t, _, ok := b.toModel()
	if t.Text == "" {
		writeError(w, http.StatusBadRequest, "укажите текст задания")
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "valueType должен быть fixed или percent (процент 0..100)")
		return
	}
	created, err := s.Store.CreateCatalogTask(r.Context(), t)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	var b taskBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	t, _, ok := b.toModel()
	if t.Text == "" {
		writeError(w, http.StatusBadRequest, "укажите текст задания")
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "valueType должен быть fixed или percent (процент 0..100)")
		return
	}
	updated, err := s.Store.UpdateCatalogTask(r.Context(), chi.URLParam(r, "id"), t)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "задание не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteCatalogTask(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Усложнения ----

type complicationBody struct {
	Text      string `json:"text"`
	Penalty   int    `json:"penalty"`
	ValueType string `json:"valueType"`
	Source    string `json:"source"`
	Author    string `json:"author"`
	Title     string `json:"title"`
}

func (b complicationBody) toModel() (models.CatalogComplication, bool) {
	vt, ok := validValue(b.ValueType, b.Penalty)
	if !ok {
		return models.CatalogComplication{}, false
	}
	return models.CatalogComplication{
		Text:      strings.TrimSpace(b.Text),
		Penalty:   b.Penalty,
		ValueType: vt,
		Source:    defaultStr(b.Source, "official"),
		Author:    strings.TrimSpace(b.Author),
		Title:     strings.TrimSpace(b.Title),
	}, ok
}

func (s *Server) handleCreateComplication(w http.ResponseWriter, r *http.Request) {
	var b complicationBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	c, ok := b.toModel()
	if c.Text == "" {
		writeError(w, http.StatusBadRequest, "укажите текст усложнения")
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "valueType должен быть fixed или percent (процент 0..100)")
		return
	}
	created, err := s.Store.CreateCatalogComplication(r.Context(), c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateComplication(w http.ResponseWriter, r *http.Request) {
	var b complicationBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	c, ok := b.toModel()
	if c.Text == "" {
		writeError(w, http.StatusBadRequest, "укажите текст усложнения")
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "valueType должен быть fixed или percent (процент 0..100)")
		return
	}
	updated, err := s.Store.UpdateCatalogComplication(r.Context(), chi.URLParam(r, "id"), c)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "усложнение не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteComplication(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteCatalogComplication(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
