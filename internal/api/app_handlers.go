package api

import (
	"crypto/subtle"
	"net/http"

	"github.com/battle-for-respect/backend/internal/models"
)

// appAccess пускает лаунчер по его секрету, остальных - только организатора.
func (s *Server) appAccess(next http.Handler) http.Handler {
	organizer := s.requireRole(models.RoleSuperadmin)(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t := s.Cfg.AppToken; t != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-App-Token")), []byte(t)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		organizer.ServeHTTP(w, r)
	})
}

// GET /api/app/sync - состояние синхронизации с сайтом.
func (s *Server) handleAppSyncStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Sync.Status())
}

// POST /api/app/sync/run - провести синхронизацию сейчас, не дожидаясь таймера.
func (s *Server) handleAppSyncRun(w http.ResponseWriter, r *http.Request) {
	s.Sync.Trigger()
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

// GET /api/app/busy - идёт ли матч: пока он идёт, лаунчер не перезапускает приложение для обновления.
func (s *Server) handleAppBusy(w http.ResponseWriter, r *http.Request) {
	var live bool
	err := s.Store.Pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM tournaments WHERE status = 'live')`).Scan(&live)
	if err != nil {
		// Не знаем наверняка - считаем, что занято: обновление подождёт.
		live = true
	}
	writeJSON(w, http.StatusOK, map[string]bool{"live": live})
}

// OnSynced сообщает серверу, что с сайта приехали строки: пресеты оверлея перечитываются.
func (s *Server) OnSynced(touched map[string]bool) {
	if touched["overlay_presets"] {
		s.presetsRev.Add(1)
	}
}
