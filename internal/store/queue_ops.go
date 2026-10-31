package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// QueueResult - что изменилось: по ревизии клиенты понимают, что состояние устарело.
type QueueResult struct {
	SessionID string
	Format    string
	Rev       int64
	EntryID   string // созданная или затронутая запись, если действие про одну запись
	MatchID   string // матч сайта, созданный действием
}

// queueMatchSize - в матче всегда две стороны: игрок против игрока или команда против команды.
const queueMatchSize = 2

// qtx - транзакция над одной сессией. Строка сессии заблокирована до конца: любые изменения очереди
// идут по одному, поэтому позиции, вызовы и записи не расходятся.
type qtx struct {
	ctx   context.Context
	tx    pgx.Tx
	actor string // кто делает: id игрока или организатора
	sess  qSession
	res   QueueResult
}

type qSession struct {
	ID, Title, Format, Status string
	RegistrationOpen, Paused  bool
	TimeoutSec                int
	Rev                       int64
}

func (q *qSession) active() bool { return q.Status == "active" }

// queueTx выполняет fn под блокировкой сессии и увеличивает её ревизию, если fn ничего не вернула как ошибку.
func (s *Store) queueTx(ctx context.Context, sessionID, actor string, fn func(q *qtx) error) (QueueResult, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return QueueResult{}, err
	}
	defer tx.Rollback(ctx)
	q := &qtx{ctx: ctx, tx: tx, actor: actor}
	err = tx.QueryRow(ctx, `
		SELECT id, title, format, status, registration_open, paused, confirm_timeout_sec, rev
		FROM queue_sessions WHERE id = $1 FOR UPDATE`, sessionID).
		Scan(&q.sess.ID, &q.sess.Title, &q.sess.Format, &q.sess.Status, &q.sess.RegistrationOpen, &q.sess.Paused,
			&q.sess.TimeoutSec, &q.sess.Rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return QueueResult{}, ErrNotFound
	}
	if err != nil {
		return QueueResult{}, err
	}
	if err := q.sweepGhosts(); err != nil {
		return QueueResult{}, err
	}
	q.res = QueueResult{SessionID: sessionID, Format: q.sess.Format}
	if err := fn(q); err != nil {
		return QueueResult{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE queue_sessions SET rev = rev + 1 WHERE id = $1 RETURNING rev`, sessionID).Scan(&q.res.Rev); err != nil {
		return QueueResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return QueueResult{}, err
	}
	return q.res, nil
}

// sweepGhosts закрывает записи, у которых не осталось игроков (аккаунт удалён): они не должны занимать место.
func (q *qtx) sweepGhosts() error {
	_, err := q.tx.Exec(q.ctx, `
		DELETE FROM queue_match_entries WHERE entry_id IN (
			SELECT e.id FROM queue_entries e WHERE e.session_id = $1 AND e.position IS NOT NULL
			  AND NOT EXISTS (SELECT 1 FROM queue_entry_members m WHERE m.entry_id = e.id))
		  AND match_id IN (SELECT id FROM queue_matches WHERE status = 'preparing')`, q.sess.ID)
	if err != nil {
		return err
	}
	_, err = q.tx.Exec(q.ctx, `
		UPDATE queue_entries e SET status = 'removed', position = NULL, closed_at = now()
		WHERE e.session_id = $1 AND e.position IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM queue_entry_members m WHERE m.entry_id = e.id)`, q.sess.ID)
	if err != nil {
		return err
	}
	return q.dropEmptyMatch()
}

func (q *qtx) needActive() error {
	if !q.sess.active() {
		return qState("Сессия завершена")
	}
	return nil
}

func (q *qtx) event(kind, entryID, matchID, label string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	raw, _ := json.Marshal(data)
	var actor, entry, match *string
	if q.actor != "" {
		actor = &q.actor
	}
	if entryID != "" {
		entry = &entryID
	}
	if matchID != "" {
		match = &matchID
	}
	_, err := q.tx.Exec(q.ctx, `
		INSERT INTO queue_events (session_id, kind, actor_id, entry_id, match_id, label, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)`, q.sess.ID, kind, actor, entry, match, label, string(raw))
	return err
}

// qEntry - запись очереди в расчётах транзакции.
type qEntry struct {
	ID        string
	Status    string
	Position  int
	Performed int
	Skipped   int
	TeamKey   string
	Label     string
}

// load читает записи в очереди по позициям (с названиями) и заодно список для расчёта порядка.
func (q *qtx) load() ([]queueItem, map[string]*qEntry, error) {
	rows, err := q.tx.Query(q.ctx, `
		SELECT e.id, e.status, e.position, e.performed_count, e.skipped_count, COALESCE(e.team_key, ''),
		       COALESCE(NULLIF(e.team_name, ''), (
		           SELECT string_agg(COALESCE(NULLIF(u.display_name, ''), u.login), ' & ' ORDER BY u.login)
		           FROM queue_entry_members m JOIN users u ON u.id = m.user_id WHERE m.entry_id = e.id), '')
		FROM queue_entries e WHERE e.session_id = $1 AND e.position IS NOT NULL ORDER BY e.position`, q.sess.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var items []queueItem
	byID := map[string]*qEntry{}
	for rows.Next() {
		e := &qEntry{}
		if err := rows.Scan(&e.ID, &e.Status, &e.Position, &e.Performed, &e.Skipped, &e.TeamKey, &e.Label); err != nil {
			return nil, nil, err
		}
		items = append(items, queueItem{ID: e.ID, Status: e.Status, Performed: e.Performed})
		byID[e.ID] = e
	}
	return items, byID, rows.Err()
}

// writeOrder записывает новый порядок: позиции 1..N без пропусков. Уникальность позиций проверяется
// в конце транзакции, так что промежуточные совпадения не мешают.
func (q *qtx) writeOrder(items []queueItem) error {
	ids := qIDs(items)
	pos := make([]int32, len(ids))
	for i := range pos {
		pos[i] = int32(i + 1)
	}
	_, err := q.tx.Exec(q.ctx, `
		UPDATE queue_entries e SET position = v.pos
		FROM unnest($2::text[], $3::int[]) AS v(id, pos)
		WHERE e.id = v.id AND e.session_id = $1`, q.sess.ID, ids, pos)
	return err
}

// entry находит запись в очереди; чужую или закрытую не отдаёт.
func (q *qtx) entry(id string, byID map[string]*qEntry) (*qEntry, error) {
	e, ok := byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	return e, nil
}

// userEntry - запись, в которой стоит игрок.
func (q *qtx) userEntry(userID string, byID map[string]*qEntry) (*qEntry, error) {
	var id string
	err := q.tx.QueryRow(q.ctx, `
		SELECT m.entry_id FROM queue_entry_members m
		WHERE m.session_id = $1 AND m.user_id = $2 AND m.active`, q.sess.ID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return q.entry(id, byID)
}

// openMatch - матч в подготовке или в эфире.
type qMatchRow struct {
	ID     string
	Number int
	Status string
}

func (q *qtx) openMatch() (*qMatchRow, error) {
	m := &qMatchRow{}
	err := q.tx.QueryRow(q.ctx, `
		SELECT id, number, status FROM queue_matches WHERE session_id = $1 AND status IN ('preparing', 'live')`, q.sess.ID).
		Scan(&m.ID, &m.Number, &m.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

// matchEntries - записи матча по сторонам.
func (q *qtx) matchEntries(matchID string) ([]string, error) {
	rows, err := q.tx.Query(q.ctx, `SELECT entry_id FROM queue_match_entries WHERE match_id = $1 ORDER BY side`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// dropEmptyMatch убирает матч в подготовке, из которого все ушли.
func (q *qtx) dropEmptyMatch() error {
	_, err := q.tx.Exec(q.ctx, `
		DELETE FROM queue_matches m WHERE m.session_id = $1 AND m.status = 'preparing'
		  AND NOT EXISTS (SELECT 1 FROM queue_match_entries me WHERE me.match_id = m.id)`, q.sess.ID)
	return err
}

// detach снимает запись с матча в подготовке.
func (q *qtx) detach(entryID string) error {
	if _, err := q.tx.Exec(q.ctx, `
		DELETE FROM queue_match_entries WHERE entry_id = $1
		  AND match_id IN (SELECT id FROM queue_matches WHERE status = 'preparing')`, entryID); err != nil {
		return err
	}
	return q.dropEmptyMatch()
}

// resetEntry возвращает вызванную запись в ожидание.
func (q *qtx) resetEntry(entryID, status string) error {
	if _, err := q.tx.Exec(q.ctx, `
		UPDATE queue_entries SET status = $2, invited_at = NULL, invite_expires_at = NULL WHERE id = $1`, entryID, status); err != nil {
		return err
	}
	_, err := q.tx.Exec(q.ctx, `UPDATE queue_entry_members SET ready_at = NULL WHERE entry_id = $1`, entryID)
	return err
}

// closeEntry выводит запись из очереди: status left (сам вышел) или removed (убрал организатор).
func (q *qtx) closeEntry(entryID, status string) error {
	if err := q.detach(entryID); err != nil {
		return err
	}
	_, err := q.tx.Exec(q.ctx, `
		UPDATE queue_entries SET status = $2, position = NULL, closed_at = now(), invited_at = NULL, invite_expires_at = NULL
		WHERE id = $1`, entryID, status)
	return err
}

func isUnique(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && (constraint == "" || pg.ConstraintName == constraint)
}

// ---------- Сессии ----------

// QueueSessionInput - параметры новой сессии; нулевые поля берутся по умолчанию.
type QueueSessionInput struct {
	Title             string
	Format            string
	ConfirmTimeoutSec int
}

// QueueDefaultTitle - название сессии по умолчанию: «Очередь на выступления | 30.10.2026».
func QueueDefaultTitle(at time.Time) string {
	return "Очередь на выступления | " + mskDay(at).Format("02.01.2006")
}

// CreateQueueSession открывает очередь на стрим. Живая очередь формата одна: вторая не открывается.
func (s *Store) CreateQueueSession(ctx context.Context, actor string, in QueueSessionInput) (QueueResult, error) {
	if in.Format != "1x1" && in.Format != "2x2" {
		return QueueResult{}, qInvalid("формат очереди — 1×1 или 2×2")
	}
	if in.Title == "" {
		in.Title = QueueDefaultTitle(time.Now())
	}
	if in.ConfirmTimeoutSec == 0 {
		in.ConfirmTimeoutSec = 60
	}
	if in.ConfirmTimeoutSec < 10 || in.ConfirmTimeoutSec > 600 {
		return QueueResult{}, qInvalid("время на подтверждение — от 10 до 600 секунд")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return QueueResult{}, err
	}
	defer tx.Rollback(ctx)
	var by *string
	if actor != "" {
		by = &actor
	}
	var res QueueResult
	err = tx.QueryRow(ctx, `
		INSERT INTO queue_sessions (title, format, confirm_timeout_sec, created_by)
		VALUES ($1, $2, $3, $4) RETURNING id, format, rev`,
		in.Title, in.Format, in.ConfirmTimeoutSec, by).Scan(&res.SessionID, &res.Format, &res.Rev)
	if isUnique(err, "queue_sessions_one_active") {
		return QueueResult{}, qState(fmt.Sprintf("Очередь %s уже идёт — завершите её, чтобы начать новую", formatLabel(in.Format)))
	}
	if err != nil {
		return QueueResult{}, err
	}
	raw, _ := json.Marshal(map[string]any{"format": in.Format, "title": in.Title})
	if _, err := tx.Exec(ctx, `INSERT INTO queue_events (session_id, kind, actor_id, data) VALUES ($1, 'session_created', $2, $3::jsonb)`,
		res.SessionID, by, string(raw)); err != nil {
		return QueueResult{}, err
	}
	return res, tx.Commit(ctx)
}

func formatLabel(f string) string {
	if f == "2x2" {
		return "2×2"
	}
	return "1×1"
}

// QueueSessionPatch - правки настроек сессии; nil - не менять.
type QueueSessionPatch struct {
	Title             *string
	Format            *string
	ConfirmTimeoutSec *int
}

// UpdateQueueSession меняет название, формат и настройки вызова. Формат и размер матча меняются,
// пока в очереди никого нет и матч не идёт: иначе записи 1×1 оказались бы в очереди 2×2.
func (s *Store) UpdateQueueSession(ctx context.Context, sessionID, actor string, p QueueSessionPatch) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		items, _, err := q.load()
		if err != nil {
			return err
		}
		m, err := q.openMatch()
		if err != nil {
			return err
		}
		data := map[string]any{}
		if p.Title != nil {
			if *p.Title == "" {
				return qInvalid("название не может быть пустым")
			}
			q.sess.Title = *p.Title
			data["title"] = *p.Title
		}
		if p.ConfirmTimeoutSec != nil {
			if *p.ConfirmTimeoutSec < 10 || *p.ConfirmTimeoutSec > 600 {
				return qInvalid("время на подтверждение — от 10 до 600 секунд")
			}
			q.sess.TimeoutSec = *p.ConfirmTimeoutSec
			data["confirmTimeoutSec"] = *p.ConfirmTimeoutSec
		}
		if p.Format != nil && *p.Format != q.sess.Format {
			if *p.Format != "1x1" && *p.Format != "2x2" {
				return qInvalid("формат очереди — 1×1 или 2×2")
			}
			if len(items) > 0 || m != nil {
				return qState("Формат меняется, пока в очереди никого нет: сначала уберите записи")
			}
			q.sess.Format = *p.Format
			q.res.Format = *p.Format
			data["format"] = *p.Format
		}
		_, err = q.tx.Exec(q.ctx, `
			UPDATE queue_sessions SET title = $2, format = $3, confirm_timeout_sec = $4 WHERE id = $1`,
			q.sess.ID, q.sess.Title, q.sess.Format, q.sess.TimeoutSec)
		if isUnique(err, "queue_sessions_one_active") {
			return qState(fmt.Sprintf("Очередь %s уже идёт", formatLabel(q.sess.Format)))
		}
		if err != nil {
			return err
		}
		return q.event("session_updated", "", "", "", data)
	})
}

// SetQueueMode переключает запись и вызовы: open / close (запись игроков), pause / resume (вся очередь).
func (s *Store) SetQueueMode(ctx context.Context, sessionID, actor, action string) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		switch action {
		case "open":
			q.sess.RegistrationOpen = true
		case "close":
			q.sess.RegistrationOpen = false
		case "pause":
			q.sess.Paused = true
		case "resume":
			q.sess.Paused = false
		default:
			return qInvalid("неизвестное действие")
		}
		if _, err := q.tx.Exec(q.ctx, `UPDATE queue_sessions SET registration_open = $2, paused = $3 WHERE id = $1`,
			q.sess.ID, q.sess.RegistrationOpen, q.sess.Paused); err != nil {
			return err
		}
		return q.event("queue_"+action, "", "", "", nil)
	})
}

// FinishQueueSession завершает сессию: история остаётся, новая сессия начнётся с пустой очереди.
// Матч в эфире сначала нужно завершить или отменить.
func (s *Store) FinishQueueSession(ctx context.Context, sessionID, actor string) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		m, err := q.openMatch()
		if err != nil {
			return err
		}
		if m != nil && m.Status == "live" {
			return qState("Сначала завершите или отмените матч в эфире")
		}
		if m != nil {
			if err := q.cancelMatch(m, "session_finished"); err != nil {
				return err
			}
		}
		if _, err := q.tx.Exec(q.ctx, `
			UPDATE queue_sessions SET status = 'finished', registration_open = false, paused = false, finished_at = now()
			WHERE id = $1`, q.sess.ID); err != nil {
			return err
		}
		return q.event("session_finished", "", "", "", nil)
	})
}

// ---------- Запись в очередь ----------

// QueueJoin - кто и кого записывает: UserID - игрок, TeamKey - команда для 2×2.
// ByOrganizer = true - организатор добавляет вручную: закрытая запись и пауза ему не помеха.
type QueueJoin struct {
	UserID      string
	TeamKey     string
	ByOrganizer bool
}

// JoinQueue записывает игрока или команду в конец очереди. Повторная запись невозможна: игрок стоит в очереди
// сессии один раз, и в команде оба игрока всегда на одном месте.
func (s *Store) JoinQueue(ctx context.Context, sessionID, actor string, in QueueJoin) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		if !in.ByOrganizer {
			if q.sess.Paused {
				return qState("Очередь приостановлена организатором")
			}
			if !q.sess.RegistrationOpen {
				return qState("Запись закрыта организатором")
			}
		}
		items, _, err := q.load()
		if err != nil {
			return err
		}
		if len(items) >= QueueMaxEntries {
			return qState("Очередь заполнена")
		}

		var members []string
		var teamKey, teamName string
		switch q.sess.Format {
		case "1x1":
			if in.TeamKey != "" {
				return qInvalid("в очереди 1×1 записываются игроки, а не команды")
			}
			if in.UserID == "" {
				return qInvalid("не указан игрок")
			}
			members = []string{in.UserID}
		default:
			if in.TeamKey == "" {
				return qInvalid("в очереди 2×2 записывается команда")
			}
			var a, b string
			err := q.tx.QueryRow(q.ctx, `SELECT member_a, member_b, name FROM teams WHERE team_key = $1`, in.TeamKey).Scan(&a, &b, &teamName)
			if errors.Is(err, pgx.ErrNoRows) {
				return &QueueError{ErrNotFound, "Команда не найдена"}
			}
			if err != nil {
				return err
			}
			if !in.ByOrganizer && in.UserID != a && in.UserID != b {
				return qForbidden("Записать можно только свою команду")
			}
			teamKey, members = in.TeamKey, []string{a, b}
		}

		var known int
		if err := q.tx.QueryRow(q.ctx, `SELECT COUNT(*) FROM users WHERE id = ANY($1)`, members).Scan(&known); err != nil {
			return err
		}
		if known != len(members) {
			return &QueueError{ErrNotFound, "Игрок не найден"}
		}
		var busy string
		err = q.tx.QueryRow(q.ctx, `
			SELECT user_id FROM queue_entry_members WHERE session_id = $1 AND user_id = ANY($2) AND active LIMIT 1`,
			q.sess.ID, members).Scan(&busy)
		if err == nil {
			if busy == in.UserID && !in.ByOrganizer {
				return qState("Вы уже стоите в очереди")
			}
			if q.sess.Format == "2x2" {
				return qState("Игрок этой команды уже стоит в очереди")
			}
			return qState("Игрок уже стоит в очереди")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		var by *string
		if in.ByOrganizer && actor != "" {
			by = &actor
		}
		// Игрок, уже выступавший в этой сессии, возвращается с прежним числом выступлений.
		var performed int
		if err := q.tx.QueryRow(q.ctx, `
			SELECT COUNT(DISTINCT me.match_id) FROM queue_entry_members em
			JOIN queue_match_entries me ON me.entry_id = em.entry_id
			JOIN queue_matches m ON m.id = me.match_id AND m.status = 'finished'
			WHERE em.session_id = $1 AND em.user_id = ANY($2)`, q.sess.ID, members).Scan(&performed); err != nil {
			return err
		}
		var entryID string
		err = q.tx.QueryRow(q.ctx, `
			INSERT INTO queue_entries (session_id, team_key, team_name, position, added_by, performed_count)
			VALUES ($1, NULLIF($2, ''), $3, (SELECT COALESCE(MAX(position), 0) + 1 FROM queue_entries WHERE session_id = $1), $4, $5)
			RETURNING id`, q.sess.ID, teamKey, teamName, by, performed).Scan(&entryID)
		if err != nil {
			return err
		}
		for _, u := range members {
			if _, err := q.tx.Exec(q.ctx, `INSERT INTO queue_entry_members (entry_id, session_id, user_id) VALUES ($1, $2, $3)`,
				entryID, q.sess.ID, u); err != nil {
				if isUnique(err, "queue_member_once") {
					return qState("Игрок уже стоит в очереди")
				}
				return err
			}
		}
		items = qPlaceNew(items, queueItem{ID: entryID, Status: QEntryWaiting, Performed: performed})
		if err := q.writeOrder(items); err != nil {
			return err
		}
		label := teamName
		if label == "" {
			if err := q.tx.QueryRow(q.ctx, `SELECT COALESCE(NULLIF(display_name, ''), login) FROM users WHERE id = $1`, members[0]).Scan(&label); err != nil {
				return err
			}
		}
		kind := "join"
		if in.ByOrganizer {
			kind = "add"
		}
		q.res.EntryID = entryID
		return q.event(kind, entryID, "", label, nil)
	})
}

// LeaveQueue - игрок выходит из очереди; уходит вся запись, в том числе команда целиком.
func (s *Store) LeaveQueue(ctx context.Context, sessionID, userID string) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, userID, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		items, byID, err := q.load()
		if err != nil {
			return err
		}
		e, err := q.userEntry(userID, byID)
		if errors.Is(err, ErrNotFound) {
			return &QueueError{ErrNotFound, "Вас нет в очереди"}
		}
		if err != nil {
			return err
		}
		if e.Status == QEntryPerforming {
			return qState("Вы выступаете — выйти нельзя, попросите организатора отменить матч")
		}
		if err := q.closeEntry(e.ID, QEntryLeft); err != nil {
			return err
		}
		items, _, _ = qWithout(items, e.ID)
		if err := q.writeOrder(items); err != nil {
			return err
		}
		q.res.EntryID = e.ID
		return q.event("leave", e.ID, "", e.Label, nil)
	})
}

// ConfirmReady - игрок подтверждает готовность на вызов. Запись готова, когда подтвердили все её игроки.
// Подтверждение после истёкшего времени принимается: решает организатор, пропускать ли.
func (s *Store) ConfirmReady(ctx context.Context, sessionID, userID string) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, userID, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		_, byID, err := q.load()
		if err != nil {
			return err
		}
		e, err := q.userEntry(userID, byID)
		if errors.Is(err, ErrNotFound) {
			return &QueueError{ErrNotFound, "Вас нет в очереди"}
		}
		if err != nil {
			return err
		}
		if e.Status != QEntryInvited && e.Status != QEntryReady {
			return qState("Вас пока не вызывали")
		}
		q.res.EntryID = e.ID
		if e.Status == QEntryReady {
			return nil
		}
		if _, err := q.tx.Exec(q.ctx, `
			UPDATE queue_entry_members SET ready_at = COALESCE(ready_at, now()) WHERE entry_id = $1 AND user_id = $2`, e.ID, userID); err != nil {
			return err
		}
		return q.refreshReady(e)
	})
}

// refreshReady переводит запись в «подтвердил готовность», когда подтвердили все её игроки.
func (q *qtx) refreshReady(e *qEntry) error {
	var waiting int
	if err := q.tx.QueryRow(q.ctx, `SELECT COUNT(*) FROM queue_entry_members WHERE entry_id = $1 AND ready_at IS NULL`, e.ID).Scan(&waiting); err != nil {
		return err
	}
	if waiting > 0 {
		return q.event("confirm_part", e.ID, "", e.Label, nil)
	}
	if _, err := q.tx.Exec(q.ctx, `UPDATE queue_entries SET status = 'ready' WHERE id = $1`, e.ID); err != nil {
		return err
	}
	return q.event("confirm", e.ID, "", e.Label, nil)
}

// ---------- Вызовы и матч ----------

// CallEntry вызывает запись на матч: entryID пуст - следующую по очереди. Вызванному даётся время на подтверждение.
func (s *Store) CallEntry(ctx context.Context, sessionID, actor, entryID string) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		if q.sess.Paused {
			return qState("Очередь приостановлена — возобновите её, чтобы вызывать")
		}
		items, byID, err := q.load()
		if err != nil {
			return err
		}
		m, err := q.openMatch()
		if err != nil {
			return err
		}
		if m != nil && m.Status == "live" {
			return qState("Матч в эфире — завершите его, чтобы вызвать следующих")
		}
		var taken []string
		if m != nil {
			if taken, err = q.matchEntries(m.ID); err != nil {
				return err
			}
			if len(taken) >= queueMatchSize {
				return qState("Матч уже собран")
			}
		}
		var e *qEntry
		if entryID == "" {
			next, ok := qNext(items)
			if !ok {
				return qState("В очереди некого вызывать")
			}
			e = byID[next.ID]
		} else if e, err = q.entry(entryID, byID); err != nil {
			return err
		}
		if !qEligible(e.Status) {
			return qState("Эту запись уже вызвали")
		}
		if m == nil {
			m = &qMatchRow{Status: "preparing"}
			var by *string
			if q.actor != "" {
				by = &q.actor
			}
			err := q.tx.QueryRow(q.ctx, `
				INSERT INTO queue_matches (session_id, number, created_by)
				VALUES ($1, (SELECT COALESCE(MAX(number), 0) + 1 FROM queue_matches WHERE session_id = $1), $2)
				RETURNING id, number`, q.sess.ID, by).Scan(&m.ID, &m.Number)
			if err != nil {
				return err
			}
		}
		side := 1
		if len(taken) > 0 {
			var used int
			if err := q.tx.QueryRow(q.ctx, `SELECT side FROM queue_match_entries WHERE match_id = $1`, m.ID).Scan(&used); err != nil {
				return err
			}
			if used == 1 {
				side = 2
			}
		}
		mmr, snapshot, err := q.snapshotEntry(e)
		if err != nil {
			return err
		}
		if _, err := q.tx.Exec(q.ctx, `
			INSERT INTO queue_match_entries (match_id, entry_id, side, label, mmr, snapshot) VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
			m.ID, e.ID, side, e.Label, mmr, snapshot); err != nil {
			return err
		}
		if _, err := q.tx.Exec(q.ctx, `
			UPDATE queue_entries SET status = 'invited', invited_at = now(), invite_expires_at = now() + make_interval(secs => $2)
			WHERE id = $1`, e.ID, q.sess.TimeoutSec); err != nil {
			return err
		}
		if _, err := q.tx.Exec(q.ctx, `UPDATE queue_entry_members SET ready_at = NULL WHERE entry_id = $1`, e.ID); err != nil {
			return err
		}
		items = qPlaceCalled(items, e.ID)
		for i := range items {
			if items[i].ID == e.ID {
				items[i].Status = QEntryInvited
			}
		}
		if err := q.writeOrder(items); err != nil {
			return err
		}
		q.res.EntryID = e.ID
		return q.event("invite", e.ID, m.ID, e.Label, map[string]any{"match": m.Number, "side": side, "timeoutSec": q.sess.TimeoutSec})
	})
}

