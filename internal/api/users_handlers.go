package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// GET /api/users — список пользователей для выпадашек кабинета (выбор участников).
// Organizer-only. Email не сериализуется (json:"-" в модели).
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// GET /api/users/overview?limit&offset&q&sort — пользователи с агрегатами участия,
// постранично. Organizer-only. В отличие от /users, отдаёт email и статистику участия.
func (s *Server) handleListUsersOverview(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, total, err := s.Store.ListUsersOverview(r.Context(), limit, offset,
		r.URL.Query().Get("q"), r.URL.Query().Get("sort"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// GET /api/players/{login} — публичный профиль игрока: user + сезон-статы + история (B6).
func (s *Server) handleGetPlayer(w http.ResponseWriter, r *http.Request) {
	login := chi.URLParam(r, "login")
	u, err := s.Store.GetUserByLogin(r.Context(), login)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "игрок не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	history, err := s.Store.PlayerHistory(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var points, wins, tournaments int
	for _, h := range history {
		if h.Status == "finished" {
			points += h.Points
			tournaments++
			if h.Win {
				wins++
			}
		}
	}
	writeJSON(w, http.StatusOK, models.PlayerProfile{
		User:        u,
		Points:      points,
		Wins:        wins,
		Tournaments: tournaments,
		History:     history,
	})
}
