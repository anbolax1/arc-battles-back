package api

import (
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/battle-for-respect/backend/internal/store"
)

// loginRe — допустимый логин: латиница, цифры и подчёркивание, 3–32 символа.
// Узкий набор исключает пробелы, омоглифы и спецсимволы (меньше путаницы и сюрпризов).
var loginRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)

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

// Ограничения ника: длина в символах (не в байтах), только буквы и цифры, не с цифры.
const (
	nickMinLen = 3
	nickMaxLen = 12
)

// nickRe - буквы любых алфавитов (с диакритикой) и цифры; первым стоит буква.
var nickRe = regexp.MustCompile(`^\p{L}[\p{L}0-9]*$`)

// Подсказка игроку про уже заведённый профиль: двойники аккаунтов запрещены.
const profileExistsHint = "Если ты играл в лиге ранее, профиль уже создан: напиши в дискорд в чат «профиль на сайте»."

// signupBody - поля формы регистрации.
type signupBody struct {
	Login    string `json:"login"`
	Password string `json:"password"`
	Nick     string `json:"nick"`
	EmbarkID string `json:"embarkId"`
}

// validateSignup проверяет поля регистрации и возвращает очищенные значения
// и текст ошибки для игрока (пустой - всё в порядке).
func validateSignup(b signupBody) (login, nick, embark, msg string) {
	login = strings.TrimSpace(b.Login)
	nick = strings.TrimSpace(b.Nick)
	embark = strings.TrimSpace(b.EmbarkID)

	if !loginRe.MatchString(login) {
		return "", "", "", "логин: 3–32 символа, латиница, цифры и подчёркивание"
	}
	if len(b.Password) < minPasswordLen {
		return "", "", "", "пароль должен быть не короче 8 символов"
	}
	if len(b.Password) > auth.MaxPasswordBytes {
		return "", "", "", "пароль слишком длинный (не более 72 байт)"
	}
	if strings.EqualFold(strings.TrimSpace(b.Password), login) {
		return "", "", "", "пароль не должен совпадать с логином"
	}
	if n := utf8.RuneCountInString(nick); n < nickMinLen || n > nickMaxLen {
		return "", "", "", "ник: от 3 до 12 символов"
	}
	if !nickRe.MatchString(nick) {
		return "", "", "", "ник: только буквы и цифры, без пробелов и знаков, не начинается с цифры"
	}
	if !embarkRe.MatchString(embark) {
		return "", "", "", "Embark ID в формате Ник#1234 (ник, решётка и ровно 4 цифры)"
	}
	return login, nick, embark, ""
}

// handleSignup - регистрация по логину, паролю, нику и Embark ID. Создаёт обычного игрока,
// сразу заводит сессию. Роль выше user регистрацией не выдаётся. Лимит по IP считает
// каждую попытку, чтобы его нельзя было обойти перебором занятых имён.
func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.registerLimiter.blocked(ip) {
		writeError(w, http.StatusTooManyRequests, "слишком много регистраций, попробуйте позже")
		return
	}

	var body signupBody
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	login, nick, embark, msg := validateSignup(body)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось обработать пароль")
		return
	}

	s.registerLimiter.inc(ip)

	u, err := s.Store.CreateRegisteredUser(r.Context(), login, nick, embark, hash)
	switch {
	case errors.Is(err, store.ErrLoginTaken):
		writeError(w, http.StatusConflict, "логин уже занят")
		return
	case errors.Is(err, store.ErrNickTaken):
		writeError(w, http.StatusConflict, "ник уже занят. "+profileExistsHint)
		return
	case errors.Is(err, store.ErrEmbarkTaken):
		writeError(w, http.StatusConflict, "этот Embark ID уже привязан к профилю. "+profileExistsHint)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "не удалось создать пользователя")
		return
	}

	token, err := auth.IssueToken(s.Cfg.JWTSecret, u.ID, string(u.Role), sessionTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось создать сессию")
		return
	}
	s.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusCreated, u)
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