// snapshotEntry фиксирует игроков записи и её MMR на момент вызова - для истории матчей.
func (q *qtx) snapshotEntry(e *qEntry) (int, string, error) {
	rows, err := q.tx.Query(q.ctx, `
		SELECT u.id, u.login, u.display_name, u.avatar_url FROM queue_entry_members m JOIN users u ON u.id = m.user_id
		WHERE m.entry_id = $1 ORDER BY lower(COALESCE(NULLIF(u.display_name, ''), u.login)), u.id`, e.ID)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	members := []models.QueueMember{}
	for rows.Next() {
		var m models.QueueMember
		if err := rows.Scan(&m.UserID, &m.Login, &m.DisplayName, &m.AvatarURL); err != nil {
			return 0, "", err
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	rows.Close()
	raw, _ := json.Marshal(members)
	var start, sum int
	if err := q.tx.QueryRow(q.ctx, `SELECT COALESCE((SELECT start_mmr FROM seasons WHERE status = 'active' LIMIT 1), $1)`, StartMmr).Scan(&start); err != nil {
		return 0, "", err
	}
	if q.sess.Format == "2x2" {
		err = q.tx.QueryRow(q.ctx, `
			SELECT COALESCE(SUM(h.delta), 0)::int FROM team_mmr_history h
			WHERE h.team_key = $1 AND h.season_key = COALESCE((SELECT id FROM seasons WHERE status = 'active' LIMIT 1), '')`, e.TeamKey).Scan(&sum)
	} else if len(members) > 0 {
		err = q.tx.QueryRow(q.ctx, `
			SELECT COALESCE(SUM(h.delta), 0)::int FROM mmr_history h
			WHERE h.user_id = $1 AND h.mode = '1x1' AND h.season_key = COALESCE((SELECT id FROM seasons WHERE status = 'active' LIMIT 1), '')`, members[0].UserID).Scan(&sum)
	}
	if err != nil {
		return 0, "", err
	}
	return start + sum, string(raw), nil
}

// ConfirmEntry - организатор подтверждает готовность вызванной записи за игроков (не могут нажать сами).
func (s *Store) ConfirmEntry(ctx context.Context, sessionID, actor, entryID string) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		_, byID, err := q.load()
		if err != nil {
			return err
		}
		e, err := q.entry(entryID, byID)
		if err != nil {
			return err
		}
		if e.Status != QEntryInvited && e.Status != QEntryReady {
			return qState("Запись не вызвана на матч")
		}
		if _, err := q.tx.Exec(q.ctx, `UPDATE queue_entry_members SET ready_at = COALESCE(ready_at, now()) WHERE entry_id = $1`, e.ID); err != nil {
			return err
		}
		q.res.EntryID = e.ID
		if e.Status == QEntryReady {
			return nil
		}
		return q.refreshReady(e)
	})
}

