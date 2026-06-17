package api

import (
	"net/http"

	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/battle-for-respect/backend/internal/config"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/battle-for-respect/backend/internal/ws"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/oauth2"
)

type Server struct {
	Cfg   config.Config
	Store *store.Store
	Hub   *ws.Hub
	OAuth *oauth2.Config
}

func New(cfg config.Config, st *store.Store, hub *ws.Hub) *Server {
	return &Server{
		Cfg:   cfg,
		Store: st,
		Hub:   hub,
		OAuth: auth.TwitchOAuth(cfg.TwitchClientID, cfg.TwitchClientSecret, cfg.TwitchRedirectURL),
	}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(s.cors)

	r.Route("/api", func(r chi.Router) {
		r.Use(s.injectUser)

		r.Get("/health", s.handleHealth)

		// --- Auth ---
		r.Get("/auth/twitch/login", s.handleTwitchLogin)
		r.Get("/auth/twitch/callback", s.handleTwitchCallback)
		r.Post("/auth/logout", s.handleLogout)

		// --- Public reads ---
		r.Get("/tournaments", s.handleListTournaments)
		r.Get("/tournaments/{id}", s.handleGetTournament)
		r.Get("/leaderboard", s.handleLeaderboard)
		r.Get("/players/{login}", s.handleGetPlayer)
		r.Get("/rules", s.handleRules)
		r.Get("/overlay/state", s.handleGetOverlayState)
		r.Get("/ws/overlay", s.handleOverlayWS)

		// --- Authenticated user ---
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/auth/me", s.handleMe)
			r.Patch("/me", s.handleUpdateMe)
			r.Get("/me/registrations", s.handleMyRegistrations)
			r.Post("/registrations", s.handleRegister)
		})

		// --- Organizer only ---
		r.Group(func(r chi.Router) {
			r.Use(s.requireOrganizer)
			r.Post("/tournaments", s.handleCreateTournament)
			r.Patch("/tournaments/{id}", s.handleUpdateTournament)
			r.Post("/tournaments/{id}/participants", s.handleAddParticipant)
			r.Post("/tournaments/{id}/rounds", s.handleCreateRound)
			r.Patch("/rounds/{id}", s.handleUpdateRound)
			r.Put("/rounds/{id}/entries/{participantId}", s.handleUpsertRoundEntry)
			r.Get("/rounds/{id}/entries", s.handleListRoundEntries)
			r.Patch("/participants/{id}", s.handleUpdateParticipant)
			r.Delete("/participants/{id}", s.handleRemoveParticipant)
			r.Get("/users", s.handleListUsers)
			r.Get("/registrations/pool", s.handleListPool)
			r.Post("/registrations/{id}/decide", s.handleDecideRegistration)
			r.Put("/overlay/state", s.handlePutOverlayState)

			// Справочник заданий и усложнений (редактирование организатором)
			r.Post("/catalog/tasks", s.handleCreateTask)
			r.Patch("/catalog/tasks/{id}", s.handleUpdateTask)
			r.Delete("/catalog/tasks/{id}", s.handleDeleteTask)
			r.Post("/catalog/complications", s.handleCreateComplication)
			r.Patch("/catalog/complications/{id}", s.handleUpdateComplication)
			r.Delete("/catalog/complications/{id}", s.handleDeleteComplication)

			// Стартовые задания: пул (скрыт от публики), распределение по раундам, зачёт в эфире.
			r.Get("/starter-tasks", s.handleListStarterTasks)
			r.Post("/starter-tasks", s.handleCreateStarterTask)
			r.Patch("/starter-tasks/{id}", s.handleUpdateStarterTask)
			r.Delete("/starter-tasks/{id}", s.handleDeleteStarterTask)
			r.Get("/tournaments/{id}/starter-tasks", s.handleListTournamentStarterTasks)
			r.Post("/rounds/{id}/starter-tasks", s.handleAssignRoundTask)
			r.Delete("/round-starter-tasks/{id}", s.handleUnassignRoundTask)
			r.Post("/round-starter-tasks/{id}/count", s.handleAdjustRoundTaskCount)

			// Штрафы-усложнения участнику в раунде (счётчик применений).
			r.Get("/tournaments/{id}/penalties", s.handleListTournamentPenalties)
			r.Post("/rounds/{id}/penalties/count", s.handleAdjustRoundPenalty)

			// Бонусные задания участников по раундам (выбор + отметка выполнения, перенос).
			r.Get("/tournaments/{id}/bonus-tasks", s.handleListTournamentBonusTasks)
			r.Post("/rounds/{id}/bonus-tasks", s.handleAssignBonusTask)
			r.Post("/round-bonus-tasks/{id}/count", s.handleAdjustBonusCount)
			r.Delete("/round-bonus-tasks/{id}", s.handleRemoveBonusTask)
		})
	})

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"overlayConns": s.Hub.Count(),
	})
}
