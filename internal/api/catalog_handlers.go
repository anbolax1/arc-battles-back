package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
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
	Name      string `json:"name"`
	Category  string `json:"category"`
	MapCode   string `json:"mapCode"`
	Active    *bool  `json:"active"`
}

func (b taskBody) toModel() (models.CatalogTask, string, bool) {
	vt, ok := validValue(b.ValueType, b.Points)
	if !ok {
		return models.CatalogTask{}, "", false
	}
	category := "task"
	if b.Category == "protocol" {
		category = "protocol"
	}
	points := b.Points
	if points == 0 {
		// Награды 3 сезона: задание - 2 балла, протокол - 1.
		points = 2
		if category == "protocol" {
			points = 1
		}
	}
	active := true
	if b.Active != nil {
		active = *b.Active
	}
	return models.CatalogTask{
		Text:      strings.TrimSpace(b.Text),
		Points:    points,
		ValueType: vt,
		Kind:      store.NormalizePlayerType(b.Kind),
		Source:    defaultStr(b.Source, "official"),
		Author:    strings.TrimSpace(b.Author),
		Title:     strings.TrimSpace(b.Title),
		Name:      strings.Trim(strings.TrimSpace(b.Name), "«»"),
		Category:  category,
		MapCode:   strings.TrimSpace(b.MapCode),
		Active:    active,
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

// POST /api/catalog/tasks/bulk {items: [...]} - добавить пачку заданий разом (вставка списка из таблицы).
func (s *Server) handleBulkCreateTasks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []taskBody `json:"items"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if len(body.Items) == 0 || len(body.Items) > 500 {
		writeError(w, http.StatusBadRequest, "нужно от 1 до 500 заданий")
		return
	}
	out := make([]models.CatalogTask, 0, len(body.Items))
	for i, b := range body.Items {
		t, _, ok := b.toModel()
		if t.Text == "" || !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("строка %d: нужен текст задания", i+1))
			return
		}
		created, err := s.Store.CreateCatalogTask(r.Context(), t)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, created)
	}
	writeJSON(w, http.StatusCreated, out)
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
