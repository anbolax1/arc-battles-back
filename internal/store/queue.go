package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// QueueMaxEntries - потолок записей в одной очереди: защита от засорения, на стриме столько не бывает.
const QueueMaxEntries = 300

var (
	// ErrQueueState - действие не подходит текущему состоянию очереди (закрыта, идёт матч, уже вызван).
	ErrQueueState = errors.New("действие недоступно в текущем состоянии очереди")
	// ErrQueueInvalid - неверные входные данные.
	ErrQueueInvalid = errors.New("некорректные данные")
	// ErrQueueForbidden - игроку нельзя это сделать (чужая команда).
	ErrQueueForbidden = errors.New("недостаточно прав для этого действия")
	// ErrQueueStale - очередь изменилась с тех пор, как организатор её видел.
	ErrQueueStale = errors.New("очередь уже изменилась")
)

// QueueError - ошибка очереди с понятным игроку текстом; Kind - одна из ошибок выше.
type QueueError struct {
	Kind error
	Msg  string
}

func (e *QueueError) Error() string { return e.Msg }
func (e *QueueError) Unwrap() error { return e.Kind }

func qState(msg string) error     { return &QueueError{ErrQueueState, msg} }
func qInvalid(msg string) error   { return &QueueError{ErrQueueInvalid, msg} }
func qForbidden(msg string) error { return &QueueError{ErrQueueForbidden, msg} }

// QueueSessionState - состояние сессии для людей: open | closed | paused | finished.
func QueueSessionState(status string, registrationOpen, paused bool) string {
	switch {
	case status == "finished":
		return "finished"
	case paused:
		return "paused"
	case !registrationOpen:
		return "closed"
	}
	return "open"
}

const queueSessionCols = `s.id, s.title, s.format, s.status, s.registration_open, s.paused, s.confirm_timeout_sec,
	s.match_size, s.rev, s.created_at, s.finished_at, COALESCE(NULLIF(cu.display_name, ''), cu.login, ''),
	(SELECT COUNT(*) FROM queue_entries e WHERE e.session_id = s.id AND e.position IS NOT NULL)::int,
	(SELECT COUNT(*) FROM queue_matches m WHERE m.session_id = s.id AND m.status = 'finished')::int`

const queueSessionFrom = ` FROM queue_sessions s LEFT JOIN users cu ON cu.id = s.created_by `

