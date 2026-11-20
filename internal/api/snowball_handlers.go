package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// snowballTarget находит игрока по нику из адреса (прежний ник тоже подходит).
func (s *Server) snowballTarget(w http.ResponseWriter, r *http.Request) (models.User, bool) {
	u, err := s.Store.GetUserByLoginOrAlias(r.Context(), chi.URLParam(r, "login"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "игрок не найден")
		return u, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return u, false
	}
	return u, true
}

// handleGetSnowballs - сколько снежков в профиле с последнего матча игрока и кто бросал последним.
func (s *Server) handleGetSnowballs(w http.ResponseWriter, r *http.Request) {
	target, ok := s.snowballTarget(w, r)
	if !ok {
		return
	}
	sum, err := s.Store.SnowballSummary(r.Context(), target.ID, viewerID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store") // ответ зависит от того, кто смотрит
	writeJSON(w, http.StatusOK, sum)
}

// handleThrowSnowball - бросок снежка в профиль игрока; бросать может любой вошедший, не чаще раза в 15 секунд.
func (s *Server) handleThrowSnowball(w http.ResponseWriter, r *http.Request) {
	target, ok := s.snowballTarget(w, r)
	if !ok {
		return
	}
	err := s.Store.ThrowSnowball(r.Context(), viewerID(r), target.ID)
	var wait store.SnowballWaitError
	switch {
	case errors.Is(err, store.ErrSnowballSelf):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.As(err, &wait):
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sum, err := s.Store.SnowballSummary(r.Context(), target.ID, viewerID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, sum)
}
