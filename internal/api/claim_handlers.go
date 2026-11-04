package api

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// POST /api/users/{id}/claim-link — организатор или модератор выдаёт ссылку активации для аккаунта-заглушки
// (в том числе после сброса пароля). Возвращает актуальный токен (стабильный, пока игрок не задал пароль).
// ?regenerate=1 — выпустить новый (прежний перестаёт работать). Модератор - только для игроков.
func (s *Server) handleCreateClaimLink(w http.ResponseWriter, r *http.Request) {
	target, _, ok := s.loadManagedAccount(w, r)
	if !ok {
		return
	}
	id := target.ID
	tok, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось сгенерировать токен")
		return
	}
	token, ok, err := s.Store.EnsureClaimToken(r.Context(), id, tok, r.URL.Query().Get("regenerate") == "1")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "аккаунт уже активирован или не является заглушкой")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// POST /api/users/{id}/reset-password - организатор или модератор сбрасывает пароль и получает ссылку на
// установку нового. Модератор - только у игроков. Свой пароль так не сбросить: остались бы без входа.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	target, actor, ok := s.loadManagedAccount(w, r)
	if !ok {
		return
	}
	id := target.ID
	if actor.ID == id {
		writeError(w, http.StatusConflict, "свой пароль сбросить нельзя")
		return
	}
	tok, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось сгенерировать токен")
		return
	}
	switch err := s.Store.ResetPassword(r.Context(), id, tok); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "пользователь не найден")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("сброс пароля: %s (%s), выполнил %s", target.Login, id, actor.Login)
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

// GET /api/claim/{token} — данные для страницы активации (ник аккаунта). Публичный.
func (s *Server) handleClaimInfo(w http.ResponseWriter, r *http.Request) {
	u, ok, err := s.Store.LookupClaimToken(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ошибка")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "ссылка недействительна или аккаунт уже активирован")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"login": u.Login, "displayName": u.DisplayName})
}

// POST /api/claim/{token} — активация: игрок задаёт пароль, получает сессию, токен гасится.
func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	u0, ok, err := s.Store.LookupClaimToken(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ошибка")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "ссылка недействительна или аккаунт уже активирован")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if len(body.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "пароль должен быть не короче 8 символов")
		return
	}
	if len(body.Password) > auth.MaxPasswordBytes {
		writeError(w, http.StatusBadRequest, "пароль слишком длинный (не более 72 байт)")
		return
	}
	if strings.EqualFold(strings.TrimSpace(body.Password), u0.Login) {
		writeError(w, http.StatusBadRequest, "пароль не должен совпадать с логином")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось обработать пароль")
		return
	}
	u, ok, err := s.Store.ClaimAccount(r.Context(), token, hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "ссылка недействительна или аккаунт уже активирован")
		return
	}
	jwt, err := auth.IssueToken(s.Cfg.JWTSecret, u.ID, string(u.Role), sessionTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось создать сессию")
		return
	}
	s.setSessionCookie(w, r, jwt)
	writeJSON(w, http.StatusOK, u)
}
