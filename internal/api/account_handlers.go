package api

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// canManageAccount: модератор правит аккаунты только игроков. Аккаунт модератора или организатора
// он не трогает: сбросив чужой пароль по ссылке, он сам вошёл бы под этим аккаунтом и поднял себе права.
func canManageAccount(actor *models.User, target models.User) bool {
	if actor.Role == models.RoleSuperadmin {
		return true
	}
	return !target.Role.AtLeast(models.RoleModerator)
}

// loadManagedAccount находит аккаунт из пути запроса и проверяет, что вызывающий вправе им распоряжаться.
// ok=false - ответ с ошибкой уже записан.
func (s *Server) loadManagedAccount(w http.ResponseWriter, r *http.Request) (target models.User, actor *models.User, ok bool) {
	actor, has := userFrom(r.Context())
	if !has {
		writeError(w, http.StatusUnauthorized, "требуется вход")
		return models.User{}, nil, false
	}
	target, err := s.Store.GetUser(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "пользователь не найден")
		return models.User{}, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось загрузить пользователя")
		return models.User{}, nil, false
	}
	if !canManageAccount(actor, target) {
		writeError(w, http.StatusForbidden, "аккаунтами модераторов и организаторов управляет только организатор")
		return models.User{}, nil, false
	}
	return target, actor, true
}

// validateNick проверяет ник по правилам регистрации и возвращает очищенное значение и текст ошибки.
func validateNick(raw string) (nick, msg string) {
	nick = strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(nick); n < nickMinLen || n > nickMaxLen {
		return "", "ник: от 3 до 12 символов"
	}
	if !nickRe.MatchString(nick) {
		return "", "ник: только буквы и цифры, без пробелов и знаков, не начинается с цифры"
	}
	return nick, ""
}

// PATCH /api/users/{id}/nick - принудительная смена ника: он станет и логином, и именем на сайте.
// Организатора переименовать нельзя: его аккаунт опознаётся по логину из настроек сервера.
func (s *Server) handleRenameUser(w http.ResponseWriter, r *http.Request) {
	target, actor, ok := s.loadManagedAccount(w, r)
	if !ok {
		return
	}
	if target.Role == models.RoleSuperadmin {
		writeError(w, http.StatusConflict, "ник организатора менять нельзя")
		return
	}
	var body struct {
		Nick string `json:"nick"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	nick, msg := validateNick(body.Nick)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	updated, err := s.Store.RenameUser(r.Context(), target.ID, nick)
	switch {
	case errors.Is(err, store.ErrNickTaken):
		writeError(w, http.StatusConflict, "ник уже занят")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "пользователь не найден")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "не удалось сменить ник")
		return
	}
	log.Printf("смена ника: %s (%s) -> %s, выполнил %s", target.Login, target.ID, updated.Login, actor.Login)
	writeJSON(w, http.StatusOK, updated)
}

// DELETE /api/users/{id} - удаляет пустой профиль. Игрока с историей не трогаем: сервер вернёт причину.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	target, actor, ok := s.loadManagedAccount(w, r)
	if !ok {
		return
	}
	if target.ID == actor.ID {
		writeError(w, http.StatusConflict, "свой профиль удалить нельзя")
		return
	}
	if target.Role == models.RoleSuperadmin {
		writeError(w, http.StatusConflict, "профиль организатора удалить нельзя")
		return
	}
	var ne *store.NotEmptyError
	switch err := s.Store.DeleteEmptyUser(r.Context(), target.ID); {
	case errors.As(err, &ne):
		writeError(w, http.StatusConflict, ne.Error())
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "пользователь не найден")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "не удалось удалить профиль")
		return
	}
	log.Printf("удалён пустой профиль: %s (%s), выполнил %s", target.Login, target.ID, actor.Login)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
