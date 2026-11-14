package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// Кулдаун: после завершённого матча игрок какое-то время не может снова встать в очередь, ни в 1×1, ни в 2×2.
// Он идёт от времени завершения последнего матча игрока, поэтому не сбрасывается ни выходом из очереди,
// ни новой сессией, ни перезапуском сервера. Вручную организатор меняет только правила (player_cooldowns).
const (
	// CooldownDefaultMinutes - общая длительность, пока организатор не задал свою: 3 часа.
	CooldownDefaultMinutes = 180
	// CooldownMaxMinutes - неделя: дольше это уже не кулдаун, а отстранение от лиги.
	CooldownMaxMinutes = 7 * 24 * 60
)

const cooldownSettingKey = "queue_cooldown_minutes"

// ErrCooldownInvalid - длительность вне допустимых пределов или неизвестное действие.
var ErrCooldownInvalid = errors.New("длительность кулдауна - от 1 минуты до 7 дней")

// cooldownDefault - общая длительность в минутах; 0 - кулдаун выключен для всех без своей длительности.
func cooldownDefault(ctx context.Context, db queryer) (int, error) {
	var v string
	err := db.QueryRow(ctx, `SELECT value FROM site_settings WHERE key = $1`, cooldownSettingKey).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return CooldownDefaultMinutes, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > CooldownMaxMinutes {
		return CooldownDefaultMinutes, nil
	}
	return n, nil
}

// CooldownDefault - общая длительность кулдауна в минутах.
func (s *Store) CooldownDefault(ctx context.Context) (int, error) {
	return cooldownDefault(ctx, s.Pool)
}

// cooldownRule - ручные настройки кулдауна игрока; у большинства их нет.
type cooldownRule struct {
	Minutes     *int // своя длительность; nil - общая
	Disabled    bool
	ClearedAt   *time.Time // снят вручную: матчи до этого момента кулдаун не дают
	ManualUntil *time.Time // поставлен вручную до этого момента
}

// cooldownEnd - до какого момента игрок на кулдауне; last - когда завершился его последний матч,
// def - общая длительность. Нулевое время - кулдауна нет; прошедшее время - кулдаун уже кончился.
func cooldownEnd(r cooldownRule, last time.Time, def int) time.Time {
	if r.Disabled {
		return time.Time{}
	}
	minutes := def
	if r.Minutes != nil {
		minutes = *r.Minutes
	}
	var end time.Time
	if !last.IsZero() && minutes > 0 && (r.ClearedAt == nil || last.After(*r.ClearedAt)) {
		end = last.Add(time.Duration(minutes) * time.Minute)
	}
	if r.ManualUntil != nil && r.ManualUntil.After(end) {
		end = *r.ManualUntil
	}
	return end
}

