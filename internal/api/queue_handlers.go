package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
)

// maxQueueTitle - название сессии показывается заголовком страницы: длиннее не помещается.
const maxQueueTitle = 100

// queueSelfLimit - сколько действий очередью игрок делает за минуту: запись/выход туда-сюда не должны засорять журнал.
const queueSelfLimit = 30

// publishQueue сообщает всем открытым страницам, что очередь изменилась. Клиенты перечитывают состояние сами:
// кадр можно потерять (медленный клиент), но следующий опрос или кадр вернёт актуальное.
func (s *Server) publishQueue(res store.QueueResult) {
	msg, err := json.Marshal(map[string]any{"type": "queue", "sessionId": res.SessionID, "format": res.Format, "rev": res.Rev})
	if err != nil {
		return
	}
	s.Hub.Broadcast(msg)
}

func writeQueueError(w http.ResponseWriter, err error) {
	var qe *store.QueueError
	msg := ""
	if errors.As(err, &qe) {
		msg = qe.Msg
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		if msg == "" {
			msg = "Не найдено"
		}
		writeError(w, http.StatusNotFound, msg)
	case errors.Is(err, store.ErrQueueInvalid):
		writeError(w, http.StatusBadRequest, msg)
	case errors.Is(err, store.ErrQueueForbidden):
		writeError(w, http.StatusForbidden, msg)
	case errors.Is(err, store.ErrQueueState), errors.Is(err, store.ErrQueueStale):
		writeError(w, http.StatusConflict, msg)
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// viewerID - id вошедшего игрока из сессии (никогда из тела запроса) или пусто.
func viewerID(r *http.Request) string {
	if u, ok := userFrom(r.Context()); ok {
		return u.ID
	}
	return ""
}

// queueAnswer отвечает свежим состоянием очереди: после действия интерфейс не ждёт отдельного запроса.
func (s *Server) queueAnswer(w http.ResponseWriter, r *http.Request, res store.QueueResult) {
	s.publishQueue(res)
	snap, err := s.Store.QueueSnapshotFor(r.Context(), res.SessionID, viewerID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, snap)
}

// GET /api/queue?format=1x1|2x2 - состояние живой очереди (без format - любой формат, 1×1 первым).
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format != "" && format != "1x1" && format != "2x2" {
		writeError(w, http.StatusBadRequest, "формат очереди — 1x1 или 2x2")
		return
	}
	id, err := s.Store.QueueActiveID(r.Context(), format)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeQueueSnapshot(w, r, id)
}

// GET /api/queue/sessions/{id} - состояние любой сессии, в том числе завершённой.
func (s *Server) handleQueueSession(w http.ResponseWriter, r *http.Request) {
	s.writeQueueSnapshot(w, r, chi.URLParam(r, "id"))
}

func (s *Server) writeQueueSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	snap, err := s.Store.QueueSnapshotFor(r.Context(), id, viewerID(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Сессия очереди не найдена")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, snap)
}

// GET /api/queue/sessions - все сессии очереди, новые сверху.
func (s *Server) handleListQueueSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListQueueSessions(r.Context(), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, list)
}

// GET /api/queue/sessions/{id}/events - журнал сессии: вызовы, пропуски, действия организатора.
func (s *Server) handleQueueEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Store.QueueEvents(r.Context(), chi.URLParam(r, "id"), 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, events)
}

// selfLimited отсекает слишком частые действия одного игрока.
func (s *Server) selfLimited(w http.ResponseWriter, userID string) bool {
	if s.queueLimiter.blocked(userID) {
		writeError(w, http.StatusTooManyRequests, "Слишком часто — подождите минуту")
		return true
	}
	s.queueLimiter.inc(userID)
	return false
}

