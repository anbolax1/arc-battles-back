package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

var tagColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// maxTagName - длиннее подпись не помещается в бейдж у ника.
const maxTagName = 40

type tagBody struct {
	Name    string `json:"name"`
	Color   string `json:"color"`
	Visible *bool  `json:"visible"`
}

func (b *tagBody) check() string {
	b.Name = strings.TrimSpace(b.Name)
	b.Color = strings.ToLower(strings.TrimSpace(b.Color))
	switch {
	case b.Name == "" || utf8.RuneCountInString(b.Name) > maxTagName:
		return "название тега — от 1 до 40 символов"
	case !tagColorRe.MatchString(b.Color):
		return "цвет — в виде #rrggbb"
	}
	return ""
}

func (b tagBody) visible() bool { return b.Visible == nil || *b.Visible }

// writeTags отвечает полным списком тегов: кабинету проще перерисовать его целиком.
func (s *Server) writeTags(w http.ResponseWriter, r *http.Request, status int) {
	tags, err := s.Store.ListTags(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, status, tags)
}

func writeTagError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "тег или игрок не найден")
	case errors.Is(err, store.ErrAutoTag):
		writeError(w, http.StatusConflict, "тег роли или победителя сезона выдаётся автоматически")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// GET /api/me/tags - свои теги с отметками, какие видны в профиле.
func (s *Server) handleMyTags(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	tags, err := s.Store.UserTags(r.Context(), u.ID, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tags == nil {
		tags = []models.UserTag{}
	}
	writeJSON(w, http.StatusOK, tags)
}

// PUT /api/me/tags/{id} {hidden} - убрать свой тег из профиля или вернуть его.
func (s *Server) handleSetMyTag(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var b struct {
		Hidden bool `json:"hidden"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if err := s.Store.SetTagHiddenByUser(r.Context(), u.ID, chi.URLParam(r, "id"), b.Hidden); err != nil {
		writeTagError(w, err)
		return
	}
	s.handleMyTags(w, r)
}

// GET /api/tags - все теги с теми, кому они выданы.
func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	s.writeTags(w, r, http.StatusOK)
}

// POST /api/tags {name, color, visible} - новый тег.
func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	var b tagBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if msg := b.check(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if _, err := s.Store.CreateTag(r.Context(), b.Name, b.Color, b.visible()); err != nil {
		writeTagError(w, err)
		return
	}
	s.writeTags(w, r, http.StatusCreated)
}

// PATCH /api/tags/{id} {name, color, visible} - поправить тег.
func (s *Server) handleUpdateTag(w http.ResponseWriter, r *http.Request) {
	var b tagBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if msg := b.check(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if _, err := s.Store.UpdateTag(r.Context(), chi.URLParam(r, "id"), b.Name, b.Color, b.visible()); err != nil {
		writeTagError(w, err)
		return
	}
	s.writeTags(w, r, http.StatusOK)
}

// DELETE /api/tags/{id} - удалить тег у всех.
func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteTag(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeTagError(w, err)
		return
	}
	s.writeTags(w, r, http.StatusOK)
}

// POST /api/tags/{id}/holders {userId} - выдать тег игроку.
func (s *Server) handleAddTagHolder(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UserID string `json:"userId"`
	}
	if err := readJSON(r, &b); err != nil || strings.TrimSpace(b.UserID) == "" {
		writeError(w, http.StatusBadRequest, "выберите игрока")
		return
	}
	if err := s.Store.SetTagHolder(r.Context(), chi.URLParam(r, "id"), strings.TrimSpace(b.UserID), true); err != nil {
		writeTagError(w, err)
		return
	}
	s.writeTags(w, r, http.StatusOK)
}

// DELETE /api/tags/{id}/holders/{userId} - забрать тег у игрока.
func (s *Server) handleRemoveTagHolder(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.SetTagHolder(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "userId"), false); err != nil {
		writeTagError(w, err)
		return
	}
	s.writeTags(w, r, http.StatusOK)
}