func scanQueueSession(row pgx.Row) (models.QueueSession, error) {
	var q models.QueueSession
	var status string
	err := row.Scan(&q.ID, &q.Title, &q.Format, &status, &q.RegistrationOpen, &q.Paused, &q.ConfirmTimeoutSec,
		&q.MatchSize, &q.Rev, &q.CreatedAt, &q.FinishedAt, &q.CreatedBy, &q.Waiting, &q.Played)
	q.State = QueueSessionState(status, q.RegistrationOpen, q.Paused)
	return q, err
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func getQueueSession(ctx context.Context, db queryer, id string) (models.QueueSession, error) {
	q, err := scanQueueSession(db.QueryRow(ctx, `SELECT `+queueSessionCols+queueSessionFrom+`WHERE s.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return q, ErrNotFound
	}
	return q, err
}

// ListQueueSessions - сессии очереди, новые сверху.
func (s *Store) ListQueueSessions(ctx context.Context, limit int) ([]models.QueueSession, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+queueSessionCols+queueSessionFrom+`ORDER BY s.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.QueueSession{}
	for rows.Next() {
		q, err := scanQueueSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// activeQueueRefs - живые очереди всех форматов.
func activeQueueRefs(ctx context.Context, db queryer) ([]models.QueueSessionRef, error) {
	rows, err := db.Query(ctx, `
		SELECT id, format, registration_open, paused, title FROM queue_sessions
		WHERE status = 'active' ORDER BY format, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.QueueSessionRef{}
	for rows.Next() {
		var r models.QueueSessionRef
		var reg, paused bool
		if err := rows.Scan(&r.ID, &r.Format, &reg, &paused, &r.Title); err != nil {
			return nil, err
		}
		r.State = QueueSessionState("active", reg, paused)
		out = append(out, r)
	}
	return out, rows.Err()
}

// QueueActiveID - id живой очереди формата; пустой format - любой формат, у 1×1 приоритет.
func (s *Store) QueueActiveID(ctx context.Context, format string) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `
		SELECT id FROM queue_sessions WHERE status = 'active' AND ($1 = '' OR format = $1)
		ORDER BY format, created_at LIMIT 1`, format).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// queueEntryRows - записи очереди с игроками по позициям; в блокирующих запросах db - транзакция.
func queueEntryRows(ctx context.Context, db queryer, sessionID string) ([]models.QueueEntry, error) {
	rows, err := db.Query(ctx, `
		SELECT e.id, e.position, e.status, e.team_key, e.team_name, e.joined_at, e.performed_count, e.skipped_count,
		       e.invited_at, e.invite_expires_at, e.added_by IS NOT NULL
		FROM queue_entries e WHERE e.session_id = $1 AND e.position IS NOT NULL ORDER BY e.position`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.QueueEntry{}
	for rows.Next() {
		var e models.QueueEntry
		var teamKey *string
		var teamName string
		if err := rows.Scan(&e.ID, &e.Position, &e.Status, &teamKey, &teamName, &e.JoinedAt, &e.PerformedCount,
			&e.SkippedCount, &e.InvitedAt, &e.InviteExpiresAt, &e.AddedByOrganizer); err != nil {
			return nil, err
		}
		if teamKey != nil {
			e.TeamKey = *teamKey
			e.Name = teamName
		}
		e.Members = []models.QueueMember{}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	members, err := queueMembers(ctx, db, sessionID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Members = members[out[i].ID]
		if out[i].Name == "" && len(out[i].Members) > 0 {
			out[i].Name = queueMemberName(out[i].Members[0])
		}
	}
	return out, nil
}

func queueMemberName(m models.QueueMember) string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Login
}

// queueMembers - игроки записей сессии, у которых запись ещё в очереди.
func queueMembers(ctx context.Context, db queryer, sessionID string) (map[string][]models.QueueMember, error) {
	rows, err := db.Query(ctx, `
		SELECT m.entry_id, u.id, u.login, u.display_name, u.avatar_url, m.ready_at
		FROM queue_entry_members m JOIN users u ON u.id = m.user_id
		WHERE m.session_id = $1 AND m.entry_id IN (SELECT id FROM queue_entries WHERE session_id = $1 AND position IS NOT NULL)
		ORDER BY m.entry_id, lower(COALESCE(NULLIF(u.display_name, ''), u.login)), u.id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]models.QueueMember{}
	for rows.Next() {
		var entryID string
		var m models.QueueMember
		if err := rows.Scan(&entryID, &m.UserID, &m.Login, &m.DisplayName, &m.AvatarURL, &m.ReadyAt); err != nil {
			return nil, err
		}
		m.Ready = m.ReadyAt != nil
		out[entryID] = append(out[entryID], m)
	}
	return out, rows.Err()
}

// queueMmrs - MMR записей в текущем сезоне: игрока для 1×1, команды для 2×2. Не игравшие - со старта сезона.
func (s *Store) queueMmrs(ctx context.Context, db queryer, format string, entries []models.QueueEntry) (map[string]int, error) {
	rule := s.activeSeasonRule(ctx)
	byKey := map[string]int{} // user id или ключ команды -> сумма изменений
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		if format == "2x2" {
			keys = append(keys, e.TeamKey)
		} else if len(e.Members) > 0 {
			keys = append(keys, e.Members[0].UserID)
		}
	}
	q := `SELECT user_id, SUM(delta)::int FROM mmr_history WHERE user_id = ANY($1) AND mode = '1x1' AND season_key = $2 GROUP BY user_id`
	if format == "2x2" {
		q = `SELECT team_key, SUM(delta)::int FROM team_mmr_history WHERE team_key = ANY($1) AND season_key = $2 GROUP BY team_key`
	}
	rows, err := db.Query(ctx, q, keys, rule.Key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var sum int
		if err := rows.Scan(&k, &sum); err != nil {
			return nil, err
		}
		byKey[k] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(entries))
	for _, e := range entries {
		k := e.TeamKey
		if format != "2x2" && len(e.Members) > 0 {
			k = e.Members[0].UserID
		}
		out[e.ID] = rule.Start + byKey[k]
	}
	return out, nil
}

// queuePriorityToday - у кого из игроков сегодня действует жетон «Без очереди».
func queuePriorityToday(ctx context.Context, db queryer, userIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `
		SELECT DISTINCT user_id FROM token_ledger
		WHERE user_id = ANY($1) AND kind = 'priority' AND action = 'use' AND created_at >= $2`, userIDs, mskDay(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// queueMatchRows - матчи сессии: onlyOpen - только идущий (подготовка или эфир), иначе все по порядку.
func queueMatchRows(ctx context.Context, db queryer, sessionID string, onlyOpen bool, limit int) ([]models.QueueMatch, error) {
	rows, err := db.Query(ctx, `
		SELECT id, number, status, created_at, started_at, finished_at
		FROM queue_matches WHERE session_id = $1 AND (NOT $2 OR status IN ('preparing', 'live'))
		ORDER BY number DESC LIMIT $3`, sessionID, onlyOpen, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.QueueMatch{}
	ids := []string{}
	for rows.Next() {
		var m models.QueueMatch
		if err := rows.Scan(&m.ID, &m.Number, &m.Status, &m.CreatedAt, &m.StartedAt, &m.FinishedAt); err != nil {
			return nil, err
		}
		m.Sides = []models.QueueMatchSide{}
		out = append(out, m)
		ids = append(ids, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(ids) == 0 {
		return out, nil
	}
	srows, err := db.Query(ctx, `
		SELECT me.match_id, me.side, me.entry_id, me.label, me.mmr, me.snapshot, COALESCE(e.status, '')
		FROM queue_match_entries me LEFT JOIN queue_entries e ON e.id = me.entry_id
		WHERE me.match_id = ANY($1) ORDER BY me.match_id, me.side`, ids)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	sides := map[string][]models.QueueMatchSide{}
	for srows.Next() {
		var matchID string
		var sd models.QueueMatchSide
		if err := srows.Scan(&matchID, &sd.Side, &sd.EntryID, &sd.Name, &sd.Mmr, &sd.Members, &sd.Status); err != nil {
			return nil, err
		}
		if sd.Members == nil {
			sd.Members = []models.QueueMember{}
		}
		sides[matchID] = append(sides[matchID], sd)
	}
	if err := srows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if s := sides[out[i].ID]; s != nil {
			out[i].Sides = s
		}
	}
	return out, nil
}

// QueueSnapshotFor собирает состояние очереди одним согласованным срезом. viewer - id вошедшего игрока или пусто.
func (s *Store) QueueSnapshotFor(ctx context.Context, sessionID, viewer string) (*models.QueueSnapshot, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	snap := &models.QueueSnapshot{ServerTime: time.Now().UTC(), Entries: []models.QueueEntry{}}
	if snap.Active, err = activeQueueRefs(ctx, tx); err != nil {
		return nil, err
	}
	if sessionID == "" {
		snap.Me = &models.QueueMe{JoinBlocked: "Очередь сейчас не открыта"}
		return snap, nil
	}
	sess, err := getQueueSession(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	snap.Session = &sess
	if snap.Entries, err = queueEntryRows(ctx, tx, sessionID); err != nil {
		return nil, err
	}
	mmrs, err := s.queueMmrs(ctx, tx, sess.Format, snap.Entries)
	if err != nil {
		return nil, err
	}
	var userIDs []string
	for _, e := range snap.Entries {
		for _, m := range e.Members {
			userIDs = append(userIDs, m.UserID)
		}
	}
	prio, err := queuePriorityToday(ctx, tx, userIDs)
	if err != nil {
		return nil, err
	}
	items := make([]queueItem, len(snap.Entries))
	for i := range snap.Entries {
		e := &snap.Entries[i]
		e.Mmr = mmrs[e.ID]
		for _, m := range e.Members {
			if prio[m.UserID] {
				e.Priority = true
			}
		}
		items[i] = queueItem{ID: e.ID, Status: e.Status, Performed: e.PerformedCount}
	}
	if sess.State != "finished" {
		if next, ok := qNext(items); ok {
			for i := range snap.Entries {
				if snap.Entries[i].ID == next.ID {
					snap.Entries[i].IsNext = true
				}
			}
		}
	}

	matches, err := queueMatchRows(ctx, tx, sessionID, true, 1)
	if err != nil {
		return nil, err
	}
	if len(matches) > 0 {
		snap.Match = &matches[0]
	}
	hist, err := queueMatchRows(ctx, tx, sessionID, false, 200)
	if err != nil {
		return nil, err
	}
	snap.History = []models.QueueMatch{}
	for i := range hist {
		if hist[i].Status == "finished" || hist[i].Status == "cancelled" {
			snap.History = append(snap.History, hist[i])
		}
	}
	for i := range snap.History {
		if snap.History[i].Status == "finished" {
			snap.Last = &snap.History[i]
			break
		}
	}
	if snap.Me, err = s.queueMe(ctx, tx, &sess, snap.Entries, viewer); err != nil {
		return nil, err
	}
	return snap, nil
}

// queueMe считает для игрока его место, соседей и возможность записаться.
func (s *Store) queueMe(ctx context.Context, db queryer, sess *models.QueueSession, entries []models.QueueEntry, viewer string) (*models.QueueMe, error) {
	me := &models.QueueMe{}
	if sess.State == "finished" {
		me.JoinBlocked = "Сессия завершена"
		return me, nil
	}
	if viewer == "" {
		switch sess.State {
		case "paused":
			me.JoinBlocked = "Очередь приостановлена организатором"
		case "closed":
			me.JoinBlocked = "Запись закрыта организатором"
		default:
			me.JoinBlocked = "Войдите, чтобы встать в очередь"
		}
		return me, nil
	}
	idx := -1
	for i, e := range entries {
		if slices.ContainsFunc(e.Members, func(m models.QueueMember) bool { return m.UserID == viewer }) {
			idx = i
			break
		}
	}
	if idx >= 0 {
		e := entries[idx]
		me.EntryID, me.Position, me.Status = e.ID, e.Position, e.Status
		me.Ahead = idx
		if idx > 0 {
			before := entries[idx-1]
			me.Before = &before
		}
		me.CanLeave = e.Status != QEntryPerforming
		for _, m := range e.Members {
			if m.UserID == viewer && e.Status == QEntryInvited {
				me.NeedsConfirm = !m.Ready
				me.Confirmed = m.Ready
			}
		}
		return me, nil
	}
	switch {
	case sess.State == "paused":
		me.JoinBlocked = "Очередь приостановлена организатором"
	case sess.State == "closed":
		me.JoinBlocked = "Запись закрыта организатором"
	case len(entries) >= QueueMaxEntries:
		me.JoinBlocked = "Очередь заполнена"
	}
	if sess.Format == "2x2" {
		teams, err := s.queueTeamOptions(ctx, db, entries, viewer)
		if err != nil {
			return nil, err
		}
		me.Teams = teams
		if me.JoinBlocked == "" {
			if len(teams) == 0 {
				me.JoinBlocked = "У вас нет команды 2×2. Команду заводит организатор"
			} else if !slices.ContainsFunc(teams, func(t models.QueueTeamOption) bool { return t.Blocked == "" }) {
				me.JoinBlocked = "Все ваши команды уже в очереди или у них занят напарник"
			}
		}
	}
	me.CanJoin = me.JoinBlocked == ""
	return me, nil
}

// queueTeamOptions - команды игрока и можно ли ими записаться: ни команда, ни её игроки не должны стоять в очереди.
func (s *Store) queueTeamOptions(ctx context.Context, db queryer, entries []models.QueueEntry, viewer string) ([]models.QueueTeamOption, error) {
	rows, err := db.Query(ctx, `
		SELECT t.team_key, t.name, ua.id, ua.login, ua.display_name, ua.avatar_url, ub.id, ub.login, ub.display_name, ub.avatar_url
		FROM teams t JOIN users ua ON ua.id = t.member_a JOIN users ub ON ub.id = t.member_b
		WHERE t.member_a = $1 OR t.member_b = $1 ORDER BY lower(t.name)`, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	queued := map[string]bool{}
	for _, e := range entries {
		for _, m := range e.Members {
			queued[m.UserID] = true
		}
	}
	out := []models.QueueTeamOption{}
	for rows.Next() {
		var t models.QueueTeamOption
		var a, b models.QueueMember
		if err := rows.Scan(&t.TeamKey, &t.Name, &a.UserID, &a.Login, &a.DisplayName, &a.AvatarURL,
			&b.UserID, &b.Login, &b.DisplayName, &b.AvatarURL); err != nil {
			return nil, err
		}
		t.Members = []models.QueueMember{a, b}
		if queued[a.UserID] || queued[b.UserID] {
			t.Blocked = "Игрок команды уже в очереди"
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// QueueMatchHistory - сыгранные и отменённые матчи сессии, новые сверху.
func (s *Store) QueueMatchHistory(ctx context.Context, sessionID string) ([]models.QueueMatch, error) {
	return queueMatchRows(ctx, s.Pool, sessionID, false, 500)
}

// QueueEvents - журнал сессии (вызовы, пропуски, действия организатора), новые сверху.
func (s *Store) QueueEvents(ctx context.Context, sessionID string, limit int) ([]models.QueueEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT ev.id, ev.at, ev.kind, COALESCE(NULLIF(u.display_name, ''), u.login, ''), ev.label, ev.data
		FROM queue_events ev LEFT JOIN users u ON u.id = ev.actor_id
		WHERE ev.session_id = $1 ORDER BY ev.id DESC LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.QueueEvent{}
	for rows.Next() {
		var e models.QueueEvent
		if err := rows.Scan(&e.ID, &e.At, &e.Kind, &e.Actor, &e.Label, &e.Data); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
