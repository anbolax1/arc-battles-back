package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config - настройки приложения.
type Config struct {
	RemoteURL string
	Token     string
	Interval  time.Duration
	MediaDir  string
}

// Hooks - что приложение делает после переноса данных.
type Hooks struct {
	// Recompute пересчитывает MMR и нашивки по матчам.
	Recompute func(ctx context.Context) error
	// Pulled зовётся, когда с сайта приехали строки этих таблиц.
	Pulled func(touched map[string]bool)
}

// ConflictInfo - строка, которую изменили обе стороны.
type ConflictInfo struct {
	At     time.Time `json:"at"`
	Table  string    `json:"table"`
	Key    string    `json:"key"`
	Winner string    `json:"winner"` // app | site
}

// Status - состояние для лаунчера и админки.
type Status struct {
	Initialized  bool           `json:"initialized"`
	Running      bool           `json:"running"`
	Online       bool           `json:"online"`
	Remote       string         `json:"remote"`
	LastAttempt  *time.Time     `json:"lastAttempt"`
	LastSuccess  *time.Time     `json:"lastSuccess"`
	LastError    string         `json:"lastError"`
	PendingPush  int            `json:"pendingPush"`  // строк ждёт отправки на сайт
	PendingPull  int            `json:"pendingPull"`  // строк ждёт загрузки с сайта
	MediaPush    int            `json:"mediaPush"`    // файлов ждёт отправки
	MediaPull    int            `json:"mediaPull"`    // файлов ждёт загрузки
	LastPushed   int            `json:"lastPushed"`   // за прошлый проход
	LastPulled   int            `json:"lastPulled"`   // за прошлый проход
	Conflicts    []ConflictInfo `json:"conflicts"`    // последние
	Failures     []Failure      `json:"failures"`     // строки, которые не удалось записать
	CurrentTask  string         `json:"currentTask"`  // что делается прямо сейчас
	LastBytes    int64          `json:"lastBytes"`    // данных передано за прошлый проход (без файлов)
	MediaTask    string         `json:"mediaTask"`    // какой файл передаётся сейчас
	MediaError   string         `json:"mediaError"`   // ошибка передачи файлов (отдельно от данных)
	LocalVersion int64          `json:"localVersion"` // версия схемы базы
}

// Runner гоняет синхронизацию по таймеру и по требованию.
type Runner struct {
	pool   *pgxpool.Pool
	cfg    Config
	client *Client
	hooks  Hooks

	mu      sync.Mutex
	st      Status
	trigger chan struct{}
	runMu   sync.Mutex

	// Файлы идут своим циклом: долгая передача видео не задерживает отправку матчей.
	mediaTrigger chan struct{}
	mediaMu      sync.Mutex
}

const chunkRows = 300

func NewRunner(pool *pgxpool.Pool, cfg Config, hooks Hooks) *Runner {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	return &Runner{
		pool: pool, cfg: cfg, hooks: hooks,
		client:  NewClient(cfg.RemoteURL, cfg.Token),
		trigger: make(chan struct{}, 1), mediaTrigger: make(chan struct{}, 1),
		st: Status{Remote: cfg.RemoteURL},
	}
}

// Status - копия текущего состояния.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.st
	s.Conflicts = append([]ConflictInfo(nil), r.st.Conflicts...)
	s.Failures = append([]Failure(nil), r.st.Failures...)
	return s
}

// Trigger просит провести проход как можно скорее.
func (r *Runner) Trigger() {
	for _, c := range []chan struct{}{r.trigger, r.mediaTrigger} {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// Start крутит проход, пока жив ctx. Первый проход - сразу.
func (r *Runner) Start(ctx context.Context) {
	go func() {
		t := time.NewTimer(0)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-r.trigger:
			}
			r.RunOnce(ctx)
			t.Reset(r.cfg.Interval)
		}
	}()
	go r.mediaLoop(ctx)
}

