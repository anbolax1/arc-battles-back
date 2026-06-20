package api

import (
	"net/http"
	"time"

	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/battle-for-respect/backend/internal/config"
	"github.com/battle-for-respect/backend/internal/media"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/battle-for-respect/backend/internal/ws"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type Server struct {
	Cfg   config.Config
	Store *store.Store
	Hub   *ws.Hub
	Media *media.Processor

	// dummyHash — фиктивный bcrypt-хеш для выравнивания времени ответа на вход
	// несуществующего логина (защита от перебора пользователей по таймингу).
	dummyHash string
	// Троттлинг входа/регистрации (защита от перебора паролей и спама аккаунтов).
	loginIPLimiter   *limiter
	loginUserLimiter *limiter
	registerLimiter  *limiter
}

func New(cfg config.Config, st *store.Store, hub *ws.Hub) *Server {
	// Хеш считаем один раз при старте — его значение неважно, важна постоянная стоимость сравнения.
	dummy, _ := auth.HashPassword("placeholder-not-a-real-account-password")
	return &Server{
		Cfg:              cfg,
		Store:            st,
		Hub:              hub,
		Media:            media.NewProcessor(cfg.MediaDir, cfg.YtDlpPath, cfg.FfmpegPath, cfg.FfprobePath),
		dummyHash:        dummy,
		loginIPLimiter:   newLimiter(15, 5*time.Minute), // грубый предохранитель против долбёжки с одного IP
		loginUserLimiter: newLimiter(8, 15*time.Minute), // против перебора пароля к конкретному логину
		registerLimiter:  newLimiter(6, time.Hour),      // против массовой регистрации с одного IP
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

		// --- Auth (логин/пароль) ---
		r.Post("/auth/register", s.handleSignup)
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/logout", s.handleLogout)

		// --- Public reads ---
		r.Get("/tournaments", s.handleListTournaments)
		r.Get("/tournaments/{id}", s.handleGetTournament)
		r.Get("/leaderboard", s.handleLeaderboard)
		r.Get("/seasons", s.handleListSeasons)
		r.Get("/players/{login}", s.handleGetPlayer)
		r.Get("/rules", s.handleRules)
		r.Get("/overlay/state", s.handleGetOverlayState)
		r.Get("/ws/overlay", s.handleOverlayWS)
		r.Get("/highlights", s.handleListHighlights)
		r.Get("/media/*", s.handleServeMedia)

		// --- Authenticated user ---
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/auth/me", s.handleMe)
			r.Patch("/me", s.handleUpdateMe)
			r.Get("/me/registrations", s.handleMyRegistrations)
			r.Post("/registrations", s.handleRegister)
			r.Post("/highlights", s.handleCreateHighlight)
		})

		// --- Superadmin only (организатор) ---
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(models.RoleSuperadmin))
			r.Post("/tournaments", s.handleCreateTournament)
			r.Patch("/tournaments/{id}", s.handleUpdateTournament)
			r.Delete("/tournaments/{id}", s.handleDeleteTournament)
			r.Post("/tournaments/{id}/participants", s.handleAddParticipant)
			r.Post("/tournaments/{id}/rounds", s.handleCreateRound)
			r.Patch("/rounds/{id}", s.handleUpdateRound)
			r.Delete("/rounds/{id}", s.handleDeleteRound)
			r.Put("/rounds/{id}/entries/{participantId}", s.handleUpsertRoundEntry)
			r.Get("/rounds/{id}/entries", s.handleListRoundEntries)
			r.Patch("/participants/{id}", s.handleUpdateParticipant)
			r.Delete("/participants/{id}", s.handleRemoveParticipant)
			r.Get("/users", s.handleListUsers)
			r.Get("/users/overview", s.handleListUsersOverview)
			r.Patch("/users/{id}/role", s.handleSetUserRole)
			r.Get("/registrations/pool", s.handleListPool)
			r.Get("/registrations/pool/page", s.handleListPoolPage)
			r.Post("/registrations/{id}/decide", s.handleDecideRegistration)
			r.Put("/overlay/state", s.handlePutOverlayState)

			// Сезоны рейтинга: начать новый (завершает текущий активный); удалить (турниры отвязываются).
			r.Post("/seasons", s.handleStartSeason)
			r.Delete("/seasons/{id}", s.handleDeleteSeason)

			// Общие пресеты раскладки оверлея (сохранить/переключать шаблоны).
			r.Get("/overlay/presets", s.handleListOverlayPresets)
			r.Post("/overlay/presets", s.handleCreateOverlayPreset)
			r.Put("/overlay/presets/{id}", s.handleUpdateOverlayPreset)
			r.Delete("/overlay/presets/{id}", s.handleDeleteOverlayPreset)

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

			// Хайлайты: модерация (очередь, одобрить/отклонить, удалить).
			r.Get("/highlights/moderation", s.handleListHighlightsModeration)
			r.Post("/highlights/{id}/moderate", s.handleModerateHighlight)
			r.Delete("/highlights/{id}", s.handleDeleteHighlight)
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
