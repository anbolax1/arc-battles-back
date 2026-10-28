package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Виды жетонов.
const (
	TokenDouble   = "double"   // изменение MMR в матче вдвое - и победа, и поражение
	TokenPriority = "priority" // без очереди до конца игрового дня
	TokenReset    = "reset"    // MMR 1×1 снова стартовый, раз в сезон
)

func ValidTokenKind(k string) bool { return k == TokenDouble || k == TokenPriority || k == TokenReset }

const (
	// priorityCooldown - «Без очереди» активируется не чаще раза в сутки.
	priorityCooldown = 24 * time.Hour
	// MaxTokenGrant - больше за раз не выдают: лишний ноль не превратится в сотню жетонов.
	MaxTokenGrant = 100
)

var (
	ErrNoTokens       = errors.New("жетонов этого вида нет")
	ErrTokenBalance   = errors.New("столько жетонов у игрока нет")
	ErrTokenCooldown  = errors.New("«Без очереди» уже активировали за последние сутки")
	ErrResetUsed      = errors.New("в этом сезоне рейтинг уже сбрасывали")
	ErrNoSeason       = errors.New("сезон не идёт - сбрасывать нечего")
	ErrInMatch        = errors.New("игрок сейчас в матче 1×1 - сбросить рейтинг можно после него")
	ErrNothingToReset = errors.New("MMR и так стартовый - сбрасывать нечего")
	ErrSideHasToken   = errors.New("у этой стороны жетон ×2 уже стоит")
	ErrNotInMatch     = errors.New("игрок не играет в этом матче")
)

// rowQuerier - пул или транзакция: проверки перед тратой жетона повторяются внутри неё.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// lockUser - траты жетонов одного игрока идут по одной: двойное нажатие не потратит жетон дважды.
func lockUser(ctx context.Context, tx pgx.Tx, userID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func tokenBalance(ctx context.Context, q rowQuerier, userID, kind string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(delta), 0)::int FROM token_ledger WHERE user_id = $1 AND kind = $2`, userID, kind).Scan(&n)
	return n, err
}

// priorityUntil - «Без очереди» действует до конца игрового дня по Москве, в который его активировали.
func priorityUntil(at time.Time) time.Time { return mskDay(at).AddDate(0, 0, 1) }

// TokenWallet - сколько у игрока жетонов и когда их можно тратить.
func (s *Store) TokenWallet(ctx context.Context, userID string) (models.TokenWallet, error) {
	var w models.TokenWallet
	rows, err := s.Pool.Query(ctx, `SELECT kind, COALESCE(SUM(delta), 0)::int FROM token_ledger WHERE user_id = $1 GROUP BY kind`, userID)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			rows.Close()
			return w, err
		}
		switch kind {
		case TokenDouble:
			w.Double = n
		case TokenPriority:
			w.Priority = n
		case TokenReset:
			w.Reset = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return w, err
	}
	var lastPriority *time.Time
	if err := s.Pool.QueryRow(ctx, `
		SELECT (SELECT MAX(created_at) FROM token_ledger WHERE user_id = $1 AND kind = 'priority' AND action = 'use'),
		       (SELECT MAX(created_at) FROM token_ledger WHERE user_id = $1 AND kind = 'reset' AND action = 'use'),
		       EXISTS (SELECT 1 FROM token_ledger l JOIN seasons sn ON sn.id = l.season_id
		               WHERE l.user_id = $1 AND l.kind = 'reset' AND l.action = 'use' AND sn.status = 'active')`, userID).
		Scan(&lastPriority, &w.LastReset, &w.ResetUsed); err != nil {
		return w, err
	}
	if lastPriority != nil {
		now := time.Now()
		if until := priorityUntil(*lastPriority); until.After(now) {
			w.PriorityUntil = &until
		}
		if next := lastPriority.Add(priorityCooldown); next.After(now) {
			w.PriorityNext = &next
		}
	}
	return w, nil
}

// GrantTokens выдаёт игроку жетоны или забирает выданные по ошибке (delta < 0): меньше нуля их не станет.
func (s *Store) GrantTokens(ctx context.Context, userID, kind string, delta int, note, by string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	bal, err := tokenBalance(ctx, tx, userID, kind)
	if err != nil {
		return err
	}
	if bal+delta < 0 {
		return ErrTokenBalance
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO token_ledger (user_id, kind, action, delta, note, created_by) VALUES ($1, $2, 'grant', $3, $4, NULLIF($5, ''))`,
		userID, kind, delta, note, by); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ActivatePriority тратит жетон «Без очереди»: до конца игрового дня игрок в пуле заявок выше остальных.
