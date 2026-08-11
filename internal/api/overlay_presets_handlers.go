package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Тело запроса на создание/обновление пресета: имя, адрес для OBS-ссылки
// (необязателен — тогда берём из имени) и произвольный JSON раскладки.
type presetBody struct {
	Name   string          `json:"name"`
	Slug   string          `json:"slug"`
	Layout json.RawMessage `json:"layout"`
}

// slugFor — адрес пресета из явно заданного поля, иначе из названия.
// Пустой результат остаётся пустым: свободный адрес подберёт store.
func slugFor(b presetBody) string {
	if s := store.Slugify(b.Slug); s != "" {
		return s
	}
	return store.Slugify(b.Name)
}

// notifyPresetsChanged — сообщить открытым оверлеям, что пресеты изменились:
// бампим ревизию и рассылаем обычный конверт состояния (ревизия едет в нём).
// Страницы вида /overlay/<slug> по смене ревизии перечитывают свою раскладку,
// поэтому правки пресета видны в OBS без перезагрузки источника.
func (s *Server) notifyPresetsChanged(ctx context.Context) {
	s.presetsRev.Add(1)
	if env, err := s.stateEnvelope(s.overlayStateBytes(ctx)); err == nil {
		s.Hub.Broadcast(env)
	}
}

func (s *Server) handleListOverlayPresets(w http.ResponseWriter, r *http.Request) {
	presets, err := s.Store.ListOverlayPresets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, presets)
}

// handleGetOverlayPreset — публичное чтение пресета по адресу из ссылки для OBS
// (или по id). Без авторизации: браузер-источник OBS ходит без сессии, а в
// раскладке нет ничего приватного.
func (s *Server) handleGetOverlayPreset(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetOverlayPresetByKey(r.Context(), chi.URLParam(r, "key"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "пресет не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCreateOverlayPreset(w http.ResponseWriter, r *http.Request) {
	var b presetBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(b.Name) == "" {
		writeError(w, http.StatusBadRequest, "укажите название пресета")
		return
	}
	if len(b.Layout) == 0 {
		b.Layout = json.RawMessage("{}")
	}
	p, err := s.Store.CreateOverlayPreset(r.Context(), strings.TrimSpace(b.Name), slugFor(b), b.Layout)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyPresetsChanged(r.Context())
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateOverlayPreset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var b presetBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if strings.TrimSpace(b.Name) == "" {
		writeError(w, http.StatusBadRequest, "укажите название пресета")
		return
	}
	if len(b.Layout) == 0 {
		b.Layout = json.RawMessage("{}")
	}
	// При обновлении адрес берём только явный: пустой = оставить прежний, иначе
	// перезапись раскладки меняла бы ссылку вслед за названием и ломала OBS.
	p, err := s.Store.UpdateOverlayPreset(r.Context(), id, strings.TrimSpace(b.Name), store.Slugify(b.Slug), b.Layout)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyPresetsChanged(r.Context())
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteOverlayPreset(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteOverlayPreset(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyPresetsChanged(r.Context())
	w.WriteHeader(http.StatusNoContent)
}
