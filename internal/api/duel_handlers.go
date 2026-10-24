package api

import (
	"net/http"

	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// POST /api/tournaments/{id}/duel - после всех рейдов счёт равный: стороны играют дуэль до первого нока,
// победителю ведущий потом ставит +1 ручными очками. Отметка в журнале: её снимает «Отменить».
func (s *Server) handleMatchDuel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.matchGuard(w, r, id) {
		return
	}
	st, err := s.Store.GetMatchState(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "матч не найден")
		return
	}
	if st.Stage != "round" {
		writeError(w, http.StatusConflict, "раунд не идёт")
		return
	}
	if store.MatchDuel(st) {
		s.writeMatch(w, r, id, http.StatusOK)
		return
	}
	totals := map[string]int{}
	for _, sc := range st.Scores {
		totals[sc.ParticipantID] += sc.Points
	}
	if len(st.Tournament.Participants) != 2 || totals[st.Tournament.Participants[0].ID] != totals[st.Tournament.Participants[1].ID] {
		writeError(w, http.StatusConflict, "дуэль - только при равном счёте")
		return
	}
	if err := s.Store.AddMatchLog(r.Context(), id, st.CurrentRound, nil, store.DuelLogKind, "Дуэль до первого нока", 0,
		map[string]any{"type": store.DuelLogKind}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}
