package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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

// PATCH /api/users/{id}/role — назначить роль пользователю. Superadmin-only (см. server.go).
// Инвариант: нельзя снять роль у последнего организатора — иначе кабинет станет недоступен никому.
// Роль читается из БД на каждом запросе (injectUser), поэтому изменение действует сразу.
func (s *Server) handleSetUserRole(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Role string `json:"role"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	role := models.Role(strings.TrimSpace(body.Role))
	if !role.Valid() {
		writeError(w, http.StatusBadRequest, "неизвестная роль")
		return
	}

	target, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "пользователь не найден")
		return
	}

	// Понижение единственного организатора запрещено.
	if target.Role == models.RoleSuperadmin && role != models.RoleSuperadmin {
		n, err := s.Store.CountSuperadmins(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "не удалось проверить роли")
			return
		}
		if n <= 1 {
			writeError(w, http.StatusConflict, "нельзя снять роль у последнего организатора")
			return
		}
	}

	if err := s.Store.SetRole(r.Context(), id, role); err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось изменить роль")
		return
	}
	updated, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось загрузить пользователя")
		return
	}
	writeJSON(w, http.StatusOK, updated)
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
	// Источники очков и любимая карта — из стора (по participant-id завершённых турниров).
	stats, err := s.Store.PlayerAggregate(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Сводка и винрейт по режимам — из истории (только завершённые турниры).
	var points, wins, tournaments int
	for _, h := range history {
		if h.Status != "finished" {
			continue
		}
		points += h.Points
		tournaments++
		if h.Win {
			wins++
		}
		if h.Mode == "2x2" {
			stats.DuoPlayed++
			if h.Win {
				stats.DuoWins++
			}
		} else {
			stats.SoloPlayed++
			if h.Win {
				stats.SoloWins++
			}
		}
	}
	// Текущая серия — ведущий ран одинаковых исходов (история отсортирована по дате убыв.).
	for _, h := range history {
		if h.Status != "finished" {
			continue
		}
		kind := "loss"
		if h.Win {
			kind = "win"
		}
		if stats.StreakKind == "" {
			stats.StreakKind = kind
			stats.StreakLen = 1
		} else if kind == stats.StreakKind {
			stats.StreakLen++
		} else {
			break
		}
	}
	mmrSolo, _ := s.Store.GetUserMmr(r.Context(), u.ID, "1x1")
	mmrDuo, _ := s.Store.GetUserMmr(r.Context(), u.ID, "2x2")
	writeJSON(w, http.StatusOK, models.PlayerProfile{
		User:        u,
		MmrSolo:     mmrSolo,
		MmrDuo:      mmrDuo,
		Points:      points,
		Wins:        wins,
		Tournaments: tournaments,
		Stats:       stats,
		History:     history,
	})
}
