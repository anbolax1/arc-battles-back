package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleListTournaments(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	list, err := s.Store.ListTournaments(r.Context(), status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetTournament(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTournament(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "турнир не найден")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleCreateTournament(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title      string     `json:"title"`
		Mode       string     `json:"mode"`
		PlayerType string     `json:"playerType"`
		Maps       []string   `json:"maps"`
		StartsAt   *time.Time `json:"startsAt"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusBadRequest, "укажите название турнира")
		return
	}
	// Ровно 1 раунд на турнир — фиксируется в store (TotalRounds игнорируется).
	t, err := s.Store.CreateTournament(r.Context(), models.Tournament{
		Title:      strings.TrimSpace(body.Title),
		Mode:       body.Mode,
		PlayerType: body.PlayerType,
		Maps:       body.Maps,
		StartsAt:   body.StartsAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleUpdateTournament(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Status              *string         `json:"status"`
		WinnerParticipantID *string         `json:"winnerParticipantId"`
		Title               *string         `json:"title"`
		PlayerType          *string         `json:"playerType"`
		StartsAt            json.RawMessage `json:"startsAt"` // ключ присутствует → ставим (null = очистить); отсутствует → не трогаем
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}

	// Правка «шапки»: название, тип игроков и/или время начала.
	if body.Title != nil || body.PlayerType != nil || body.StartsAt != nil {
		var title *string
		if body.Title != nil {
			t := strings.TrimSpace(*body.Title)
			if t == "" {
				writeError(w, http.StatusBadRequest, "название турнира не может быть пустым")
				return
			}
			title = &t
		}
		startsAtSet := false
		var startsAt *time.Time
		if body.StartsAt != nil {
			startsAtSet = true
			if string(body.StartsAt) != "null" {
				var ts time.Time
				if err := json.Unmarshal(body.StartsAt, &ts); err != nil {
					writeError(w, http.StatusBadRequest, "некорректное время начала")
					return
				}
				startsAt = &ts
			}
		}
		if err := s.Store.UpdateTournamentMeta(r.Context(), id, title, body.PlayerType, startsAtSet, startsAt); errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "турнир не найден")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if body.Status != nil {
		// В эфире одновременно только один турнир: при переводе в live остальные live → upcoming.
		if *body.Status == "live" {
			if err := s.Store.DemoteLiveExcept(r.Context(), id); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if _, err := s.Store.UpdateTournamentStatus(r.Context(), id, *body.Status); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Авто-победитель: при «завершён» — сторона с макс. очками (если не задан явно).
		// При возврате из «завершён» в другой статус — снимаем победителя и откатываем MMR.
		if *body.Status == "finished" {
			if body.WinnerParticipantID == nil {
				_ = s.Store.SetWinnerTop(r.Context(), id)
				_ = s.Store.ApplyTournamentMmr(r.Context(), id)
			}
		} else {
			_ = s.Store.RevertTournamentMmr(r.Context(), id)
			_ = s.Store.ClearWinner(r.Context(), id)
		}
	}
	if body.WinnerParticipantID != nil {
		if _, err := s.Store.SetTournamentWinner(r.Context(), id, *body.WinnerParticipantID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Явный победитель → турнир завершён → пересчитываем MMR.
		_ = s.Store.ApplyTournamentMmr(r.Context(), id)
	}
	t, err := s.Store.GetTournament(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "турнир не найден")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// DELETE /api/tournaments/{id} — удалить турнир и всё связанное (раунды, участники,
// результаты, назначенные задания и усложнения раундов). Пулы каталога/стартовых заданий
// НЕ трогаются; поставленные игроки возвращаются в пул заявок. Organizer-only.
func (s *Server) handleDeleteTournament(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.DeleteTournament(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "турнир не найден")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAddParticipant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if st, err := s.Store.TournamentStatus(r.Context(), id); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var body struct {
		Kind    string          `json:"kind"`
		Name    string          `json:"name"`
		UserID  *string         `json:"userId"`
		Seed    int             `json:"seed"`
		Members json.RawMessage `json:"members"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "укажите имя участника")
		return
	}
	p, err := s.Store.AddParticipant(r.Context(), models.Participant{
		TournamentID: id,
		Kind:         body.Kind,
		Name:         strings.TrimSpace(body.Name),
		UserID:       body.UserID,
		Seed:         body.Seed,
		Members:      body.Members,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Если игрока поставили из заявки — заявка принята и уходит из пула.
	// Для 2x2 проверяем и капитана (UserID), и аккаунты в составе команды (members).
	for _, uid := range participantUserIDs(body.UserID, body.Members) {
		_ = s.Store.MarkRegistrationPlaced(r.Context(), uid, id)
	}
	writeJSON(w, http.StatusCreated, p)
}

// participantUserIDs собирает аккаунты, связанные с участником: одиночный
// userId плюс userId членов команды из members (для 2x2).
func participantUserIDs(userID *string, members json.RawMessage) []string {
	out := []string{}
	if userID != nil && *userID != "" {
		out = append(out, *userID)
	}
	if len(members) > 0 {
		var ms []struct {
			UserID string `json:"userId"`
		}
		if json.Unmarshal(members, &ms) == nil {
			for _, m := range ms {
				if m.UserID != "" {
					out = append(out, m.UserID)
				}
			}
		}
	}
	return out
}

// B1: частичная правка участника (очки/имя/состав). Organizer-only.
func (s *Server) handleUpdateParticipant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if st, err := s.Store.StatusByParticipant(r.Context(), id); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var body struct {
		TotalPoints *int            `json:"totalPoints"`
		Name        *string         `json:"name"`
		Seed        *int            `json:"seed"`
		UserID      *string         `json:"userId"`
		Members     json.RawMessage `json:"members"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	p, err := s.Store.UpdateParticipant(r.Context(), id, store.ParticipantUpdate{
		TotalPoints: body.TotalPoints,
		Name:        body.Name,
		Seed:        body.Seed,
		UserID:      body.UserID,
		Members:     body.Members,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "участник не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleRemoveParticipant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if st, err := s.Store.StatusByParticipant(r.Context(), id); err == nil && s.blockIfFinished(w, st) {
		return
	}
	// До удаления запоминаем связанные аккаунты, чтобы вернуть их заявки в пул.
	part, _ := s.Store.GetParticipant(r.Context(), id)
	if err := s.Store.RemoveParticipant(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, uid := range participantUserIDs(part.UserID, part.Members) {
		_ = s.Store.RevertRegistrationToPool(r.Context(), uid, part.TournamentID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateRound(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if st, err := s.Store.TournamentStatus(r.Context(), id); err == nil && s.blockIfFinished(w, st) {
		return
	}
	var body struct {
		Number int    `json:"number"`
		Map    string `json:"map"`
		Status string `json:"status"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	round, err := s.Store.CreateRound(r.Context(), models.Round{
		TournamentID: id,
		Number:       body.Number,
		Map:          body.Map,
		Status:       body.Status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, round)
}