func playerCooldownRules(ctx context.Context, db queryer, userIDs []string) (map[string]cooldownRule, error) {
	out := map[string]cooldownRule{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `
		SELECT user_id, minutes, disabled, cleared_at, manual_until FROM player_cooldowns WHERE user_id = ANY($1)`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var r cooldownRule
		if err := rows.Scan(&id, &r.Minutes, &r.Disabled, &r.ClearedAt, &r.ManualUntil); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

// queuePlayer - что очередь знает об игроке: история выступлений и до какого времени у него кулдаун.
type queuePlayer struct {
	History  playerHistory
	Cooldown time.Time // конец действующего кулдауна; нулевое - кулдауна нет
}

// CooldownPtr - конец действующего кулдауна для ответа API; нет кулдауна - пусто.
func (p queuePlayer) CooldownPtr() *time.Time {
	if p.Cooldown.IsZero() {
		return nil
	}
	t := p.Cooldown
	return &t
}

// queuePlayersOf - история и кулдаун игроков на момент now. withUnknown - см. playerHistories.
func queuePlayersOf(ctx context.Context, db queryer, userIDs []string, now time.Time, withUnknown bool) (map[string]queuePlayer, error) {
	hist, err := playerHistories(ctx, db, userIDs, now, withUnknown)
	if err != nil {
		return nil, err
	}
	rules, err := playerCooldownRules(ctx, db, userIDs)
	if err != nil {
		return nil, err
	}
	def, err := cooldownDefault(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make(map[string]queuePlayer, len(userIDs))
	for _, id := range userIDs {
		p := queuePlayer{History: hist[id]}
		if end := cooldownEnd(rules[id], p.History.Last, def); end.After(now) {
			p.Cooldown = end
		}
		out[id] = p
	}
	return out, nil
}

// mskClock - время по Москве для сообщений: «18:30», а не сегодня - «15.11 18:30».
func mskClock(t, now time.Time) string {
	if mskDay(t).Equal(mskDay(now)) {
		return t.In(moscow).Format("15:04")
	}
	return t.In(moscow).Format("02.01 15:04")
}

// ---------- Кабинет: правки кулдаунов ----------

// SetCooldownDefault меняет общую длительность кулдауна сразу для всех, у кого нет своей.
func (s *Store) SetCooldownDefault(ctx context.Context, actor string, minutes int) error {
	if minutes < 0 || minutes > CooldownMaxMinutes {
		return ErrCooldownInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Две правки подряд не теряют друг друга в журнале: строка настройки заблокирована до конца.
	if _, err := tx.Exec(ctx, `
		INSERT INTO site_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`,
		cooldownSettingKey, strconv.Itoa(CooldownDefaultMinutes)); err != nil {
		return err
	}
	var prev string
	if err := tx.QueryRow(ctx, `SELECT value FROM site_settings WHERE key = $1 FOR UPDATE`, cooldownSettingKey).Scan(&prev); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE site_settings SET value = $2, updated_at = now() WHERE key = $1`,
		cooldownSettingKey, strconv.Itoa(minutes)); err != nil {
		return err
	}
	from, _ := strconv.Atoi(prev)
	if err := cooldownLog(ctx, tx, actor, "", "", "default", map[string]any{"from": from, "to": minutes}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Действия с кулдауном игрока из кабинета.
const (
	CooldownDuration = "duration" // своя длительность (Minutes)
	CooldownUseDef   = "default"  // вернуть общую длительность
	CooldownDisable  = "disable"  // кулдаун не действует совсем
	CooldownEnable   = "enable"   // снова как у всех
	CooldownClear    = "clear"    // снять текущий кулдаун
	CooldownSet      = "set"      // поставить вручную на Minutes от текущего момента
)

// ChangePlayerCooldown меняет кулдаун игрока и пишет правку в журнал. Отдаёт кулдаун после правки.
func (s *Store) ChangePlayerCooldown(ctx context.Context, actor, userID, action string, minutes int) (models.CooldownPlayer, error) {
	if (action == CooldownDuration || action == CooldownSet) && (minutes < 1 || minutes > CooldownMaxMinutes) {
		return models.CooldownPlayer{}, ErrCooldownInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return models.CooldownPlayer{}, err
	}
	defer tx.Rollback(ctx)

	var label string
	err = tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(display_name, ''), login) FROM users WHERE id = $1`, userID).Scan(&label)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.CooldownPlayer{}, ErrNotFound
	}
	if err != nil {
		return models.CooldownPlayer{}, err
	}
	// Строка игрока заблокирована до конца: одновременные правки идут по очереди и не затирают друг друга.
	if _, err := tx.Exec(ctx, `INSERT INTO player_cooldowns (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return models.CooldownPlayer{}, err
	}
	var r cooldownRule
	if err := tx.QueryRow(ctx, `
		SELECT minutes, disabled, cleared_at, manual_until FROM player_cooldowns WHERE user_id = $1 FOR UPDATE`, userID).
		Scan(&r.Minutes, &r.Disabled, &r.ClearedAt, &r.ManualUntil); err != nil {
		return models.CooldownPlayer{}, err
	}
	now := time.Now()
	data := map[string]any{}
	switch action {
	case CooldownDuration:
		data["from"], data["to"] = r.Minutes, minutes
		r.Minutes = &minutes
	case CooldownUseDef:
		data["from"] = r.Minutes
		r.Minutes = nil
	case CooldownDisable:
		r.Disabled = true
	case CooldownEnable:
		r.Disabled = false
	case CooldownClear:
		// Матчи до этого момента кулдаун больше не дают, ручной тоже снят; следующий матч начнёт новый.
		r.ClearedAt, r.ManualUntil = &now, nil
	case CooldownSet:
		// Ровно столько, сколько поставили: кулдаун от прошлых матчей больше не продлевает его.
		until := now.Add(time.Duration(minutes) * time.Minute)
		r.ClearedAt, r.ManualUntil = &now, &until
		data["minutes"], data["until"] = minutes, until
	default:
		return models.CooldownPlayer{}, ErrCooldownInvalid
	}
	if _, err := tx.Exec(ctx, `
		UPDATE player_cooldowns SET minutes = $2, disabled = $3, cleared_at = $4, manual_until = $5,
		       updated_at = now(), updated_by = NULLIF($6, '')
		WHERE user_id = $1`, userID, r.Minutes, r.Disabled, r.ClearedAt, r.ManualUntil, actor); err != nil {
		return models.CooldownPlayer{}, err
	}
	if err := cooldownLog(ctx, tx, actor, userID, label, action, data); err != nil {
		return models.CooldownPlayer{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.CooldownPlayer{}, err
	}
	list, err := s.cooldownPlayers(ctx, []string{userID}, time.Now())
	if err != nil {
		return models.CooldownPlayer{}, err
	}
	if len(list) == 0 {
		return models.CooldownPlayer{}, ErrNotFound
	}
	return list[0], nil
}

func cooldownLog(ctx context.Context, tx pgx.Tx, actor, userID, label, action string, data map[string]any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO cooldown_log (actor_id, user_id, label, action, data) VALUES (NULLIF($1, ''), NULLIF($2, ''), $3, $4, $5::jsonb)`,
		actor, userID, label, action, string(raw))
	return err
}

// cooldownSearchLimit - сколько игроков находит поиск по нику: дальше уточняют запрос.
const cooldownSearchLimit = 30

// CooldownOverview - игроки для раздела кулдаунов. С поиском - найденные по нику; без поиска - все,
// у кого кулдаун идёт сейчас или заданы свои правила.
func (s *Store) CooldownOverview(ctx context.Context, query string) (models.CooldownOverview, error) {
	now := time.Now()
	out := models.CooldownOverview{ServerTime: now.UTC(), Players: []models.CooldownPlayer{}}
	def, err := cooldownDefault(ctx, s.Pool)
	if err != nil {
		return out, err
	}
	out.DefaultMinutes = def
	query = strings.TrimSpace(query)
	var rows pgx.Rows
	if query != "" {
		rows, err = s.Pool.Query(ctx, `
			SELECT id FROM users
			WHERE strpos(lower(login), lower($1)) > 0 OR strpos(lower(display_name), lower($1)) > 0
			ORDER BY lower(login) = lower($1) DESC, lower(COALESCE(NULLIF(display_name, ''), login)) LIMIT $2`,
			query, cooldownSearchLimit)
	} else {
		// Дольше недели кулдаун не бывает: сыгравших раньше незачем проверять.
		rows, err = s.Pool.Query(ctx, `
			SELECT p.user_id FROM participants p JOIN tournaments t ON t.id = p.tournament_id
			WHERE t.status = 'finished' AND p.user_id IS NOT NULL AND `+matchMomentSQL+` >= $1
			UNION
			SELECT m ->> 'userId' FROM participants p JOIN tournaments t ON t.id = p.tournament_id,
			     jsonb_array_elements(CASE WHEN jsonb_typeof(p.members) = 'array' THEN p.members ELSE '[]'::jsonb END) m
			WHERE t.status = 'finished' AND COALESCE(m ->> 'userId', '') <> '' AND `+matchMomentSQL+` >= $1
			UNION
			SELECT user_id FROM player_cooldowns`, now.Add(-CooldownMaxMinutes*time.Minute))
	}
	if err != nil {
		return out, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	list, err := s.cooldownPlayers(ctx, ids, now)
	if err != nil {
		return out, err
	}
	if query != "" {
		out.Players = list
		return out, nil
	}
	for _, p := range list {
		if p.Until != nil || p.Minutes != nil || p.Disabled {
			out.Players = append(out.Players, p)
		}
	}
	// Сначала те, кто на кулдауне, - кто освободится раньше, тот выше; дальше остальные по нику.
	sort.SliceStable(out.Players, func(i, j int) bool {
		a, b := out.Players[i], out.Players[j]
		if (a.Until != nil) != (b.Until != nil) {
			return a.Until != nil
		}
		if a.Until != nil && !a.Until.Equal(*b.Until) {
			return a.Until.Before(*b.Until)
		}
		return strings.ToLower(cooldownName(a)) < strings.ToLower(cooldownName(b))
	})
	return out, nil
}

func cooldownName(p models.CooldownPlayer) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Login
}

// cooldownPlayers - кулдаун и история игроков для кабинета в порядке ids; несуществующих пропускает.
func (s *Store) cooldownPlayers(ctx context.Context, ids []string, now time.Time) ([]models.CooldownPlayer, error) {
	out := []models.CooldownPlayer{}
	if len(ids) == 0 {
		return out, nil
	}
	players, err := queuePlayersOf(ctx, s.Pool, ids, now, true)
	if err != nil {
		return nil, err
	}
	rules, err := playerCooldownRules(ctx, s.Pool, ids)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT u.id, u.login, u.display_name, u.avatar_url,
		       EXISTS (SELECT 1 FROM queue_entry_members m JOIN queue_sessions qs ON qs.id = m.session_id
		               WHERE m.user_id = u.id AND m.active AND qs.status = 'active')
		FROM users u WHERE u.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]models.CooldownPlayer{}
	for rows.Next() {
		var p models.CooldownPlayer
		if err := rows.Scan(&p.UserID, &p.Login, &p.DisplayName, &p.AvatarURL, &p.InQueue); err != nil {
			return nil, err
		}
		qp, r := players[p.UserID], rules[p.UserID]
		p.Minutes, p.Disabled = r.Minutes, r.Disabled
		if r.ManualUntil != nil && r.ManualUntil.After(now) {
			p.ManualUntil = r.ManualUntil
		}
		p.Until = qp.CooldownPtr()
		p.TodayMatches, p.TotalMatches = qp.History.Today, qp.History.Total
		p.LastMatchAt = qp.History.LastPtr()
		p.Newbie, p.HistoryUnknown = qp.History.Newbie(), qp.History.Unknown
		byID[p.UserID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			out = append(out, p)
			delete(byID, id) // повтор id в списке - один игрок
		}
	}
	return out, nil
}

// cooldownLogLimit - сколько последних правок видно в кабинете.
const cooldownLogLimit = 100

// CooldownLog - последние правки кулдаунов, новые сверху; userID - только об этом игроке.
func (s *Store) CooldownLog(ctx context.Context, userID string) ([]models.CooldownLogEntry, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT l.id, l.at, COALESCE(NULLIF(a.display_name, ''), a.login, ''), COALESCE(l.user_id, ''), l.label, l.action, l.data
		FROM cooldown_log l LEFT JOIN users a ON a.id = l.actor_id
		WHERE ($1 = '' OR l.user_id = $1) ORDER BY l.id DESC LIMIT $2`, userID, cooldownLogLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.CooldownLogEntry{}
	for rows.Next() {
		var e models.CooldownLogEntry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.UserID, &e.Label, &e.Action, &e.Data); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// cooldownBlock - почему игроки не могут встать в очередь из-за кулдауна: текст для того, кто записывает
// (viewer; organizer - записывает организатор). Пусто - ни у кого кулдауна нет.
func cooldownBlock(players map[string]queuePlayer, names map[string]string, members []string, viewer string, organizer bool, now time.Time) string {
	for _, u := range members {
		end := players[u].Cooldown
		if end.IsZero() {
			continue
		}
		at := mskClock(end, now)
		switch {
		case u == viewer && !organizer:
			return fmt.Sprintf("Вы уже выступали. Следующая запись будет доступна с %s МСК", at)
		case organizer:
			return fmt.Sprintf("У игрока %s кулдаун до %s МСК - если нужно, снимите его в разделе «Кулдауны»", names[u], at)
		default:
			return fmt.Sprintf("У игрока %s кулдаун до %s МСК: записать команду можно после него", names[u], at)
		}
	}
	return ""
}
