package api

import (
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/battle-for-respect/backend/internal/store"
)

// embarkRe — Embark ID: ник (без «#»), решётка, ровно 4 цифры — напр. «Istwood#1234».
var embarkRe = regexp.MustCompile(`^[^#]+#\d{4}$`)

const minPasswordLen = 8

type credentials struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// clientIP возвращает IP клиента — ключ троттлинга. НЕ доверяем произвольным заголовкам
// (True-Client-IP, клиентский X-Forwarded-For легко подделать). За доверенным прокси
// (TRUST_PROXY=true) берём X-Real-IP, который nginx ставит из $remote_addr; иначе — реальный
// адрес TCP-соединения r.RemoteAddr.
func (s *Server) clientIP(r *http.Request) string {
	if s.Cfg.TrustProxy {
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// handleLogin — вход по логину/паролю. На неверные данные отвечает одинаково
// («неверный логин или пароль») и тратит время bcrypt даже при отсутствии пользователя,
// чтобы не раскрывать существование логина. Троттлинг по IP и по логину против перебора.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.loginIPLimiter.blocked(ip) {
		writeError(w, http.StatusTooManyRequests, "слишком много попыток входа, попробуйте позже")
		return
	}
	s.loginIPLimiter.inc(ip)

	var body credentials
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	login := strings.TrimSpace(body.Login)
	if login == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "укажите логин и пароль")
		return
	}

	loginKey := strings.ToLower(login)
	if s.loginUserLimiter.blocked(loginKey) {
		writeError(w, http.StatusTooManyRequests, "слишком много попыток входа, попробуйте позже")
		return
	}

	id, role, hash, err := s.Store.GetUserAuthByLogin(r.Context(), login)
	if errors.Is(err, store.ErrNotFound) {
		// Сравниваем с фиктивным хешем, чтобы время ответа не выдавало отсутствие логина.
		auth.CheckPassword(s.dummyHash, body.Password)
		s.loginUserLimiter.inc(loginKey)
		writeError(w, http.StatusUnauthorized, "неверный логин или пароль")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось выполнить вход")
		return
	}
	if !auth.CheckPassword(hash, body.Password) {
		s.loginUserLimiter.inc(loginKey)
		writeError(w, http.StatusUnauthorized, "неверный логин или пароль")
		return
	}

	// Успех — сбрасываем счётчик неудач по логину.
	s.loginUserLimiter.reset(loginKey)

	token, err := auth.IssueToken(s.Cfg.JWTSecret, id, string(role), sessionTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось создать сессию")
		return
	}
	s.setSessionCookie(w, r, token)

	u, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось загрузить профиль")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	u.Tags, _ = s.Store.UserTags(r.Context(), u.ID, true)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Сдвигаем эпоху сессий — текущий (и любой ранее выпущенный) токен становится недействителен
	// на сервере, а не только удаляется cookie в браузере.
	if u, ok := userFrom(r.Context()); ok {
		_ = s.Store.RevokeSessions(r.Context(), u.ID)
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var body struct {
		EmbarkID string `json:"embarkId"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	embark := strings.TrimSpace(body.EmbarkID)
	// Пусто — допустимо (очистить). Иначе строгий формат «Ник#1234».
	if embark != "" && !embarkRe.MatchString(embark) {
		writeError(w, http.StatusBadRequest, "Формат Embark ID: Ник#1234 (ник, решётка и ровно 4 цифры)")
		return
	}
	updated, err := s.Store.UpdateEmbarkID(r.Context(), u.ID, embark)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось обновить профиль")
		return
	}
	// Держим пул актуальным: подтягиваем новый Embark ID в незакрытую заявку игрока.
	_ = s.Store.SyncPendingRegistrationEmbark(r.Context(), u.ID, updated.EmbarkID)
	writeJSON(w, http.StatusOK, updated)
}
