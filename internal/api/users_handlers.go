package api

import (
	"errors"
	"net/http"

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