// mediaLoop переносит файлы, как только данные первой загрузки на месте.
func (r *Runner) mediaLoop(ctx context.Context) {
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.mediaTrigger:
		}
		if r.Status().Initialized {
			r.mediaMu.Lock()
			err := r.syncMedia(ctx)
			r.mediaMu.Unlock()
			r.set(func(s *Status) {
				s.MediaTask = ""
				if err != nil {
					s.MediaError = err.Error()
				} else {
					s.MediaError = ""
				}
			})
			if err != nil {
				log.Printf("синхронизация файлов: %v", err)
			}
		}
		t.Reset(r.cfg.Interval)
	}
}

func (r *Runner) set(f func(s *Status)) {
	r.mu.Lock()
	f(&r.st)
	r.mu.Unlock()
}

func (r *Runner) task(s string) { r.set(func(st *Status) { st.CurrentTask = s }) }

// RunOnce - один полный проход; безопасен при параллельных вызовах.
func (r *Runner) RunOnce(ctx context.Context) {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	now := time.Now()
	r.set(func(s *Status) { s.Running = true; s.LastAttempt = &now })
	r.client.bytes.Store(0)
	err := r.cycle(ctx)
	done := time.Now()
	r.set(func(s *Status) {
		s.LastBytes = r.client.bytes.Load()
		s.Running = false
		s.CurrentTask = ""
		if err != nil {
			s.LastError = err.Error()
		} else {
			s.LastError = ""
			s.LastSuccess = &done
		}
	})
	if err != nil {
		log.Printf("синхронизация: %v", err)
	}
}

func activeTables() []Table {
	var out []Table
	for _, t := range Tables {
		if !t.InitOnly {
			out = append(out, t)
		}
	}
	return out
}

