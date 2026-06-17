package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// handleRegister — подача заявки в общий пул (без привязки к турниру).
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var body struct {
		EmbarkID string `json:"embarkId"`
		Note     string `json:"note"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	embark := strings.TrimSpace(body.EmbarkID)
	reg, err := s.Store.CreateRegistration(r.Context(), u.ID, embark, strings.TrimSpace(body.Note))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if embark != "" {
		_, _ = s.Store.UpdateEmbarkID(r.Context(), u.ID, embark)
	}
	writeJSON(w, http.StatusCreated, reg)
}

// handleListPool — открытые заявки (пул), кто подал раньше — выше. Organizer-only.
func (s *Server) handleListPool(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListPool(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleListPoolPage — пул заявок постранично: ?limit&offset → {items, total}. Organizer-only.
func (s *Server) handleListPoolPage(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, total, err := s.Store.ListPoolPage(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) handleMyRegistrations(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	list, err := s.Store.ListMyRegistrations(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleDecideRegistration — отклонить заявку из пула (или вернуть). Постановка
// игрока в турнир (accepted) выполняется автоматически при добавлении участника.
func (s *Server) handleDecideRegistration(w http.ResponseWriter, r *http.Request) {
	regID := chi.URLParam(r, "id")
	var body struct {
		Status string `json:"status"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if body.Status != "declined" && body.Status != "pending" {
		writeError(w, http.StatusBadRequest, "status должен быть declined или pending")
		return
	}
	reg, err := s.Store.DecideRegistration(r.Context(), regID, body.Status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, reg)
}
