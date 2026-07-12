package api

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/go-chi/chi/v5"
)

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// POST /api/users/{id}/claim-link — организатор выдаёт ссылку активации для аккаунта-заглушки.
// Возвращает актуальный токен (стабильный, пока игрок не активировал аккаунт). ?regenerate=1 —
// выпустить новый (прежний перестаёт работать). Superadmin-only (см. server.go).
func (s *Server) handleCreateClaimLink(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
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
	s.setSessionCookie(w, jwt)
	writeJSON(w, http.StatusOK, u)
}