// QueueMatchStart - как создать матч сайта для вызванной пары.
type QueueMatchStart struct {
	Force      bool   // создать, не дожидаясь подтверждения готовности
	Rounds     int    // 2 или 3 раунда, по умолчанию 2
	PlayerType string // pve | pvp | pvpve, по умолчанию pvp
}

// StartQueueMatch создаёт матч сайта для двух вызванных (он сразу становится текущим, дальше пики-баны и раунды
// идут в консоли матча) и переводит их в эфир. Без Force нужны обе стороны и подтверждение готовности каждой.
func (s *Store) StartQueueMatch(ctx context.Context, sessionID, actor string, in QueueMatchStart) (QueueResult, error) {
	if in.Rounds == 0 {
		in.Rounds = 2
	}
	if in.PlayerType == "" {
		in.PlayerType = "pvp"
	}
	if in.Rounds != 2 && in.Rounds != 3 {
		return QueueResult{}, qInvalid("в матче два или три раунда")
	}
	if in.PlayerType != "pve" && in.PlayerType != "pvp" && in.PlayerType != "pvpve" {
		return QueueResult{}, qInvalid("тип игроков - pve, pvp или pvpve")
	}
	var created string
	res, err := s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		if q.sess.Paused {
			return qState("Очередь приостановлена")
		}
		m, err := q.openMatch()
		if err != nil {
			return err
		}
		if m == nil {
			return qState("Никто не вызван")
		}
		if m.Status != "preparing" {
			return qState("Матч уже в эфире")
		}
		ids, err := q.matchEntries(m.ID)
		if err != nil {
			return err
		}
		if len(ids) < queueMatchSize {
			return qState("Матч не собран: вызовите второго участника")
		}
		_, byID, err := q.load()
		if err != nil {
			return err
		}
		match := NewMatch{Mode: q.sess.Format, PlayerType: in.PlayerType, Format: FormatMatch, Rounds: in.Rounds}
		for i, id := range ids {
			e, ok := byID[id]
			if !ok {
				return qState("Участник матча ушёл из очереди")
			}
			if !in.Force && e.Status != QEntryReady {
				return qState("Не все подтвердили готовность")
			}
			if q.sess.Format == "2x2" {
				match.Sides[i] = MatchSide{TeamKey: e.TeamKey}
				continue
			}
			var uid string
			if err := q.tx.QueryRow(q.ctx, `SELECT user_id FROM queue_entry_members WHERE entry_id = $1`, id).Scan(&uid); err != nil {
				return err
			}
			match.Sides[i] = MatchSide{UserID: uid}
		}
		t, err := s.CreateMatch(ctx, match)
		switch {
		case errors.Is(err, ErrLiveMatchExists):
			return qState("Сейчас идёт другой матч - завершите или отмените его, потом создавайте этот")
		case errors.Is(err, ErrConflict):
			return qState("Стороны матча должны быть разными игроками")
		case errors.Is(err, ErrNotFound):
			return qState("Игрок или команда матча не найдены")
		case err != nil:
			return err
		}
		created = t.ID
		for _, id := range ids {
			if _, err := q.tx.Exec(q.ctx, `UPDATE queue_entries SET status = 'performing', invite_expires_at = NULL WHERE id = $1`, id); err != nil {
				return err
			}
		}
		if _, err := q.tx.Exec(q.ctx, `UPDATE queue_matches SET status = 'live', started_at = now(), tournament_id = $2 WHERE id = $1`, m.ID, t.ID); err != nil {
			return err
		}
		q.res.MatchID = t.ID
		return q.event("match_started", "", m.ID, "", map[string]any{"match": m.Number, "forced": in.Force, "tournamentId": t.ID})
	})
	if err != nil && created != "" {
		_ = s.DeleteTournament(ctx, created) // очередь не приняла матч - настоящий матч не остаётся без пары
	}
	return res, err
}

