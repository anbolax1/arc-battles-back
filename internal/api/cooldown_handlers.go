package api

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// publishAllQueues - что-то вне очереди (правка кулдауна, удалённый матч) поменяло то, что видят её страницы:
// все живые очереди перечитывают состояние.
func (s *Server) publishAllQueues(ctx context.Context) {
	list, err := s.Store.ActiveQueueResults(ctx)
	if err != nil {
		log.Printf("очереди: %v", err)
		return
	}
	for _, res := range list {
		s.publishQueue(res)
	}
}

// afterMatchFinished - матч сайта завершён: у его игроков начался кулдаун и вырос счёт матчей за сегодня.
// Их записи в других живых очередях встают на новое место, страницы очередей перечитывают состояние.
func (s *Server) afterMatchFinished(ctx context.Context, tournamentID, actor string) {
	if _, err := s.Store.QueueAfterMatch(ctx, tournamentID, actor); err != nil {
		log.Printf("очереди после матча %s: %v", tournamentID, err)
	}
	s.publishAllQueues(ctx)
}

func writeCooldownError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrCooldownInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "игрок не найден")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// GET /api/cooldowns?q= - общая длительность и игроки: найденные по нику или все, у кого кулдаун идёт
// сейчас или заданы свои правила.
func (s *Server) handleCooldowns(w http.ResponseWriter, r *http.Request) {
	ov, err := s.Store.CooldownOverview(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, ov)
}

// GET /api/cooldowns/log?userId= - журнал правок кулдаунов, новые сверху.
func (s *Server) handleCooldownLog(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.CooldownLog(r.Context(), r.URL.Query().Get("userId"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, list)
}

// PUT /api/cooldowns/default {minutes} - общая длительность кулдауна; 0 - кулдаун выключен для всех без своей.
func (s *Server) handleSetCooldownDefault(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Minutes *int `json:"minutes"`
	}
	if err := readJSON(r, &b); err != nil || b.Minutes == nil {
		writeError(w, http.StatusBadRequest, "укажите длительность в минутах")
		return
	}
	if err := s.Store.SetCooldownDefault(r.Context(), actorID(r), *b.Minutes); err != nil {
		writeCooldownError(w, err)
		return
	}
	s.publishAllQueues(r.Context())
	s.handleCooldowns(w, r)
}

// POST /api/cooldowns/players/{id} {action, minutes?} - правка кулдауна игрока: своя длительность (duration),
// общая (default), отключить (disable), включить (enable), снять (clear), поставить вручную (set).
// Свой кулдаун модератор не меняет: иначе он сам себе снимал бы ограничение очереди.
func (s *Server) handleChangeCooldown(w http.ResponseWriter, r *http.Request) {
	actor, _ := userFrom(r.Context())
	id := chi.URLParam(r, "id")
	if id == actor.ID && !actor.Role.AtLeast(models.RoleSuperadmin) {
		writeError(w, http.StatusForbidden, "свой кулдаун меняет организатор")
		return
	}
	var b struct {
		Action  string `json:"action"`
		Minutes int    `json:"minutes"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	p, err := s.Store.ChangePlayerCooldown(r.Context(), actor.ID, id, b.Action, b.Minutes)
	if err != nil {
		writeCooldownError(w, err)
		return
	}
	s.publishAllQueues(r.Context())
	writeJSON(w, http.StatusOK, p)
}
