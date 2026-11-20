package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// SnowballCooldown - пауза между бросками одного игрока: снежки не должны превращаться в спам.
const SnowballCooldown = 15 * time.Second

// recentThrowers - сколько последних бросавших показывается в профиле.
const recentThrowers = 5

// ErrSnowballSelf - в свой профиль снежки не бросают.
var ErrSnowballSelf = errors.New("в себя снежки не бросают")

// SnowballWaitError - бросок слишком скоро после прошлого; Wait - сколько ещё подождать.
type SnowballWaitError struct{ Wait time.Duration }

func (e SnowballWaitError) Error() string {
	return fmt.Sprintf("снежок ещё лепится, подождите %d с", waitSeconds(e.Wait))
}

func waitSeconds(d time.Duration) int {
	return int((d + time.Second - 1) / time.Second)
}

// ThrowSnowball записывает бросок. Проверка паузы и вставка идут под замком на бросающего,
// иначе два быстрых запроса подряд оба прошли бы проверку.
func (s *Store) ThrowSnowball(ctx context.Context, fromID, toID string) error {
	if fromID == toID {
		return ErrSnowballSelf
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // после Commit откат ничего не делает

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('snowball:' || $1))`, fromID); err != nil {
		return err
	}
	var since *float64
	if err := tx.QueryRow(ctx,
		`SELECT EXTRACT(EPOCH FROM now() - MAX(created_at)) FROM snowballs WHERE from_user = $1`, fromID).Scan(&since); err != nil {
		return err
	}
	if since != nil && *since < SnowballCooldown.Seconds() {
		return SnowballWaitError{Wait: time.Duration((SnowballCooldown.Seconds() - *since) * float64(time.Second))}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO snowballs (from_user, to_user) VALUES ($1, $2)`, fromID, toID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SnowballSummary - снежки в профиле игрока с его последнего завершённого матча (сыграл - счёт с нуля).
// viewerID пустой у гостя: тогда Me не заполняется.
func (s *Store) SnowballSummary(ctx context.Context, toID, viewerID string) (models.SnowballSummary, error) {
	out := models.SnowballSummary{Recent: []models.SnowballThrower{}}

	var lastMatch *time.Time
	if err := s.Pool.QueryRow(ctx, `
		SELECT MAX(COALESCE(t.starts_at, t.created_at))
		  FROM participants p
		  JOIN tournaments t ON t.id = p.tournament_id AND t.status = 'finished'
		 WHERE p.user_id = $1
		    OR (jsonb_typeof(p.members) = 'array'
		        AND p.members @> jsonb_build_array(jsonb_build_object('userId', $1::text)))`, toID).Scan(&lastMatch); err != nil {
		return out, err
	}
	from := time.Time{}
	if lastMatch != nil {
		from = *lastMatch
	}

	if err := s.Pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM snowballs WHERE to_user = $1 AND created_at > $2`, toID, from).Scan(&out.Count); err != nil {
		return out, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT u.login, u.display_name
		  FROM (SELECT from_user, MAX(created_at) AS at FROM snowballs
		         WHERE to_user = $1 AND created_at > $2 GROUP BY from_user) s
		  JOIN users u ON u.id = s.from_user
		 ORDER BY s.at DESC
		 LIMIT $3`, toID, from, recentThrowers)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var t models.SnowballThrower
		if err := rows.Scan(&t.Login, &t.DisplayName); err != nil {
			return out, err
		}
		out.Recent = append(out.Recent, t)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	if viewerID == "" {
		return out, nil
	}
	me := &models.SnowballMe{Self: viewerID == toID}
	var since *float64
	err = s.Pool.QueryRow(ctx,
		`SELECT EXTRACT(EPOCH FROM now() - MAX(created_at)) FROM snowballs WHERE from_user = $1`, viewerID).Scan(&since)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if since != nil && *since < SnowballCooldown.Seconds() {
		me.WaitSec = waitSeconds(time.Duration((SnowballCooldown.Seconds() - *since) * float64(time.Second)))
	}
	out.Me = me
	return out, nil
}