// linkedSiteMatchOpen - у матча очереди есть матч сайта, который ещё не завершён.
func (q *qtx) linkedSiteMatchOpen(matchID string) (bool, error) {
	var status *string
	err := q.tx.QueryRow(q.ctx, `
		SELECT t.status FROM queue_matches m JOIN tournaments t ON t.id = m.tournament_id WHERE m.id = $1`, matchID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return status != nil && *status != "finished", err
}

// FinishQueueMatch фиксирует выступление вручную: игроки идут в конец очереди, число их выступлений растёт.
// Пока матч сайта идёт, его завершают в консоли матча - очередь продвинется сама.
func (s *Store) FinishQueueMatch(ctx context.Context, sessionID, actor string) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		m, err := q.openMatch()
		if err != nil {
			return err
		}
		if m == nil || m.Status != "live" {
			return qState("Нет матча в эфире")
		}
		if open, err := q.linkedSiteMatchOpen(m.ID); err != nil {
			return err
		} else if open {
			return qState("Матч сайта ещё идёт: завершите его в консоли матча, очередь продвинется сама")
		}
		return q.finishMatch(m)
	})
}

func (q *qtx) finishMatch(m *qMatchRow) error {
	ids, err := q.matchEntries(m.ID)
	if err != nil {
		return err
	}
	items, byID, err := q.load()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, alive := byID[id]; !alive {
			continue // запись закрыта (аккаунт удалён) - в очередь не возвращается
		}
		if _, err := q.tx.Exec(q.ctx, `UPDATE queue_entries SET performed_count = performed_count + 1 WHERE id = $1`, id); err != nil {
			return err
		}
		if err := q.resetEntry(id, QEntryWaiting); err != nil {
			return err
		}
		items = qPlaceBack(items, id)
	}
	if err := q.writeOrder(items); err != nil {
		return err
	}
	if _, err := q.tx.Exec(q.ctx, `UPDATE queue_matches SET status = 'finished', finished_at = now() WHERE id = $1`, m.ID); err != nil {
		return err
	}
	return q.event("match_finished", "", m.ID, "", map[string]any{"match": m.Number})
}