func tableNames(ts []Table) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func (r *Runner) ensureMeta(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS sync_meta (key text PRIMARY KEY, value text NOT NULL);
		CREATE TABLE IF NOT EXISTS sync_base (tbl text NOT NULL, key text NOT NULL, hash text NOT NULL, PRIMARY KEY (tbl, key))`)
	return err
}

func (r *Runner) meta(ctx context.Context, key string) string {
	var v string
	_ = r.pool.QueryRow(ctx, `SELECT value FROM sync_meta WHERE key = $1`, key).Scan(&v)
	return v
}

func (r *Runner) setMeta(ctx context.Context, key, val string) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO sync_meta (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, val)
	return err
}

type baseEntry struct{ Table, Key, Hash string } // Hash == "" - убрать запись

func (r *Runner) setBase(ctx context.Context, entries []baseEntry) error {
	var upT, upK, upH, delT, delK []string
	for _, e := range entries {
		if e.Hash == "" {
			delT, delK = append(delT, e.Table), append(delK, e.Key)
		} else {
			upT, upK, upH = append(upT, e.Table), append(upK, e.Key), append(upH, e.Hash)
		}
	}
	if len(upT) > 0 {
		if _, err := r.pool.Exec(ctx, `
			INSERT INTO sync_base (tbl, key, hash) SELECT * FROM unnest($1::text[], $2::text[], $3::text[])
			ON CONFLICT (tbl, key) DO UPDATE SET hash = EXCLUDED.hash`, upT, upK, upH); err != nil {
			return err
		}
	}
	if len(delT) > 0 {
		if _, err := r.pool.Exec(ctx, `
			DELETE FROM sync_base b USING unnest($1::text[], $2::text[]) AS d(tbl, key) WHERE b.tbl = d.tbl AND b.key = d.key`, delT, delK); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) loadBase(ctx context.Context) (Hashes, error) {
	rows, err := r.pool.Query(ctx, `SELECT tbl, key, hash FROM sync_base`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := Hashes{}
	for rows.Next() {
		var t, k, h string
		if err := rows.Scan(&t, &k, &h); err != nil {
			return nil, err
		}
		if out[t] == nil {
			out[t] = map[string]string{}
		}
		out[t][k] = h
	}
	return out, rows.Err()
}

func (r *Runner) localMigration(ctx context.Context) (int64, error) {
	var v int64
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&v)
	return v, err
}

func (r *Runner) cycle(ctx context.Context) error {
	h, err := r.client.Hello(ctx)
	if err != nil {
		r.set(func(s *Status) { s.Online = false })
		return fmt.Errorf("сайт недоступен: %w", err)
	}
	r.set(func(s *Status) { s.Online = true })
	mig, err := r.localMigration(ctx)
	if err != nil {
		return err
	}
	r.set(func(s *Status) { s.LocalVersion = mig })
	if h.Proto != ProtoVersion || h.Migration != mig {
		return fmt.Errorf("версии не совпадают (сайт: протокол %d, схема %d; приложение: протокол %d, схема %d): обновите приложение или сайт",
			h.Proto, h.Migration, ProtoVersion, mig)
	}
	if err := r.ensureMeta(ctx); err != nil {
		return err
	}
	if r.meta(ctx, "initialized") != "1" {
		return r.initialize(ctx)
	}
	r.set(func(s *Status) { s.Initialized = true })

	r.task("слияние дублей аккаунтов")
	if err := r.mergeCollisions(ctx); err != nil {
		log.Printf("синхронизация: слияние аккаунтов: %v", err)
		r.addFailure(Failure{Table: "users", Error: "слияние аккаунтов: " + err.Error()})
	}

	r.task("сравнение данных")
	active, err := r.changedTables(ctx)
	if err != nil {
		return err
	}
	local, err := Manifest(ctx, r.pool, active)
	if err != nil {
		return fmt.Errorf("хеши приложения: %w", err)
	}
	var remote Hashes
	if len(active) > 0 {
		remote, err = r.client.Manifest(ctx, tableNames(active))
		if err != nil {
			return fmt.Errorf("хеши сайта: %w", err)
		}
	}
	base, err := r.loadBase(ctx)
	if err != nil {
		return err
	}
	plan := Plan(active, local, remote, base)
	r.set(func(s *Status) {
		s.PendingPull, s.PendingPush = len(plan.Pull), len(plan.Push)
		for _, c := range plan.Conflicts {
			s.Conflicts = append(s.Conflicts, ConflictInfo{At: time.Now(), Table: c.Table, Key: c.Key, Winner: sideName(c.Winner)})
		}
		if len(s.Conflicts) > 50 {
			s.Conflicts = s.Conflicts[len(s.Conflicts)-50:]
		}
		s.Failures = nil
	})

	var entries []baseEntry
	for _, op := range plan.Same {
		entries = append(entries, baseEntry{op.Table, op.Key, op.Local})
	}

	r.task("загрузка данных с сайта")
	pulled, touchedPull, err := r.applyPull(ctx, plan.Pull)
	entries = append(entries, pulled...)
	if err != nil {
		_ = r.setBase(ctx, entries)
		return fmt.Errorf("загрузка с сайта: %w", err)
	}
	r.task("отправка данных на сайт")
	pushed, touchedPush, err := r.applyPush(ctx, plan.Push)
	entries = append(entries, pushed...)
	if err := r.setBase(ctx, entries); err != nil {
		return err
	}
	if err != nil {
		return fmt.Errorf("отправка на сайт: %w", err)
	}
	r.set(func(s *Status) { s.LastPulled, s.LastPushed = len(pulled), len(pushed) })

	if len(touchedPush) > 0 {
		if err := r.client.Done(ctx, keys(touchedPush)); err != nil {
			return fmt.Errorf("сайт не принял завершение: %w", err)
		}
	}
	if len(touchedPull) > 0 {
		if r.hooks.Pulled != nil {
			r.hooks.Pulled(touchedPull)
		}
		if intersects(touchedPull, recomputeTables) && r.hooks.Recompute != nil {
			r.task("пересчёт рейтинга")
			if err := r.hooks.Recompute(ctx); err != nil {
				return fmt.Errorf("пересчёт рейтинга: %w", err)
			}
		}
	}

	return nil
}

// changedTables - таблицы, у которых контрольная сумма на сайте или в приложении не равна сумме прошлой
// синхронизации. Остальные сравнивать незачем: переносить в них нечего.
func (r *Runner) changedTables(ctx context.Context) ([]Table, error) {
	active := activeTables()
	names := tableNames(active)
	localD, err := Digests(ctx, r.pool, active)
	if err != nil {
		return nil, fmt.Errorf("суммы приложения: %w", err)
	}
	remoteD, err := r.client.Digests(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("суммы сайта: %w", err)
	}
	baseD, err := r.baseDigests(ctx)
	if err != nil {
		return nil, err
	}
	var changed []Table
	for _, t := range active {
		b := baseD[t.Name]
		if localD[t.Name] != b || remoteD[t.Name] != b {
			changed = append(changed, t)
		}
	}
	return changed, nil
}

// baseDigests - суммы общей точки по таблицам в том же виде, что у Digests.
func (r *Runner) baseDigests(ctx context.Context) (map[string]Digest, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT tbl, count(*), coalesce(md5(string_agg(key || hash, '' ORDER BY key COLLATE "C")), '') FROM sync_base GROUP BY tbl`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Digest{}
	for rows.Next() {
		var t string
		var d Digest
		if err := rows.Scan(&t, &d.N, &d.D); err != nil {
			return nil, err
		}
		out[t] = d
	}
	return out, rows.Err()
}

