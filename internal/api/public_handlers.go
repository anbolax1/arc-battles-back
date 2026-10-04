package api

import (
	"context"
	"net/http"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/go-chi/chi/v5"
)

// handleGetTeam — публичная страница команды 2×2: состав + статистика + аналитика + динамика MMR.
func (s *Server) handleGetTeam(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "teamKey")
	tp, ok, err := s.Store.TeamProfile(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "команда не найдена")
		return
	}
	writeJSON(w, http.StatusOK, tp)
}

func (s *Server) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	if mode != "2x2" {
		mode = "1x1"
	}
	// season: "all" — за всё время; пусто — текущий активный сезон; иначе конкретный id.
	season := r.URL.Query().Get("season")
	seasonID := season
	if season == "all" {
		seasonID = ""
	} else if season == "" {
		if active, err := s.Store.ActiveSeason(r.Context()); err == nil {
			seasonID = active.ID
		} else {
			seasonID = "" // активного нет — показываем за всё время
		}
	}
	// 2×2 — рейтинг по КОМАНДАМ (пара игроков = команда с одним MMR); 1×1 — по игрокам.
	var rows any
	if mode == "2x2" {
		teams, err := s.Store.TeamLeaderboard(r.Context(), seasonID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ids := []string{}
		for _, t := range teams {
			for _, m := range t.Members {
				ids = append(ids, m.UserID)
			}
		}
		tags := s.siteTags(r.Context(), ids)
		for i := range teams {
			for j := range teams[i].Members {
				teams[i].Members[j].Tags = tags[teams[i].Members[j].UserID]
			}
		}
		rows = teams
	} else {
		players, err := s.Store.Leaderboard(r.Context(), mode, seasonID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ids := make([]string, len(players))
		for i, p := range players {
			ids[i] = p.UserID
		}
		tags := s.siteTags(r.Context(), ids)
		for i := range players {
			players[i].Tags = tags[players[i].UserID]
		}
		rows = players
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "seasonId": seasonID, "rows": rows})
}

// siteTags - теги игроков, которые видны на сайте, как в профиле. Без тегов таблица всё равно
// нужна, поэтому сбой чтения тегов не роняет ответ.
func (s *Server) siteTags(ctx context.Context, userIDs []string) map[string][]models.UserTag {
	tags, err := s.Store.TagsForUsers(ctx, userIDs, true)
	if err != nil {
		return map[string][]models.UserTag{}
	}
	return tags
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Store.ListCatalogTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	complications, err := s.Store.ListCatalogComplications(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	legendary, err := s.Store.ListLegendary(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":         tasks,         // контракты
		"complications": complications, // протоколы
		"legendary":     legendary,     // легендарные контракты
	})
}
