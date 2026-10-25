package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// maxTeamName - название стоит на табло оверлея рядом со счётом: длиннее не помещается, как и ник.
const maxTeamName = 32

type teamBody struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// check приводит название в порядок; состав проверяется, если он передан (у новой команды - всегда).
func (b *teamBody) check(newTeam bool) string {
	b.Name = strings.Join(strings.Fields(b.Name), " ")
	if n := utf8.RuneCountInString(b.Name); n < 2 || n > maxTeamName {
		return fmt.Sprintf("название команды — от 2 до %d символов", maxTeamName)
	}
	if !newTeam && b.Members == nil {
		return ""
	}
	if len(b.Members) != 2 {
		return "в команде два игрока"
	}
	for i := range b.Members {
		b.Members[i] = strings.TrimSpace(b.Members[i])
	}
	if b.Members[0] == "" || b.Members[0] == b.Members[1] {
		return "в команде два разных игрока"
	}
	return ""
}

func writeTeamError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "команда или игрок не найдены")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusBadRequest, "в команде два разных игрока")
	case errors.Is(err, store.ErrTeamPlayed):
		writeError(w, http.StatusConflict, "у команды уже есть матчи — состав не меняется, для другого состава заведите новую команду")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// writeTeamExists - у пары уже есть команда: в ответе её ключ, чтобы кабинет предложил выбрать её.
func (s *Server) writeTeamExists(w http.ResponseWriter, r *http.Request, key string) {
	resp := map[string]string{"error": "у этой пары игроков уже есть команда", "teamKey": key}
	if t, err := s.Store.GetMatchTeam(r.Context(), key); err == nil {
		resp["error"] = fmt.Sprintf("у этой пары игроков уже есть команда «%s»", t.Name)
	}
	writeJSON(w, http.StatusConflict, resp)
}

// GET /api/match-teams - команды для выбора сторон 2×2 (MMR и счёт текущего сезона).
func (s *Server) handleListMatchTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := s.Store.ListMatchTeams(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, teams)
}

// POST /api/teams {name, members: [userId, userId]} - новая команда 2×2.
func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	var b teamBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if msg := b.check(true); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	t, err := s.Store.CreateTeam(r.Context(), b.Name, b.Members[0], b.Members[1])
	if errors.Is(err, store.ErrTeamExists) {
		s.writeTeamExists(w, r, store.TeamKey(b.Members[0], b.Members[1]))
		return
	}
	if err != nil {
		writeTeamError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// PATCH /api/teams/{teamKey} {name, members?} - переименовать команду; состав меняется, пока у неё нет матчей.
func (s *Server) handleUpdateTeam(w http.ResponseWriter, r *http.Request) {
	var b teamBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if msg := b.check(false); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	t, renamed, err := s.Store.UpdateTeam(r.Context(), chi.URLParam(r, "teamKey"), b.Name, b.Members)
	if errors.Is(err, store.ErrTeamExists) {
		s.writeTeamExists(w, r, store.TeamKey(b.Members[0], b.Members[1]))
		return
	}
	if err != nil {
		writeTeamError(w, err)
		return
	}
	// Команда сейчас в эфире: табло сразу показывает новое название.
	if cur, err := s.Store.CurrentMatchID(r.Context()); err == nil && slices.Contains(renamed, cur) {
		s.publishMatchOverlay(r.Context(), cur)
	}
	writeJSON(w, http.StatusOK, t)
}

// DELETE /api/teams/{teamKey} - убрать команду, у которой ещё нет матчей.
func (s *Server) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	err := s.Store.DeleteTeam(r.Context(), chi.URLParam(r, "teamKey"))
	if errors.Is(err, store.ErrTeamPlayed) {
		writeError(w, http.StatusConflict, "у команды уже есть матчи — удалить её нельзя")
		return
	}
	if err != nil {
		writeTeamError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
