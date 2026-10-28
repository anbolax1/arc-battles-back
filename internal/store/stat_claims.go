package store

import (
	"context"
	"errors"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// Что игрок вписывает в профиль со скриншотом из игры.
const (
	StatHours  = "hours"  // часов в игре
	StatKnocks = "knocks" // ноков за всё время
)

// StatLimits - больше этого в профиле игры не бывает: такое число - опечатка.
var StatLimits = map[string]int{
	StatHours:  20000,
	StatKnocks: 1000000,
}

const statClaimCols = `c.id, c.user_id, u.login, u.display_name, c.kind, c.value, c.screenshot, c.status, c.reason,
	c.created_at, c.decided_at,
	(SELECT p.value FROM stat_claims p WHERE p.user_id = c.user_id AND p.kind = c.kind AND p.status = 'approved'
	   AND p.created_at < c.created_at ORDER BY p.created_at DESC LIMIT 1)`

func scanStatClaim(row pgx.Row) (models.StatClaim, error) {
	var c models.StatClaim
	var shot string
	err := row.Scan(&c.ID, &c.UserID, &c.Login, &c.DisplayName, &c.Kind, &c.Value, &shot, &c.Status, &c.Reason,
		&c.CreatedAt, &c.DecidedAt, &c.Previous)
	c.ScreenshotURL = "/media/" + shot
	return c, err
}

// SubmitStatClaim записывает новое число со скриншотом. Прежняя непроверенная заявка того же вида
// заменяется: модератору незачем сверять устаревшее. Отдаёт файл заменённого скриншота - его удалить.
func (s *Store) SubmitStatClaim(ctx context.Context, userID, kind string, value int, screenshot string) (models.StatClaim, string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return models.StatClaim{}, "", err
	}
	defer tx.Rollback(ctx)
	var old string
	err = tx.QueryRow(ctx, `
		DELETE FROM stat_claims WHERE user_id = $1 AND kind = $2 AND status = 'pending' RETURNING screenshot`,
		userID, kind).Scan(&old)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return models.StatClaim{}, "", err
	}
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO stat_claims (user_id, kind, value, screenshot) VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, kind, value, screenshot).Scan(&id); err != nil {
		return models.StatClaim{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.StatClaim{}, "", err
	}
	c, err := s.GetStatClaim(ctx, id)
	return c, old, err
}

func (s *Store) GetStatClaim(ctx context.Context, id string) (models.StatClaim, error) {
	c, err := scanStatClaim(s.Pool.QueryRow(ctx, `
		SELECT `+statClaimCols+` FROM stat_claims c JOIN users u ON u.id = c.user_id WHERE c.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// PlayerStatValues - часы и ноки для профиля: последнее не отклонённое число каждого вида.
func (s *Store) PlayerStatValues(ctx context.Context, userID string) (hours, knocks *models.StatValue, err error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ON (kind) kind, value, status = 'approved'
		FROM stat_claims WHERE user_id = $1 AND status <> 'rejected'
		ORDER BY kind, created_at DESC`, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var v models.StatValue
		if err := rows.Scan(&kind, &v.Value, &v.Confirmed); err != nil {
			return nil, nil, err
		}
		switch kind {
		case StatHours:
			hours = &v
		case StatKnocks:
			knocks = &v
		}
	}
	return hours, knocks, rows.Err()
}

// MyStatClaims - последняя заявка игрока каждого вида: в своём профиле он видит, проверена ли она.
func (s *Store) MyStatClaims(ctx context.Context, userID string) ([]models.StatClaim, error) {
	return s.listStatClaims(ctx, `
		SELECT DISTINCT ON (c.kind) `+statClaimCols+`
		FROM stat_claims c JOIN users u ON u.id = c.user_id
		WHERE c.user_id = $1
		ORDER BY c.kind, c.created_at DESC`, userID)
}

// UserStatClaims - заявки игрока для модератора, свежие сверху.
func (s *Store) UserStatClaims(ctx context.Context, userID string, limit int) ([]models.StatClaim, error) {
	return s.listStatClaims(ctx, `
		SELECT `+statClaimCols+`
		FROM stat_claims c JOIN users u ON u.id = c.user_id
		WHERE c.user_id = $1
		ORDER BY (c.status = 'pending') DESC, c.created_at DESC LIMIT $2`, userID, limit)
}

// ListStatClaims - очередь проверки: непроверенные - сначала старые, проверенные - сначала свежие.
func (s *Store) ListStatClaims(ctx context.Context, status string, limit, offset int) ([]models.StatClaim, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	order := "c.created_at DESC"
	if status == "pending" {
		order = "c.created_at ASC"
	}
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM stat_claims WHERE ($1 = '' OR status = $1)`, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	items, err := s.listStatClaims(ctx, `
		SELECT `+statClaimCols+`
		FROM stat_claims c JOIN users u ON u.id = c.user_id
		WHERE ($1 = '' OR c.status = $1)
		ORDER BY `+order+` LIMIT $2 OFFSET $3`, status, limit, offset)
	return items, total, err
}

func (s *Store) listStatClaims(ctx context.Context, q string, args ...any) ([]models.StatClaim, error) {
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.StatClaim{}
	for rows.Next() {
		c, err := scanStatClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DecideStatClaim подтверждает число со скриншота или отклоняет его с причиной. Решение по уже
// проверенной заявке можно поменять: модератор мог ошибиться.
func (s *Store) DecideStatClaim(ctx context.Context, id string, approve bool, reason, by string) (models.StatClaim, error) {
	status := "rejected"
	if approve {
		status, reason = "approved", ""
	}
	ct, err := s.Pool.Exec(ctx, `
		UPDATE stat_claims SET status = $2, reason = $3, decided_at = $4, decided_by = NULLIF($5, '')
		WHERE id = $1`, id, status, reason, time.Now(), by)
	if err != nil {
		return models.StatClaim{}, err
	}
	if ct.RowsAffected() == 0 {
		return models.StatClaim{}, ErrNotFound
	}
	return s.GetStatClaim(ctx, id)
}