// CancelQueueMatch отменяет подготовку: вызванные возвращаются в ожидание, выступление не засчитывается.
// Матч сайта, уже созданный для пары, отменяют в консоли матча - очередь вернётся сама.
func (s *Store) CancelQueueMatch(ctx context.Context, sessionID, actor string) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		m, err := q.openMatch()
		if err != nil {
			return err
		}
		if m == nil {
			return qState("Нет матча для отмены")
		}
		if open, err := q.linkedSiteMatchOpen(m.ID); err != nil {
			return err
		} else if open {
			return qState("Матч сайта уже создан: отмените его в консоли матча, очередь вернётся сама")
		}
		return q.cancelMatch(m, "cancel")
	})
}

// QueueMatchRef - матч очереди, к которому привязан матч сайта.
type QueueMatchRef struct{ SessionID, MatchID string }

// QueueMatchOfTournament - матч очереди, идущий вместе с матчем сайта.
func (s *Store) QueueMatchOfTournament(ctx context.Context, tournamentID string) (QueueMatchRef, bool) {
	var ref QueueMatchRef
	err := s.Pool.QueryRow(ctx, `
		SELECT session_id, id FROM queue_matches WHERE tournament_id = $1 AND status = 'live'`, tournamentID).Scan(&ref.SessionID, &ref.MatchID)
	return ref, err == nil
}