// POST /api/queue/sessions/{id}/join {teamKey?} - игрок записывается сам; кто он - берётся из входа, не из запроса.
func (s *Server) handleQueueJoin(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if s.selfLimited(w, u.ID) {
		return
	}
	var b struct {
		TeamKey string `json:"teamKey"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &b); err != nil {
			writeError(w, http.StatusBadRequest, "некорректный JSON")
			return
		}
	}
	res, err := s.Store.JoinQueue(r.Context(), chi.URLParam(r, "id"), u.ID, store.QueueJoin{UserID: u.ID, TeamKey: strings.TrimSpace(b.TeamKey)})
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/leave - игрок выходит из очереди.
func (s *Server) handleQueueLeave(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if s.selfLimited(w, u.ID) {
		return
	}
	res, err := s.Store.LeaveQueue(r.Context(), chi.URLParam(r, "id"), u.ID)
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/confirm - игрок подтверждает готовность на вызов.
func (s *Server) handleQueueConfirm(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if s.selfLimited(w, u.ID) {
		return
	}
	res, err := s.Store.ConfirmReady(r.Context(), chi.URLParam(r, "id"), u.ID)
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// ---------- Организатор ----------

// actorID - организатор, выполняющий действие (для журнала).
func actorID(r *http.Request) string { return viewerID(r) }

type queueSessionBody struct {
	Title             *string `json:"title"`
	Format            *string `json:"format"`
	ConfirmTimeoutSec *int    `json:"confirmTimeoutSec"`
	MatchSize         *int    `json:"matchSize"`
}

func (b *queueSessionBody) check() string {
	if b.Title != nil {
		t := strings.Join(strings.Fields(*b.Title), " ")
		b.Title = &t
		if n := utf8.RuneCountInString(t); n > maxQueueTitle {
			return "название — не длиннее 100 символов"
		}
	}
	return ""
}

// POST /api/queue/sessions {title?, format, confirmTimeoutSec?, matchSize?} - открыть очередь на стрим.
func (s *Server) handleCreateQueueSession(w http.ResponseWriter, r *http.Request) {
	var b queueSessionBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if msg := b.check(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	in := store.QueueSessionInput{}
	if b.Title != nil {
		in.Title = *b.Title
	}
	if b.Format != nil {
		in.Format = *b.Format
	}
	if b.ConfirmTimeoutSec != nil {
		in.ConfirmTimeoutSec = *b.ConfirmTimeoutSec
	}
	if b.MatchSize != nil {
		in.MatchSize = *b.MatchSize
	}
	res, err := s.Store.CreateQueueSession(r.Context(), actorID(r), in)
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// PATCH /api/queue/sessions/{id} - название, формат, время на подтверждение, размер матча.
func (s *Server) handleUpdateQueueSession(w http.ResponseWriter, r *http.Request) {
	var b queueSessionBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if msg := b.check(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	res, err := s.Store.UpdateQueueSession(r.Context(), chi.URLParam(r, "id"), actorID(r), store.QueueSessionPatch{
		Title: b.Title, Format: b.Format, ConfirmTimeoutSec: b.ConfirmTimeoutSec, MatchSize: b.MatchSize,
	})
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/mode {action: open|close|pause|resume} - запись игроков и вся очередь.
func (s *Server) handleQueueMode(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Action string `json:"action"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	res, err := s.Store.SetQueueMode(r.Context(), chi.URLParam(r, "id"), actorID(r), b.Action)
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/finish - завершить сессию; история остаётся.
func (s *Server) handleFinishQueueSession(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.FinishQueueSession(r.Context(), chi.URLParam(r, "id"), actorID(r))
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/call {entryId?} - вызвать следующего или конкретного участника (команду).
func (s *Server) handleQueueCall(w http.ResponseWriter, r *http.Request) {
	var b struct {
		EntryID string `json:"entryId"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &b); err != nil {
			writeError(w, http.StatusBadRequest, "некорректный JSON")
			return
		}
	}
	res, err := s.Store.CallEntry(r.Context(), chi.URLParam(r, "id"), actorID(r), strings.TrimSpace(b.EntryID))
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/entries {userId?, teamKey?} - добавить игрока или команду вручную.
func (s *Server) handleQueueAdd(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UserID  string `json:"userId"`
		TeamKey string `json:"teamKey"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	res, err := s.Store.JoinQueue(r.Context(), chi.URLParam(r, "id"), actorID(r), store.QueueJoin{
		UserID: strings.TrimSpace(b.UserID), TeamKey: strings.TrimSpace(b.TeamKey), ByOrganizer: true,
	})
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/entries/{entryId}/confirm - подтвердить готовность за вызванных.
func (s *Server) handleQueueConfirmEntry(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.ConfirmEntry(r.Context(), chi.URLParam(r, "id"), actorID(r), chi.URLParam(r, "entryId"))
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/entries/{entryId}/skip {mode: end|remove} - пропустить: в конец или из сессии.
func (s *Server) handleQueueSkip(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Mode string `json:"mode"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &b); err != nil {
			writeError(w, http.StatusBadRequest, "некорректный JSON")
			return
		}
	}
	res, err := s.Store.SkipEntry(r.Context(), chi.URLParam(r, "id"), actorID(r), chi.URLParam(r, "entryId"), b.Mode)
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/entries/{entryId}/return {position?} - вернуть пропущенного.
func (s *Server) handleQueueReturn(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Position int `json:"position"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &b); err != nil {
			writeError(w, http.StatusBadRequest, "некорректный JSON")
			return
		}
	}
	res, err := s.Store.ReturnEntry(r.Context(), chi.URLParam(r, "id"), actorID(r), chi.URLParam(r, "entryId"), b.Position)
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/entries/{entryId}/move {position, rev?} - переставить на позицию (с 1).
func (s *Server) handleQueueMove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Position int   `json:"position"`
		Rev      int64 `json:"rev"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	res, err := s.Store.MoveEntry(r.Context(), chi.URLParam(r, "id"), actorID(r), chi.URLParam(r, "entryId"), b.Position, b.Rev)
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// DELETE /api/queue/sessions/{id}/entries/{entryId} - убрать участника из очереди.
func (s *Server) handleQueueRemove(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.RemoveEntry(r.Context(), chi.URLParam(r, "id"), actorID(r), chi.URLParam(r, "entryId"))
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/match/start {force?} - выпустить вызванных в эфир.
func (s *Server) handleQueueMatchStart(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Force bool `json:"force"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &b); err != nil {
			writeError(w, http.StatusBadRequest, "некорректный JSON")
			return
		}
	}
	res, err := s.Store.StartQueueMatch(r.Context(), chi.URLParam(r, "id"), actorID(r), b.Force)
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/match/finish - выступление закончено: участники уходят в конец очереди.
func (s *Server) handleQueueMatchFinish(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.FinishQueueMatch(r.Context(), chi.URLParam(r, "id"), actorID(r))
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}

// POST /api/queue/sessions/{id}/match/cancel - отменить подготовку или эфир: вызванные ждут дальше.
func (s *Server) handleQueueMatchCancel(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.CancelQueueMatch(r.Context(), chi.URLParam(r, "id"), actorID(r))
	if err != nil {
		writeQueueError(w, err)
		return
	}
	s.queueAnswer(w, r, res)
}
