package syncer

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Recomputer откладывает пересчёт MMR и нашивок: пока идёт поток записей, пересчёт не запускается
// на каждую, а ждёт паузы.
type Recomputer struct {
	Run    func() error
	Delay  time.Duration
	mu     sync.Mutex
	timer  *time.Timer
	failed error
}

// Touch сообщает, что рейтинг мог измениться.
func (r *Recomputer) Touch() {
	if r == nil || r.Run == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.Delay
	if d <= 0 {
		d = 5 * time.Second
	}
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(d, func() {
		err := r.Run()
		r.mu.Lock()
		r.failed = err
		r.mu.Unlock()
	})
}

// Handler - сторона сайта: принимает запросы приложения. Доступ по общему секрету в заголовке X-Sync-Token.
type Handler struct {
	Pool     *pgxpool.Pool
	Token    string
	MediaDir string
	// UpdateDir - папка с установщиком приложения и latest.json: приложение забирает оттуда обновления.
	UpdateDir string
	Recompute *Recomputer
	// OnApplied зовётся после записи в таблицы приложения организатора (например, сбросить кэши).
	OnApplied func(touched map[string]bool)
}

type helloResp struct {
	Proto     int      `json:"proto"`
	Migration int64    `json:"migration"`
	Tables    []string `json:"tables"`
}

type manifestReq struct {
	Tables []string `json:"tables"`
}

type rowsReq struct {
	Table string   `json:"table"`
	Keys  []string `json:"keys"`
}

type rowsResp struct {
	Rows map[string]json.RawMessage `json:"rows"`
}

type applyReq struct {
	Changes []TableChange `json:"changes"`
}

type doneReq struct {
	Tables []string `json:"tables"`
}

// MediaFile - файл из папки медиа.
type MediaFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Sync-Token")), []byte(h.Token)) != 1 {
		http.Error(w, `{"error":"нет доступа"}`, http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/hello":
		h.hello(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/manifest":
		h.manifest(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/digests":
		h.digests(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/rows":
		h.rows(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/apply":
		h.apply(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/done":
		h.done(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/schema":
		h.schema(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/logins":
		h.logins(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/update":
		h.update(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/update/file":
		h.updateFile(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/media":
		h.mediaList(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/media/file":
		h.mediaGet(w, r)
	case r.Method == http.MethodPut && r.URL.Path == "/media/file":
		h.mediaPut(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (h *Handler) hello(w http.ResponseWriter, r *http.Request) {
	var mig int64
	if err := h.Pool.QueryRow(r.Context(), `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&mig); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	names := make([]string, len(Tables))
	for i, t := range Tables {
		names[i] = t.Name
	}
	writeJSON(w, http.StatusOK, helloResp{Proto: ProtoVersion, Migration: mig, Tables: names})
}

func (h *Handler) manifest(w http.ResponseWriter, r *http.Request) {
	var req manifestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	tables := Tables
	if len(req.Tables) > 0 {
		tables = nil
		for _, n := range req.Tables {
			t, ok := tableByName(n)
			if !ok {
				fail(w, http.StatusBadRequest, errors.New("неизвестная таблица "+n))
				return
			}
			tables = append(tables, t)
		}
	}
	m, err := Manifest(r.Context(), h.Pool, tables)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (h *Handler) digests(w http.ResponseWriter, r *http.Request) {
	var req manifestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	tables := Tables
	if len(req.Tables) > 0 {
		tables = nil
		for _, n := range req.Tables {
			t, ok := tableByName(n)
			if !ok {
				fail(w, http.StatusBadRequest, errors.New("неизвестная таблица "+n))
				return
			}
			tables = append(tables, t)
		}
	}
	d, err := Digests(r.Context(), h.Pool, tables)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *Handler) rows(w http.ResponseWriter, r *http.Request) {
	var req rowsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	t, ok := tableByName(req.Table)
	if !ok {
		fail(w, http.StatusBadRequest, errors.New("неизвестная таблица"))
		return
	}
	rows, err := FetchRows(r.Context(), h.Pool, t, req.Keys)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rowsResp{Rows: rows})
}

func (h *Handler) apply(w http.ResponseWriter, r *http.Request) {
	var req applyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	res, err := Apply(r.Context(), h.Pool, req.Changes, true)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) done(w http.ResponseWriter, r *http.Request) {
	var req doneReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	touched := map[string]bool{}
	recompute := false
	for _, n := range req.Tables {
		touched[n] = true
		if recomputeTables[n] {
			recompute = true
		}
	}
	if h.OnApplied != nil {
		h.OnApplied(touched)
	}
	if recompute {
		h.Recompute.Touch()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// schema отдаёт команду создания таблицы: приложению нужны таблицы, которых нет в миграциях.
func (h *Handler) schema(w http.ResponseWriter, r *http.Request) {
	t, ok := tableByName(r.URL.Query().Get("table"))
	if !ok {
		fail(w, http.StatusBadRequest, errors.New("неизвестная таблица"))
		return
	}
	ddl, found, err := TableDDL(r.Context(), h.Pool, t.Name)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		fail(w, http.StatusNotFound, errors.New("таблицы нет"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ddl": ddl})
}

// logins отдаёт логины пользователей: приложение по ним находит дубли аккаунтов, заведённых офлайн.
func (h *Handler) logins(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Pool.Query(r.Context(), `SELECT id, lower(login) FROM users`)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, l string
		if err := rows.Scan(&id, &l); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		out[id] = l
	}
	writeJSON(w, http.StatusOK, out)
}

// ListMedia - все файлы папки медиа (без временных).
func ListMedia(dir string) ([]MediaFile, error) {
	var out []MediaFile
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".sync-") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, MediaFile{Path: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// safeMediaPath превращает относительный путь из запроса в путь внутри папки медиа.
func safeMediaPath(dir, rel string) (string, bool) {
	rel = filepath.FromSlash(rel)
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return "", false
	}
	full := filepath.Join(dir, rel)
	if !strings.HasPrefix(full, filepath.Clean(dir)+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}

func (h *Handler) mediaList(w http.ResponseWriter, r *http.Request) {
	files, err := ListMedia(h.MediaDir)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (h *Handler) mediaGet(w http.ResponseWriter, r *http.Request) {
	full, ok := safeMediaPath(h.MediaDir, r.URL.Query().Get("path"))
	if !ok {
		fail(w, http.StatusBadRequest, errors.New("плохой путь"))
		return
	}
	f, err := os.Open(full)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	http.ServeContent(w, r, filepath.Base(full), info.ModTime(), f)
}

// mediaPut принимает файл потоком во временный файл и переименовывает: недокачанный файл не остаётся.
func (h *Handler) mediaPut(w http.ResponseWriter, r *http.Request) {
	full, ok := safeMediaPath(h.MediaDir, r.URL.Query().Get("path"))
	if !ok {
		fail(w, http.StatusBadRequest, errors.New("плохой путь"))
		return
	}
	if _, err := os.Stat(full); err == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "exists"})
		return
	}
	if err := writeFileAtomic(full, r.Body); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func writeFileAtomic(full string, src io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".sync-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = io.Copy(tmp, src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, full)
}