var errQueueNothing = errors.New("нечего менять")

// SettleQueueMatch продвигает очередь вслед за матчем сайта: завершён - участники уходят в конец, отменён -
// возвращаются в ожидание. Ничего не делает, если матч очереди уже не идёт.
func (s *Store) SettleQueueMatch(ctx context.Context, ref QueueMatchRef, actor string, finished bool) (QueueResult, bool, error) {
	res, err := s.queueTx(ctx, ref.SessionID, actor, func(q *qtx) error {
		m, err := q.openMatch()
		if err != nil {
			return err
		}
		if m == nil || m.ID != ref.MatchID || m.Status != "live" {
			return errQueueNothing
		}
		if finished {
			return q.finishMatch(m)
		}
		return q.cancelMatch(m, "site_match_cancelled")
	})
	if errors.Is(err, errQueueNothing) {
		return QueueResult{}, false, nil
	}
	return res, err == nil, err
}

func (q *qtx) cancelMatch(m *qMatchRow, reason string) error {
	ids, err := q.matchEntries(m.ID)
	if err != nil {
		return err
	}
	_, byID, err := q.load()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, alive := byID[id]; !alive {
			continue
		}
		if err := q.resetEntry(id, QEntryWaiting); err != nil {
			return err
		}
	}
	if _, err := q.tx.Exec(q.ctx, `UPDATE queue_matches SET status = 'cancelled', finished_at = now() WHERE id = $1`, m.ID); err != nil {
		return err
	}
	return q.event("match_cancelled", "", m.ID, "", map[string]any{"match": m.Number, "reason": reason})
}

