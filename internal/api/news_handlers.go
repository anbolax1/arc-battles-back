package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/battle-for-respect/backend/internal/media"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// maxNewsTitle - заголовок стоит крупно в ленте: длиннее не помещается в пару строк.
const maxNewsTitle = 140

// maxNewsBody - новость, а не статья: длинный текст лучше разбить на несколько.
const maxNewsBody = 20000

// newsImageDir - папка картинок новостей в хранилище медиа.
const newsImageDir = "news"

type newsBody struct {
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	PublishedAt *time.Time `json:"publishedAt"`
}

func (b *newsBody) check() string {
	b.Title = strings.TrimSpace(b.Title)
	b.Body = strings.TrimSpace(b.Body)
	switch {
	case b.Title == "" || utf8.RuneCountInString(b.Title) > maxNewsTitle:
		return fmt.Sprintf("заголовок — от 1 до %d символов", maxNewsTitle)
	case utf8.RuneCountInString(b.Body) > maxNewsBody:
		return fmt.Sprintf("текст — до %d символов", maxNewsBody)
	}
	return ""
}

func writeNewsError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "новость не найдена")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// GET /api/news?limit&offset - вышедшие новости, свежие сверху.
func (s *Server) handleListNews(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, total, err := s.Store.ListNews(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// GET /api/news/{id} - одна новость; черновик и запланированную видит только организатор.
func (s *Server) handleGetNews(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r.Context())
	n, err := s.Store.GetNews(r.Context(), chi.URLParam(r, "id"), ok && u.Role.AtLeast(models.RoleSuperadmin))
	if err != nil {
		writeNewsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

// GET /api/news/all - все новости для кабинета, с черновиками.
func (s *Server) handleListAllNews(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListAllNews(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// POST /api/news {title, body, publishedAt} - новая новость; без даты публикации - черновик.
func (s *Server) handleCreateNews(w http.ResponseWriter, r *http.Request) {
	var b newsBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if msg := b.check(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	n, err := s.Store.CreateNews(r.Context(), b.Title, b.Body, b.PublishedAt)
	if err != nil {
		writeNewsError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

// PATCH /api/news/{id} {title, body, publishedAt} - поправить новость, снять с публикации или выпустить.
func (s *Server) handleUpdateNews(w http.ResponseWriter, r *http.Request) {
	var b newsBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if msg := b.check(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	n, err := s.Store.UpdateNews(r.Context(), chi.URLParam(r, "id"), b.Title, b.Body, b.PublishedAt)
	if err != nil {
		writeNewsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

// DELETE /api/news/{id} - удалить новость вместе с картинкой.
func (s *Server) handleDeleteNews(w http.ResponseWriter, r *http.Request) {
	image, err := s.Store.DeleteNews(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeNewsError(w, err)
		return
	}
	s.Media.Remove(image)
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/news/{id}/image - картинка новости: файл в теле запроса либо {url} - тогда сервер
// скачивает картинку к себе.
func (s *Server) handleSetNewsImage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.Store.GetNews(r.Context(), id, true); err != nil {
		writeNewsError(w, err)
		return
	}
	// Новое имя у каждой картинки: браузер не покажет прежнюю из кэша.
	name := fmt.Sprintf("%s-%d", id, time.Now().UnixNano())
	var rel string
	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var b struct {
			URL string `json:"url"`
		}
		if err := readJSON(r, &b); err != nil {
			writeError(w, http.StatusBadRequest, "некорректный JSON")
			return
		}
		rel, err = s.Media.FetchImage(r.Context(), b.URL, newsImageDir, name)
	} else {
		rel, err = s.Media.SaveImage(newsImageDir, name, http.MaxBytesReader(w, r.Body, media.MaxImageBytes+1))
	}
	if err != nil {
		writePreviewError(w, err)
		return
	}
	old := s.Store.NewsImagePath(r.Context(), id)
	n, err := s.Store.SetNewsImage(r.Context(), id, rel)
	if err != nil {
		s.Media.Remove(rel)
		writeNewsError(w, err)
		return
	}
	s.Media.Remove(old)
	writeJSON(w, http.StatusOK, n)
}

// DELETE /api/news/{id}/image - убрать картинку новости.
func (s *Server) handleDeleteNewsImage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	old := s.Store.NewsImagePath(r.Context(), id)
	n, err := s.Store.SetNewsImage(r.Context(), id, "")
	if err != nil {
		writeNewsError(w, err)
		return
	}
	s.Media.Remove(old)
	writeJSON(w, http.StatusOK, n)
}
