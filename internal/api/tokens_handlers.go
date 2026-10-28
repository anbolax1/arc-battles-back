package api

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// maxTokenNote - за что выдан жетон, одной строкой в журнале.
const maxTokenNote = 120

// moderationHistory - сколько последних скриншотов и строк журнала жетонов видно на странице игрока.
const moderationHistory = 30

// tokenRuleErrors - правила жетонов, а не сбой: ответ 409 с понятной причиной.
var tokenRuleErrors = []error{
	store.ErrNoTokens, store.ErrTokenBalance, store.ErrTokenCooldown, store.ErrResetUsed, store.ErrNoSeason,
	store.ErrInMatch, store.ErrNothingToReset, store.ErrSideHasToken, store.ErrMatchStarted,
}

func isTokenRule(err error) bool {
	for _, e := range tokenRuleErrors {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func writeTokenError(w http.ResponseWriter, err error) {
	switch {
	case isTokenRule(err):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrNotInMatch):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "не найдено")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) writeWallet(w http.ResponseWriter, r *http.Request, userID string) {
	wallet, err := s.Store.TokenWallet(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wallet)
}

// POST /api/users/{id}/tokens {kind, delta, note} - модератор выдаёт жетоны или забирает выданные по ошибке.
func (s *Server) handleGrantTokens(w http.ResponseWriter, r *http.Request) {
	mod, _ := userFrom(r.Context())
	id := chi.URLParam(r, "id")
	var body struct {
		Kind  string `json:"kind"`
		Delta int    `json:"delta"`
		Note  string `json:"note"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if !store.ValidTokenKind(body.Kind) {
		writeError(w, http.StatusBadRequest, "неизвестный жетон")
		return
	}
	if id == mod.ID && !mod.Role.AtLeast(models.RoleSuperadmin) {
		writeError(w, http.StatusForbidden, "себе жетоны выдаёт организатор")
		return
	}
	if body.Delta == 0 || body.Delta > store.MaxTokenGrant || body.Delta < -store.MaxTokenGrant {
		writeError(w, http.StatusBadRequest, "за раз - от 1 до 100 жетонов")
		return
	}
	note := strings.TrimSpace(body.Note)
	if utf8.RuneCountInString(note) > maxTokenNote {
		writeError(w, http.StatusBadRequest, "комментарий - не длиннее 120 символов")
		return
	}
	if err := s.Store.GrantTokens(r.Context(), id, body.Kind, body.Delta, note, mod.ID); err != nil {
		writeTokenError(w, err)
		return
	}
	s.writeWallet(w, r, id)
}

// POST /api/users/{id}/tokens/priority - модератор активирует игроку «Без очереди» на сегодня.
func (s *Server) handleActivatePriority(w http.ResponseWriter, r *http.Request) {
	mod, _ := userFrom(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.Store.ActivatePriority(r.Context(), id, mod.ID); err != nil {
		writeTokenError(w, err)
		return
	}
	s.writeWallet(w, r, id)
}

// DELETE /api/users/{id}/tokens/priority - «Без очереди» активировали по ошибке: жетон возвращается.
func (s *Server) handleCancelPriority(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.CancelPriority(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusConflict, "сегодня «Без очереди» не активировали")
			return
		}
		writeTokenError(w, err)
		return
	}
	s.writeWallet(w, r, id)
}

// POST /api/tournaments/{id}/double {userId, on} - жетон ×2 игрока на матч, пока тот не начался.
func (s *Server) handleMatchDouble(w http.ResponseWriter, r *http.Request) {
	mod, _ := userFrom(r.Context())
	id := chi.URLParam(r, "id")
	var body struct {
		UserID string `json:"userId"`
		On     bool   `json:"on"`
	}
	if err := readJSON(r, &body); err != nil || body.UserID == "" {
		writeError(w, http.StatusBadRequest, "нужен игрок матча")
		return
	}
	if err := s.Store.SetDoubleToken(r.Context(), id, body.UserID, body.On, mod.ID); err != nil {
		writeTokenError(w, err)
		return
	}
	s.writeMatch(w, r, id, http.StatusOK)
}

// POST /api/me/reset - игрок сам тратит жетон сброса: MMR 1×1 в текущем сезоне снова стартовый.
func (s *Server) handleResetRating(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if err := s.Store.ResetRating(r.Context(), u.ID); err != nil {
		writeTokenError(w, err)
		return
	}
	s.writeWallet(w, r, u.ID)
}

// GET /api/users/{id}/moderation - для страницы игрока: скриншоты на проверке, журнал жетонов и матчи,
// на которые можно поставить жетон ×2.
func (s *Server) handlePlayerModeration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	var out models.PlayerModeration
	var err error
	if out.Claims, err = s.Store.UserStatClaims(ctx, id, moderationHistory); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out.Ledger, err = s.Store.TokenLedger(ctx, id, moderationHistory); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out.Matches, err = s.Store.PendingMatches(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