// ---------- Управление записями ----------

// SkipEntry пропускает запись. mode "end" - в конец очереди (метка «Пропущен»), "remove" - исключить из сессии.
// Пропуск вызванного, который не подтвердил готовность, записывается как неявка.
func (s *Store) SkipEntry(ctx context.Context, sessionID, actor, entryID, mode string) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		if mode == "" {
			mode = "end"
		}
		if mode != "end" && mode != "remove" {
			return qInvalid("пропустить можно в конец очереди или исключить из сессии")
		}
		items, byID, err := q.load()
		if err != nil {
			return err
		}
		e, err := q.entry(entryID, byID)
		if err != nil {
			return err
		}
		if e.Status == QEntryPerforming {
			return qState("Участник в эфире — завершите или отмените матч")
		}
		kind := "skip"
		if e.Status == QEntryInvited {
			kind = "no_show"
		}
		q.res.EntryID = e.ID
		if mode == "remove" {
			if err := q.closeEntry(e.ID, QEntryRemoved); err != nil {
				return err
			}
			items, _, _ = qWithout(items, e.ID)
			if err := q.writeOrder(items); err != nil {
				return err
			}
			return q.event(kind, e.ID, "", e.Label, map[string]any{"mode": "remove"})
		}
		if err := q.detach(e.ID); err != nil {
			return err
		}
		if err := q.resetEntry(e.ID, QEntrySkipped); err != nil {
			return err
		}
		if _, err := q.tx.Exec(q.ctx, `UPDATE queue_entries SET skipped_count = skipped_count + 1 WHERE id = $1`, e.ID); err != nil {
			return err
		}
		for i := range items {
			if items[i].ID == e.ID {
				items[i].Status = QEntrySkipped
			}
		}
		if err := q.writeOrder(qPlaceSkipped(items, e.ID)); err != nil {
			return err
		}
		return q.event(kind, e.ID, "", e.Label, map[string]any{"mode": "end"})
	})
}