func (s *Store) ActivatePriority(ctx context.Context, userID, by string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	bal, err := tokenBalance(ctx, tx, userID, TokenPriority)
	if err != nil {
		return err
	}
	if bal < 1 {
		return ErrNoTokens
	}
	var last *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT MAX(created_at) FROM token_ledger WHERE user_id = $1 AND kind = 'priority' AND action = 'use'`, userID).Scan(&last); err != nil {
		return err
	}
	if last != nil && time.Since(*last) < priorityCooldown {
		return ErrTokenCooldown
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO token_ledger (user_id, kind, action, delta, created_by) VALUES ($1, 'priority', 'use', -1, NULLIF($2, ''))`,
		userID, by); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CancelPriority возвращает жетон «Без очереди», активированный по ошибке, пока его игровой день не кончился.
func (s *Store) CancelPriority(ctx context.Context, userID string) error {
	ct, err := s.Pool.Exec(ctx, `
		DELETE FROM token_ledger WHERE id = (
			SELECT id FROM token_ledger WHERE user_id = $1 AND kind = 'priority' AND action = 'use' AND created_at >= $2
			ORDER BY created_at DESC LIMIT 1)`, userID, mskDay(time.Now()))
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// sideOf - сторона матча, за которую играет userID.
func sideOf(parts []models.Participant, userID string) (models.Participant, bool) {
	for _, p := range parts {
		if slices.Contains(participantUsers(p), userID) {
			return p, true
		}
	}
	return models.Participant{}, false
}

// SetDoubleToken ставит жетон ×2 игрока на матч или снимает его, и жетон возвращается. Только пока матч
// не начался: с первым раундом ставка известна обоим. В 2×2 жетон удваивает MMR всей команды, поэтому
// на сторону ставится один.
func (s *Store) SetDoubleToken(ctx context.Context, tournamentID, userID string, on bool, by string) error {
	parts, err := s.ListParticipants(ctx, tournamentID)
	if err != nil {
		return err
	}
	side, ok := sideOf(parts, userID)
	if !ok {
		return ErrNotInMatch
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	var status string
	var started bool
	err = tx.QueryRow(ctx, `
		SELECT status, EXISTS(SELECT 1 FROM rounds WHERE tournament_id = $1 AND status <> 'pending')
		FROM tournaments WHERE id = $1`, tournamentID).Scan(&status, &started)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "finished" || started {
		return ErrMatchStarted
	}
	if !on {
		if _, err := tx.Exec(ctx, `DELETE FROM token_ledger WHERE tournament_id = $1 AND user_id = $2 AND kind = 'double'`, tournamentID, userID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var mine, side2 bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM token_ledger WHERE tournament_id = $1 AND kind = 'double' AND user_id = $2),
		       EXISTS(SELECT 1 FROM token_ledger WHERE tournament_id = $1 AND kind = 'double' AND user_id = ANY($3))`,
		tournamentID, userID, participantUsers(side)).Scan(&mine, &side2); err != nil {
		return err
	}
	if mine {
		return nil
	}
	if side2 {
		return ErrSideHasToken
	}
	bal, err := tokenBalance(ctx, tx, userID, TokenDouble)
	if err != nil {
		return err
	}
	if bal < 1 {
		return ErrNoTokens
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO token_ledger (user_id, kind, action, delta, tournament_id, created_by)
		VALUES ($1, 'double', 'use', -1, $2, NULLIF($3, ''))`, userID, tournamentID, by); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// doubledUsers - кто поставил жетон ×2 на матч.
func (s *Store) doubledUsers(ctx context.Context, tournamentID string) (map[string]bool, error) {
	rows, err := s.Pool.Query(ctx, `SELECT user_id FROM token_ledger WHERE tournament_id = $1 AND kind = 'double'`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out[u] = true
	}
	return out, rows.Err()
}

// tokenMult - жетон ×2 удваивает изменение MMR тому, кто его поставил; в 2×2 - всей его команде.
func tokenMult(doubled map[string]bool, users ...string) int {
	for _, u := range users {
		if doubled[u] {
			return 2
		}
	}
	return 1
}

// MatchDoubles - жетоны ×2 на матче по сторонам и сколько таких жетонов осталось у игроков сторон.
func (s *Store) MatchDoubles(ctx context.Context, parts []models.Participant, tournamentID string) ([]models.DoubleToken, map[string]int, error) {
	doubled, err := s.doubledUsers(ctx, tournamentID)
	if err != nil {
		return nil, nil, err
	}
	tokens := []models.DoubleToken{}
	var users []string
	for _, p := range parts {
		for _, u := range participantUsers(p) {
			users = append(users, u)
			if doubled[u] {
				tokens = append(tokens, models.DoubleToken{ParticipantID: p.ID, UserID: u})
			}
		}
	}
	wallet := map[string]int{}
	for _, u := range users {
		wallet[u] = 0
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT user_id, COALESCE(SUM(delta), 0)::int FROM token_ledger
		WHERE kind = 'double' AND user_id = ANY($1) GROUP BY user_id`, users)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		var n int
		if err := rows.Scan(&u, &n); err != nil {
			return nil, nil, err
		}
		wallet[u] = n
	}
	return tokens, wallet, rows.Err()
}

// PendingMatches - матчи игрока, которые ещё не начались: на них модератор ставит жетон ×2.
func (s *Store) PendingMatches(ctx context.Context, userID string) ([]models.PendingMatch, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT t.id, t.title, t.mode, t.starts_at,
		       EXISTS(SELECT 1 FROM token_ledger l WHERE l.tournament_id = t.id AND l.kind = 'double' AND l.user_id = $1),
		       EXISTS(SELECT 1 FROM token_ledger l WHERE l.tournament_id = t.id AND l.kind = 'double' AND l.user_id <> $1
		              AND (l.user_id = p.user_id OR p.members @> jsonb_build_array(jsonb_build_object('userId', l.user_id))))
		FROM participants p JOIN tournaments t ON t.id = p.tournament_id
		WHERE t.status IN ('upcoming', 'live')
		  AND NOT EXISTS (SELECT 1 FROM rounds r WHERE r.tournament_id = t.id AND r.status <> 'pending')
		  AND (p.user_id = $1 OR (jsonb_typeof(p.members) = 'array'
		       AND p.members @> jsonb_build_array(jsonb_build_object('userId', $1::text))))
		ORDER BY COALESCE(t.starts_at, t.created_at)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.PendingMatch{}
	for rows.Next() {
		var m models.PendingMatch
		var mate bool
		if err := rows.Scan(&m.TournamentID, &m.Title, &m.Mode, &m.StartsAt, &m.Active, &mate); err != nil {
			return nil, err
		}
		if mate {
			m.Blocked = ErrSideHasToken.Error()
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// TokenLedger - журнал жетонов игрока для модератора, свежие сверху.
func (s *Store) TokenLedger(ctx context.Context, userID string, limit int) ([]models.TokenEntry, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT l.id, l.kind, l.delta, l.note, l.tournament_id, COALESCE(t.title, ''),
		       COALESCE(NULLIF(b.display_name, ''), b.login, ''), l.created_at
		FROM token_ledger l
		LEFT JOIN tournaments t ON t.id = l.tournament_id
		LEFT JOIN users b ON b.id = l.created_by
		WHERE l.user_id = $1
		ORDER BY l.created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.TokenEntry{}
	for rows.Next() {
		var e models.TokenEntry
		if err := rows.Scan(&e.ID, &e.Kind, &e.Delta, &e.Note, &e.TournamentID, &e.Match, &e.By, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// resetCheck - можно ли сейчас сбросить рейтинг: нужен жетон, идущий сезон, ни одного сброса в нём и MMR не
// стартовый. Пока идёт матч 1×1 игрока - нельзя: его итог считался бы от рейтинга до сброса. sum - изменение
// MMR в сезоне.
func resetCheck(ctx context.Context, q rowQuerier, userID string, rule seasonRule) (sum int, err error) {
	if rule.Key == "" {
		return 0, ErrNoSeason
	}
	var bal int
	var used, inMatch bool
	if err := q.QueryRow(ctx, `
		SELECT (SELECT COALESCE(SUM(delta), 0)::int FROM token_ledger WHERE user_id = $1 AND kind = 'reset'),
		       EXISTS (SELECT 1 FROM token_ledger WHERE user_id = $1 AND kind = 'reset' AND action = 'use' AND season_id = $2),
		       EXISTS (SELECT 1 FROM tournaments t JOIN participants p ON p.tournament_id = t.id
		               WHERE p.user_id = $1 AND t.mode = '1x1'
		                 AND (t.status = 'live' OR (t.status = 'upcoming' AND t.starts_at <= now()))),
		       (SELECT COALESCE(SUM(delta), 0)::int FROM mmr_history WHERE user_id = $1 AND mode = '1x1' AND season_key = $2)`,
		userID, rule.Key).Scan(&bal, &used, &inMatch, &sum); err != nil {
		return 0, err
	}
	switch {
	case bal < 1:
		return sum, ErrNoTokens
	case used:
		return sum, ErrResetUsed
	case inMatch:
		return sum, ErrInMatch
	case sum == 0:
		return sum, ErrNothingToReset
	}
	return sum, nil
}

// ResetBlocked - почему игрок сейчас не может сбросить рейтинг; nil - может.
func (s *Store) ResetBlocked(ctx context.Context, userID string) error {
	_, err := resetCheck(ctx, s.Pool, userID, s.activeSeasonRule(ctx))
	return err
}

// ResetRating тратит жетон сброса: MMR 1×1 игрока в текущем сезоне снова стартовый. Матчи, победы и нашивки
// остаются. Сброс - отдельная строка истории MMR, и полный пересчёт рейтинга повторяет его в своё время.
func (s *Store) ResetRating(ctx context.Context, userID string) error {
	rule := s.activeSeasonRule(ctx)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	sum, err := resetCheck(ctx, tx, userID, rule)
	if err != nil {
		return err
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO token_ledger (user_id, kind, action, delta, season_id, created_by)
		VALUES ($1, 'reset', 'use', -1, $2, $1) RETURNING created_at`, userID, rule.Key).Scan(&at); err != nil {
		return err
	}
	if err := insertReset(ctx, tx, userID, rule, rule.Start+sum, at); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.recomputeUserMmr(ctx, userID, "1x1")
}

// insertReset пишет в историю сброс рейтинга: от before снова к стартовому MMR сезона.
func insertReset(ctx context.Context, q rowQuerier, userID string, rule seasonRule, before int, at time.Time) error {
	_, err := q.Exec(ctx, `
		INSERT INTO mmr_history (user_id, mode, tournament_id, delta, mmr_before, mmr_after, season_key, created_at, reset)
		VALUES ($1, '1x1', NULL, $2, $3, $4, $5, $6, true)`,
		userID, rule.Start-before, before, rule.Start, rule.Key, at)
	return err
}

// applyReset повторяет сброс при пересчёте рейтинга: сколько бы ни было до него, после - стартовый MMR.
func (s *Store) applyReset(ctx context.Context, userID, seasonID string, at time.Time) error {
	var rule seasonRule
	if err := s.Pool.QueryRow(ctx, `SELECT id, k_factor, start_mmr FROM seasons WHERE id = $1`, seasonID).
		Scan(&rule.Key, &rule.K, &rule.Start); err != nil {
		return err
	}
	before, err := s.userSeasonMmr(ctx, userID, "1x1", rule)
	if err != nil {
		return err
	}
	return insertReset(ctx, s.Pool, userID, rule, before, at)
}
