package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

const maxUploadBytes = 200 << 20 // 200 МБ на загружаемый файл

func ptrOrNil(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func isTwitchClipURL(u string) bool {
	u = strings.ToLower(strings.TrimSpace(u))
	return strings.Contains(u, "clips.twitch.tv/") ||
		(strings.Contains(u, "twitch.tv/") && strings.Contains(u, "/clip/"))
}

// POST /api/highlights — создать хайлайт. Два режима:
//   - JSON {twitchClipUrl, tournamentId?, title?} — клип скачивается к нам в фоне;
//   - multipart/form-data (file, title?, tournamentId?, sourceUrl?) — загрузка своего файла.
//
// Доступно авторизованным (через Twitch). Публикуется после модерации (status pending→approved).
func (s *Server) handleCreateHighlight(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		s.createHighlightUpload(w, r, u.ID)
		return
	}

	var body struct {
		TwitchClipURL string `json:"twitchClipUrl"`
		TournamentID  string `json:"tournamentId"`
		Title         string `json:"title"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	clipURL := strings.TrimSpace(body.TwitchClipURL)
	if !isTwitchClipURL(clipURL) {
		writeError(w, http.StatusBadRequest, "укажите ссылку на твич-клип (clips.twitch.tv/… или twitch.tv/…/clip/…)")
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = "Хайлайт"
	}
	h, err := s.Store.CreateHighlight(r.Context(), u.ID, ptrOrNil(body.TournamentID),
		title, "twitch_clip", clipURL, "", "", 0, "processing")
	if err != nil {
		writeError(w, http.StatusBadRequest, "не удалось создать хайлайт: "+err.Error())
		return
	}

	// Скачиваем клип в фоне (своим контекстом — запрос уже завершится).
	go func(id, url string) {
		file, thumb, dur, derr := s.Media.DownloadClip(context.Background(), id, url)
		if derr != nil {
			log.Printf("highlight %s: скачивание клипа не удалось: %v", id, derr)
			_ = s.Store.SetHighlightFailed(context.Background(), id, "не удалось скачать клип")
			return
		}
		if e := s.Store.SetHighlightProcessed(context.Background(), id, file, thumb, dur); e != nil {
			log.Printf("highlight %s: сохранение результата: %v", id, e)
		}
	}(h.ID, clipURL)

	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) createHighlightUpload(w http.ResponseWriter, r *http.Request, userID string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "файл слишком большой или некорректная форма (макс. 200 МБ)")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "приложите видео-файл в поле file")
		return
	}
	defer file.Close()
	if ct := hdr.Header.Get("Content-Type"); !strings.HasPrefix(ct, "video/") {
		writeError(w, http.StatusBadRequest, "ожидается видео-файл")
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = "Хайлайт"
	}
	// Сначала заводим запись (нужен id для имени файла), затем сохраняем файл.
	h, err := s.Store.CreateHighlight(r.Context(), userID, ptrOrNil(r.FormValue("tournamentId")),
		title, "upload", strings.TrimSpace(r.FormValue("sourceUrl")), "", "", 0, "processing")
	if err != nil {
		writeError(w, http.StatusBadRequest, "не удалось создать хайлайт: "+err.Error())
		return
	}
	fpath, thumb, dur, serr := s.Media.SaveUpload(r.Context(), h.ID, file)
	if serr != nil {
		_ = s.Store.SetHighlightFailed(r.Context(), h.ID, "не удалось сохранить файл")
		writeError(w, http.StatusInternalServerError, "не удалось сохранить файл")
		return
	}
	if e := s.Store.SetHighlightProcessed(r.Context(), h.ID, fpath, thumb, dur); e != nil {
		writeError(w, http.StatusInternalServerError, e.Error())
		return
	}
	out, _ := s.Store.GetHighlight(r.Context(), h.ID)
	writeJSON(w, http.StatusCreated, out)
}

// GET /api/highlights?tournamentId&userId&limit&offset — публичный список одобренных.
func (s *Server) handleListHighlights(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	random := r.URL.Query().Get("random") == "1" || r.URL.Query().Get("random") == "true"
	items, total, err := s.Store.ListApprovedHighlights(r.Context(),
		r.URL.Query().Get("tournamentId"), r.URL.Query().Get("userId"), limit, offset, random)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// GET /api/highlights/moderation?status&limit&offset — очередь модерации. Organizer-only.
func (s *Server) handleListHighlightsModeration(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	if status == "all" {
		status = ""
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, total, err := s.Store.ListHighlightsByStatus(r.Context(), status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// POST /api/highlights/{id}/moderate {approve, reason} — одобрить/отклонить. Organizer-only.
func (s *Server) handleModerateHighlight(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var body struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	err := s.Store.ModerateHighlight(r.Context(), chi.URLParam(r, "id"), u.ID, body.Approve, strings.TrimSpace(body.Reason))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "хайлайт не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, _ := s.Store.GetHighlight(r.Context(), chi.URLParam(r, "id"))
	writeJSON(w, http.StatusOK, out)
}

// DELETE /api/highlights/{id} — удалить хайлайт и его файлы. Organizer-only.
func (s *Server) handleDeleteHighlight(w http.ResponseWriter, r *http.Request) {
	file, thumb, err := s.Store.DeleteHighlight(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "хайлайт не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Media.Remove(file, thumb)
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/media/* — раздача файлов хайлайтов из хранилища (с поддержкой range для перемотки).
func (s *Server) handleServeMedia(w http.ResponseWriter, r *http.Request) {
	rel := chi.URLParam(r, "*")
	full, ok := s.Media.Resolve(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, full)
}