// ReturnEntry возвращает пропущенного: position пуст - он становится следующим на вызов, иначе встаёт на позицию.
func (s *Store) ReturnEntry(ctx context.Context, sessionID, actor, entryID string, position int) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		items, byID, err := q.load()
		if err != nil {
			return err
		}
		e, err := q.entry(entryID, byID)
		if err != nil {
			return err
		}
		if e.Status != QEntrySkipped {
			return qState("Вернуть можно только пропущенного")
		}
		if err := q.resetEntry(e.ID, QEntryWaiting); err != nil {
			return err
		}
		for i := range items {
			if items[i].ID == e.ID {
				items[i].Status = QEntryWaiting
			}
		}
		if position <= 0 {
			position = qInflight(items) + 1
		}
		if err := q.writeOrder(qMoveTo(items, e.ID, position)); err != nil {
			return err
		}
		q.res.EntryID = e.ID
		return q.event("return", e.ID, "", e.Label, map[string]any{"position": position})
	})
}

// MoveEntry вручную переставляет запись на позицию toPosition (с 1). expectedRev > 0 - переставлять только если
// очередь не менялась с тех пор, как организатор её видел.
func (s *Store) MoveEntry(ctx context.Context, sessionID, actor, entryID string, toPosition int, expectedRev int64) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		if expectedRev > 0 && expectedRev != q.sess.Rev {
			return &QueueError{ErrQueueStale, "Очередь уже изменилась — порядок обновлён, попробуйте ещё раз"}
		}
		items, byID, err := q.load()
		if err != nil {
			return err
		}
		e, err := q.entry(entryID, byID)
		if err != nil {
			return err
		}
		if qInMatch(e.Status) {
			return qState("Вызванного на матч переставлять нельзя — сначала пропустите или отмените матч")
		}
		if toPosition < 1 || toPosition > len(items) {
			return qInvalid(fmt.Sprintf("позиция — от 1 до %d", len(items)))
		}
		items = qMoveTo(items, e.ID, toPosition)
		if err := q.writeOrder(items); err != nil {
			return err
		}
		q.res.EntryID = e.ID
		return q.event("move", e.ID, "", e.Label, map[string]any{"from": e.Position, "to": qIndex(items, e.ID) + 1})
	})
}

// RemoveEntry убирает запись из очереди (организатор). Выступающего убрать нельзя.
func (s *Store) RemoveEntry(ctx context.Context, sessionID, actor, entryID string) (QueueResult, error) {
	return s.queueTx(ctx, sessionID, actor, func(q *qtx) error {
		if err := q.needActive(); err != nil {
			return err
		}
		items, byID, err := q.load()
		if err != nil {
			return err
		}
		e, err := q.entry(entryID, byID)
		if err != nil {
			return err
		}
		if e.Status == QEntryPerforming {
			return qState("Участник в эфире — завершите или отмените матч")
		}
		if err := q.closeEntry(e.ID, QEntryRemoved); err != nil {
			return err
		}
		items, _, _ = qWithout(items, e.ID)
		if err := q.writeOrder(items); err != nil {
			return err
		}
		q.res.EntryID = e.ID
		return q.event("remove", e.ID, "", e.Label, nil)
	})
}
