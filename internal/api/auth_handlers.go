package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/battle-for-respect/backend/internal/models"
)

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Server) handleTwitchLogin(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.TwitchClientID == "" {
		writeError(w, http.StatusInternalServerError, "Twitch OAuth не настроен (TWITCH_CLIENT_ID)")
		return
	}
	state, err := randomState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось сгенерировать OAuth state")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((10 * time.Minute).Seconds()),
	})
	http.Redirect(w, r, s.OAuth.AuthCodeURL(state), http.StatusFound)
}

func (s *Server) handleTwitchCallback(w http.ResponseWriter, r *http.Request) {
	stateC, err := r.Cookie(stateCookie)
	if err != nil || stateC.Value == "" || stateC.Value != r.URL.Query().Get("state") {
		writeError(w, http.StatusBadRequest, "неверный OAuth state")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "отсутствует code")
		return
	}

	token, err := s.OAuth.Exchange(r.Context(), code)
	if err != nil {
		writeError(w, http.StatusBadGateway, "не удалось обменять код Twitch")
		return
	}
	tu, err := auth.FetchTwitchUser(r.Context(), s.Cfg.TwitchClientID, token.AccessToken)
	if err != nil {
		writeError(w, http.StatusBadGateway, "не удалось получить профиль Twitch")
		return
	}

	defaultRole := models.RoleViewer
	if s.Cfg.IsOrganizerLogin(tu.Login) {
		defaultRole = models.RoleOrganizer
	}

	u, err := s.Store.UpsertTwitchUser(r.Context(), tu.ID, tu.Login, tu.DisplayName, tu.ProfileImageURL, tu.Email, defaultRole)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось сохранить пользователя")
		return
	}
	// Доводим роль до organizer для бутстрап-логинов, даже если пользователь уже существовал.
	if s.Cfg.IsOrganizerLogin(tu.Login) && u.Role != models.RoleOrganizer {
		if err := s.Store.SetRole(r.Context(), u.ID, models.RoleOrganizer); err == nil {
			u.Role = models.RoleOrganizer
		}
	}

	jwtToken, err := auth.IssueToken(s.Cfg.JWTSecret, u.ID, string(u.Role), sessionTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось создать сессию")
		return
	}
	s.setSessionCookie(w, jwtToken)
	http.Redirect(w, r, s.Cfg.FrontendURL, http.StatusFound)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
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
	updated, err := s.Store.UpdateEmbarkID(r.Context(), u.ID, body.EmbarkID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось обновить профиль")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
