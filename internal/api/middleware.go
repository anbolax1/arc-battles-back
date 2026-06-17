package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/battle-for-respect/backend/internal/models"
)

const (
	sessionCookie = "rsp_session"
	stateCookie   = "rsp_oauth_state"
	sessionTTL    = 7 * 24 * time.Hour
)

// cors разрешает запросы с фронтенда с передачей cookie.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Vary: Origin — всегда (ответ зависит от Origin), иначе кэш/CDN на
		// split-инфраструктуре может отдать ответ без ACAO всем подряд.
		w.Header().Set("Vary", "Origin")
		origin := r.Header.Get("Origin")
		if origin != "" && origin == s.Cfg.FrontendURL {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// injectUser кладёт пользователя в контекст, если есть валидный токен (cookie или Bearer).
func (s *Server) injectUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if c, err := r.Cookie(sessionCookie); err == nil {
			token = c.Value
		}
		if token == "" {
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				token = strings.TrimPrefix(h, "Bearer ")
			}
		}
		if token != "" {
			if uid, _, err := auth.ParseToken(s.Cfg.JWTSecret, token); err == nil {
				if u, err := s.Store.GetUser(r.Context(), uid); err == nil {
					r = r.WithContext(withUser(r.Context(), &u))
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := userFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "требуется вход через Twitch")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireOrganizer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := userFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "требуется вход через Twitch")
			return
		}
		if u.Role != models.RoleOrganizer {
			writeError(w, http.StatusForbidden, "доступно только организатору")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// blockIfFinished пишет 409 и возвращает true, если турнир завершён — правки его данных
// (очки, задания, штрафы, участники, раунды) закрыты, пока его не вернут в другой статус.
func (s *Server) blockIfFinished(w http.ResponseWriter, status string) bool {
	if status == "finished" {
		writeError(w, http.StatusConflict, "турнир завершён — правки закрыты; верните его в эфир, чтобы менять")
		return true
	}
	return false
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Domain:   s.Cfg.CookieDomain,
		HttpOnly: true,
		Secure:   s.Cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		Domain:   s.Cfg.CookieDomain,
		HttpOnly: true,
		Secure:   s.Cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