func sideName(s Side) string {
	if s == SideApp {
		return "app"
	}
	return "site"
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func intersects(a map[string]bool, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

func (r *Runner) addFailure(f Failure) {
	r.set(func(s *Status) {
		s.Failures = append(s.Failures, f)
		if len(s.Failures) > 50 {
			s.Failures = s.Failures[len(s.Failures)-50:]
		}
	})
}

type tableOps struct {
	table Table
	ups   []Op
	dels  []Op
}

// groupOps раскладывает операции по таблицам в порядке реестра; del - признак удаления для данного направления.
func groupOps(ops []Op, isDelete func(Op) bool) []tableOps {
	by := map[string]*tableOps{}
	for _, op := range ops {
		t, ok := tableByName(op.Table)
		if !ok {
			continue
		}
		g := by[op.Table]
		if g == nil {
			g = &tableOps{table: t}
			by[op.Table] = g
		}
		if isDelete(op) {
			g.dels = append(g.dels, op)
		} else {
			g.ups = append(g.ups, op)
		}
	}
	var out []tableOps
	for _, t := range Tables {
		if g := by[t.Name]; g != nil {
			out = append(out, *g)
		}
	}
	return out
}

func chunks[T any](in []T, n int) [][]T {
	var out [][]T
	for len(in) > 0 {
		k := n
		if len(in) < k {
			k = len(in)
		}
		out = append(out, in[:k])
		in = in[k:]
	}
	return out
}

func opKeys(ops []Op) []string {
	k := make([]string, len(ops))
	for i, o := range ops {
		k[i] = o.Key
	}
	return k
}

// applyPull записывает на приложение то, что изменилось на сайте.
func (r *Runner) applyPull(ctx context.Context, ops []Op) ([]baseEntry, map[string]bool, error) {
	var done []baseEntry
	touched := map[string]bool{}
	groups := groupOps(ops, Op.deleteOnPull)
	opOf := map[[2]string]Op{}
	for _, op := range ops {
		opOf[[2]string{op.Table, op.Key}] = op
	}
	absorb := func(res ApplyResult) {
		for _, a := range res.Applied {
			op := opOf[[2]string{a.Table, a.Key}]
			done = append(done, baseEntry{a.Table, a.Key, op.Remote})
			touched[a.Table] = true
		}
		for _, f := range res.Failed {
			r.addFailure(f)
		}
	}
	for _, g := range groups {
		for _, part := range chunks(g.ups, chunkRows) {
			rows, err := r.client.Rows(ctx, g.table.Name, opKeys(part))
			if err != nil {
				return done, touched, err
			}
			ch := TableChange{Table: g.table.Name}
			for _, op := range part {
				row, ok := rows[op.Key]
				if !ok {
					continue // удалена на сайте за время прохода: разберёмся в следующий
				}
				ch.Upserts = append(ch.Upserts, UpRow{Key: op.Key, IfHash: op.Local, Row: row})
			}
			res, err := Apply(ctx, r.pool, []TableChange{ch}, true)
			if err != nil {
				return done, touched, err
			}
			absorb(res)
		}
	}
	for i := len(groups) - 1; i >= 0; i-- {
		g := groups[i]
		for _, part := range chunks(g.dels, chunkRows) {
			ch := TableChange{Table: g.table.Name}
			for _, op := range part {
				ch.Deletes = append(ch.Deletes, DelRow{Key: op.Key, IfHash: op.Local})
			}
			res, err := Apply(ctx, r.pool, []TableChange{ch}, true)
			if err != nil {
				return done, touched, err
			}
			absorb(res)
		}
	}
	return done, touched, nil
}

// applyPush отправляет на сайт то, что изменилось в приложении.
func (r *Runner) applyPush(ctx context.Context, ops []Op) ([]baseEntry, map[string]bool, error) {
	var done []baseEntry
	touched := map[string]bool{}
	groups := groupOps(ops, Op.deleteOnPush)
	opOf := map[[2]string]Op{}
	for _, op := range ops {
		opOf[[2]string{op.Table, op.Key}] = op
	}
	absorb := func(res ApplyResult) {
		for _, a := range res.Applied {
			op := opOf[[2]string{a.Table, a.Key}]
			done = append(done, baseEntry{a.Table, a.Key, op.Local})
			touched[a.Table] = true
		}
		for _, f := range res.Failed {
			r.addFailure(f)
		}
	}
	for _, g := range groups {
		for _, part := range chunks(g.ups, chunkRows) {
			rows, err := FetchRows(ctx, r.pool, g.table, opKeys(part))
			if err != nil {
				return done, touched, err
			}
			ch := TableChange{Table: g.table.Name}
			for _, op := range part {
				row, ok := rows[op.Key]
				if !ok {
					continue
				}
				ch.Upserts = append(ch.Upserts, UpRow{Key: op.Key, IfHash: op.Remote, Row: row})
			}
			res, err := r.client.Apply(ctx, []TableChange{ch})
			if err != nil {
				return done, touched, err
			}
			absorb(res)
		}
	}
	for i := len(groups) - 1; i >= 0; i-- {
		g := groups[i]
		for _, part := range chunks(g.dels, chunkRows) {
			ch := TableChange{Table: g.table.Name}
			for _, op := range part {
				ch.Deletes = append(ch.Deletes, DelRow{Key: op.Key, IfHash: op.Remote})
			}
			res, err := r.client.Apply(ctx, []TableChange{ch})
			if err != nil {
				return done, touched, err
			}
			absorb(res)
		}
	}
	return done, touched, nil
}

// initialize - первая загрузка: приложение становится копией сайта. Всё, что в локальной базе было
// до этого (заводские данные миграций), стирается, иначе у тех же записей оказались бы разные ключи.
func (r *Runner) initialize(ctx context.Context) error {
	r.task("первая загрузка данных с сайта")
	remote, err := r.client.Manifest(ctx, nil)
	if err != nil {
		return fmt.Errorf("хеши сайта: %w", err)
	}
	for _, t := range Tables {
		if !t.Optional {
			continue
		}
		ddl, found, err := r.client.Schema(ctx, t.Name)
		if err != nil {
			return fmt.Errorf("схема %s: %w", t.Name, err)
		}
		if found {
			if _, err := r.pool.Exec(ctx, ddl); err != nil {
				return fmt.Errorf("создание таблицы %s: %w", t.Name, err)
			}
		}
	}
	names := tableNames(Tables)
	names = append(names, derivedTables...)
	var quoted []string
	for _, n := range names {
		var exists bool
		if err := r.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+ident(n)).Scan(&exists); err != nil {
			return err
		}
		if exists {
			quoted = append(quoted, ident(n))
		}
	}
	if _, err := r.pool.Exec(ctx, "TRUNCATE "+strings.Join(quoted, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
		return fmt.Errorf("очистка перед загрузкой: %w", err)
	}
	if _, err := r.pool.Exec(ctx, `TRUNCATE sync_base`); err != nil {
		return err
	}
	total := 0
	for _, t := range Tables {
		r.task("первая загрузка: " + t.Name)
		ks := make([]string, 0, len(remote[t.Name]))
		for k := range remote[t.Name] {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, part := range chunks(ks, chunkRows) {
			rows, err := r.client.Rows(ctx, t.Name, part)
			if err != nil {
				return err
			}
			ch := TableChange{Table: t.Name}
			for _, k := range part {
				if row, ok := rows[k]; ok {
					ch.Upserts = append(ch.Upserts, UpRow{Key: k, Row: row})
				}
			}
			res, err := Apply(ctx, r.pool, []TableChange{ch}, false)
			if err != nil {
				return fmt.Errorf("%s: %w", t.Name, err)
			}
			if len(res.Failed) > 0 {
				return fmt.Errorf("%s: не записалась строка %s: %s", t.Name, res.Failed[0].Key, res.Failed[0].Error)
			}
			total += len(res.Applied)
		}
	}
	local, err := Manifest(ctx, r.pool, activeTables())
	if err != nil {
		return err
	}
	var entries []baseEntry
	mismatch := 0
	for _, t := range activeTables() {
		for k, rh := range remote[t.Name] {
			if local[t.Name][k] != rh {
				mismatch++
			}
			entries = append(entries, baseEntry{t.Name, k, rh})
		}
	}
	if mismatch > 0 {
		log.Printf("синхронизация: после первой загрузки у %d строк хеш отличается от сайта", mismatch)
	}
	if err := r.setBase(ctx, entries); err != nil {
		return err
	}
	if r.hooks.Recompute != nil {
		r.task("пересчёт рейтинга")
		if err := r.hooks.Recompute(ctx); err != nil {
			return fmt.Errorf("пересчёт рейтинга: %w", err)
		}
	}
	if r.hooks.Pulled != nil {
		r.hooks.Pulled(map[string]bool{"overlay_presets": true})
	}
	if err := r.setMeta(ctx, "initialized", "1"); err != nil {
		return err
	}
	log.Printf("синхронизация: первая загрузка завершена, строк: %d", total)
	r.set(func(s *Status) { s.Initialized = true })
	return nil
}

// syncMedia переносит файлы в обе стороны. Файлы только добавляются: удаление и правка на месте не
// переносятся, имена у файлов уникальные.
func (r *Runner) syncMedia(ctx context.Context) error {
	if r.cfg.MediaDir == "" {
		return nil
	}
	localFiles, err := ListMedia(r.cfg.MediaDir)
	if err != nil {
		return err
	}
	remoteFiles, err := r.client.MediaList(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, f := range localFiles {
		have[f.Path] = true
	}
	there := map[string]bool{}
	for _, f := range remoteFiles {
		there[f.Path] = true
	}
	var push, pull []MediaFile
	for _, f := range localFiles {
		if !there[f.Path] {
			push = append(push, f)
		}
	}
	for _, f := range remoteFiles {
		if !have[f.Path] {
			pull = append(pull, f)
		}
	}
	r.set(func(s *Status) { s.MediaPush, s.MediaPull = len(push), len(pull) })
	var errs []string
	for _, f := range pull {
		full, ok := safeMediaPath(r.cfg.MediaDir, f.Path)
		if !ok {
			continue
		}
		r.set(func(s *Status) { s.MediaTask = "файл с сайта: " + f.Path })
		if err := r.client.MediaGet(ctx, f.Path, full); err != nil {
			errs = append(errs, f.Path+": "+err.Error())
			continue
		}
		r.set(func(s *Status) { s.MediaPull-- })
	}
	for _, f := range push {
		full, ok := safeMediaPath(r.cfg.MediaDir, f.Path)
		if !ok {
			continue
		}
		r.set(func(s *Status) { s.MediaTask = "файл на сайт: " + f.Path })
		if err := putFile(ctx, r.client, f.Path, full); err != nil {
			errs = append(errs, f.Path+": "+err.Error())
			continue
		}
		r.set(func(s *Status) { s.MediaPush-- })
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func putFile(ctx context.Context, c *Client, rel, full string) error {
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return c.MediaPut(ctx, rel, info.Size(), f)
}

// mergeCollisions сливает аккаунт, заведённый в приложении офлайн, с одноимённым аккаунтом на сайте: всё,
// что ссылалось на заготовку, переводится на настоящий аккаунт, заготовка удаляется.
func (r *Runner) mergeCollisions(ctx context.Context) error {
	rows, err := r.pool.Query(ctx, `
		SELECT id, lower(login) FROM users
		WHERE NOT EXISTS (SELECT 1 FROM sync_base b WHERE b.tbl = 'users' AND b.key = users.id)`)
	if err != nil {
		return err
	}
	type lu struct{ id, login string }
	var fresh []lu
	for rows.Next() {
		var x lu
		if err := rows.Scan(&x.id, &x.login); err != nil {
			rows.Close()
			return err
		}
		fresh = append(fresh, x)
	}
	rows.Close()
	if len(fresh) == 0 {
		return nil
	}
	logins, err := r.client.Logins(ctx)
	if err != nil {
		return err
	}
	byLogin := map[string]string{}
	for id, l := range logins {
		byLogin[l] = id
	}
	var errs []string
	for _, x := range fresh {
		rid, ok := byLogin[x.login]
		if !ok || rid == x.id {
			continue
		}
		if err := r.mergeUser(ctx, x.id, rid); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", x.login, err))
			continue
		}
		log.Printf("синхронизация: аккаунт %s слит с аккаунтом сайта", x.login)
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (r *Runner) mergeUser(ctx context.Context, oldID, newID string) error {
	rows, err := r.client.Rows(ctx, "users", []string{newID})
	if err != nil {
		return err
	}
	row, ok := rows[newID]
	if !ok {
		return errors.New("на сайте аккаунт уже исчез")
	}
	tx, err := beginWrite(ctx, r.pool)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE users SET login = login || '~dup~' || left(id, 8) WHERE id = $1`, oldID); err != nil {
		return err
	}
	res, err := ApplyTx(ctx, tx, []TableChange{{Table: "users", Upserts: []UpRow{{Key: newID, Row: row}}}}, false)
	if err != nil {
		return err
	}
	if len(res.Failed) > 0 {
		return errors.New(res.Failed[0].Error)
	}
	fk, err := tx.Query(ctx, `
		SELECT c.conrelid::regclass::text, a.attname
		FROM pg_constraint c JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		WHERE c.contype = 'f' AND c.confrelid = 'public.users'::regclass AND array_length(c.conkey, 1) = 1`)
	if err != nil {
		return err
	}
	type col struct{ table, col string }
	var cols []col
	for fk.Next() {
		var c col
		if err := fk.Scan(&c.table, &c.col); err != nil {
			fk.Close()
			return err
		}
		cols = append(cols, c)
	}
	fk.Close()
	for _, c := range cols {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = $1 WHERE %s = $2`, c.table, ident(c.col), ident(c.col)), newID, oldID); err != nil {
			return fmt.Errorf("%s.%s: %w", c.table, c.col, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE participants SET members = replace(members::text, $1, $2)::jsonb WHERE members::text LIKE '%' || $1 || '%'`, oldID, newID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, oldID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarshalStatus - статус одной строкой JSON (для лаунчера).
func (r *Runner) MarshalStatus() []byte {
	b, _ := json.Marshal(r.Status())
	return b
}
