package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/battle-for-respect/backend/internal/media"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// statClaimDir - папка скриншотов с часами и ноками в хранилище медиа.
const statClaimDir = "claims"

// maxClaimReason - причина отказа стоит в профиле игрока одной строкой.
const maxClaimReason = 160

// maxPlayerSearch - сколько игроков показывать в подсказке при выборе тиммейтов.
const maxPlayerSearch = 10

// POST /api/me/stat-claims/{kind}?value=N - часы в игре или ноки за всё время: в теле скриншот из игры.
// Число сразу видно в профиле с пометкой «не подтверждено», пока модератор не сверит его.
func (s *Server) handleSubmitStatClaim(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	kind := chi.URLParam(r, "kind")
	limit, ok := store.StatLimits[kind]
	if !ok {
		writeError(w, http.StatusNotFound, "такого показателя нет")
		return
	}
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("value")))
	if err != nil || value < 0 || value > limit {
		writeError(w, http.StatusBadRequest, "нужно целое число от 0 до "+strconv.Itoa(limit))
		return
	}
	tok, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось сохранить скриншот")
		return
	}
	rel, err := s.Media.SaveImage(statClaimDir, tok, http.MaxBytesReader(w, r.Body, media.MaxImageBytes+1))
	if err != nil {
		writePreviewError(w, err)
		return
	}
	c, old, err := s.Store.SubmitStatClaim(r.Context(), u.ID, kind, value, rel)
	if err != nil {
		s.Media.Remove(rel)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Media.Remove(old)
	writeJSON(w, http.StatusOK, c)
}

// GET /api/stat-claims?status&limit&offset - очередь проверки скриншотов (модератор).
func (s *Server) handleListStatClaims(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && status != "pending" && status != "approved" && status != "rejected" {
		writeError(w, http.StatusBadRequest, "неизвестный статус")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, err := s.Store.ListStatClaims(r.Context(), status, limit, max(offset, 0))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// POST /api/stat-claims/{id}/decide {approve, reason} - модератор сверил число со скриншотом.
func (s *Server) handleDecideStatClaim(w http.ResponseWriter, r *http.Request) {
	mod, _ := userFrom(r.Context())
	var body struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if utf8.RuneCountInString(reason) > maxClaimReason {
		writeError(w, http.StatusBadRequest, "причина - не длиннее 160 символов")
		return
	}
	claim, err := s.Store.GetStatClaim(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "заявка не найдена")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if claim.UserID == mod.ID && !mod.Role.AtLeast(models.RoleSuperadmin) {
		writeError(w, http.StatusForbidden, "свой скриншот проверяет другой модератор или организатор")
		return
	}
	c, err := s.Store.DecideStatClaim(r.Context(), claim.ID, body.Approve, reason, mod.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "заявка не найдена")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// GET /api/favorites/options - из чего выбирать любимое (публично: списки одинаковые для всех).
func (s *Server) handleFavoriteOptions(w http.ResponseWriter, r *http.Request) {
	opts, err := s.Store.FavoriteOptions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

// PUT /api/me/favorites - любимое в своём профиле; заполнять его может подписчик и роли выше.
func (s *Server) handleSaveFavorites(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if !u.Role.AtLeast(models.RoleSubscriber) {
		name, _ := s.Store.SubscriberRoleName(r.Context())
		writeError(w, http.StatusForbidden, "любимое заполняют игроки с ролью «"+name+"»")
		return
	}
	var body struct {
		Teammates []string `json:"teammates"`
		MapCode   string   `json:"mapCode"`
		Weapon    string   `json:"weapon"`
		Augment   string   `json:"augment"`
		Shield    string   `json:"shield"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	err := s.Store.SaveFavorites(r.Context(), u.ID, store.FavoritesInput{
		Teammates: body.Teammates,
		MapCode:   strings.TrimSpace(body.MapCode),
		Weapon:    strings.TrimSpace(body.Weapon),
		Augment:   strings.TrimSpace(body.Augment),
		Shield:    strings.TrimSpace(body.Shield),
	})
	switch {
	case errors.Is(err, store.ErrBadFavorite):
		writeError(w, http.StatusBadRequest, "такого варианта нет в списке, а тиммейтов - не больше пяти")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusBadRequest, "среди тиммейтов есть игрок, которого на сайте больше нет")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	f, _, err := s.Store.GetFavorites(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// GET /api/player-search?q - игроки сайта по нику: подсказка при выборе любимых тиммейтов.
func (s *Server) handleSearchPlayers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, []models.TeamMember{})
		return
	}
	items, err := s.Store.SearchPlayers(r.Context(), q, maxPlayerSearch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// GET /api/me/extras - свой профиль: заявки с часами и ноками, любимое и можно ли его заполнять.
func (s *Server) handleMyExtras(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	ctx := r.Context()
	claims, err := s.Store.MyStatClaims(ctx, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fav, _, err := s.Store.GetFavorites(ctx, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, models.MyExtras{
		Claims:       claims,
		Favorites:    fav,
		CanFavorites: u.Role.AtLeast(models.RoleSubscriber),
	})
}
