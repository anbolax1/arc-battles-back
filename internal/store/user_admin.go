package store

import (
	"context"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// NotEmptyError - профиль нельзя удалить: за ним есть история, которую каскад унёс бы вместе с аккаунтом.
type NotEmptyError struct{ Reason string }

func (e *NotEmptyError) Error() string { return "профиль не пустой: " + e.Reason }

// RenameUser принудительно меняет ник: он же логин и имя на сайте, поэтому поиск и вход идут по новому.
// Прежние логин и имя сохраняются псевдонимами: по ним игрок всё ещё входит, а новичок их занять не может.
// Прежние копии имени в матчах (участники, составы пар) переписываются, чтобы ник был один везде.
func (s *Store) RenameUser(ctx context.Context, id, nick string) (models.User, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return models.User{}, err
	}
	defer tx.Rollback(ctx)

	// Тот же замок, что у регистрации: проверка занятости и запись не должны разойтись при гонке.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('register'))`); err != nil {
		return models.User{}, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&exists); err != nil {
		return models.User{}, err
	}
	if !exists {
		return models.User{}, ErrNotFound
	}
	var taken bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE id <> $1 AND (lower(login) = lower($2) OR lower(display_name) = lower($2)))
		    OR EXISTS (SELECT 1 FROM user_login_aliases WHERE user_id <> $1 AND lower(alias) = lower($2))`,
		id, nick).Scan(&taken); err != nil {
		return models.User{}, err
	}
	if taken {
		return models.User{}, ErrNickTaken
	}

	// Прежние логин и имя остаются входом; собственный прежний ник, к которому возвращаются, из них убираем.
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_login_aliases (alias, user_id)
		SELECT DISTINCT ON (lower(a)) a, $1 FROM (SELECT login AS a FROM users WHERE id = $1
		                                          UNION ALL SELECT display_name FROM users WHERE id = $1) o
		WHERE a <> '' AND lower(a) <> lower($2)
		ON CONFLICT DO NOTHING`, id, nick); err != nil {
		return models.User{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_login_aliases WHERE user_id = $1 AND lower(alias) = lower($2)`, id, nick); err != nil {
		return models.User{}, err
	}
	u, err := scanUser(tx.QueryRow(ctx,
		`UPDATE users SET login = $2, display_name = $2, updated_at = now() WHERE id = $1 RETURNING `+userCols, id, nick))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return models.User{}, ErrNickTaken
		}
		return models.User{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE participants SET name = $2 WHERE user_id = $1`, id, nick); err != nil {
		return models.User{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE participants SET members = (
			SELECT jsonb_agg(CASE WHEN m ->> 'userId' = $1 THEN jsonb_set(m, '{name}', to_jsonb($2::text)) ELSE m END)
			FROM jsonb_array_elements(members) m)
		WHERE jsonb_typeof(members) = 'array'
		  AND members @> jsonb_build_array(jsonb_build_object('userId', $1::text))`, id, nick); err != nil {
		return models.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.User{}, err
	}
	return u, nil
}

// DeleteEmptyUser удаляет профиль, за которым ничего нет: ни матчей, ни команд, ни очереди, ни жетонов,
// ни скриншотов и клипов. Остальное (теги, заявки, ссылки входа, любимое) уходит вместе с аккаунтом.
// Иначе возвращает *NotEmptyError с причиной: история игрока не должна пропасть молча.
func (s *Store) DeleteEmptyUser(ctx context.Context, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Блокируем строку, чтобы за время проверки игрока не успели записать в матч.
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}

	// Порядок - от самой заметной причины к менее заметным.
	checks := []struct{ reason, sql string }{
		{"есть матчи", `SELECT EXISTS (SELECT 1 FROM participants p WHERE p.user_id = $1
			OR (jsonb_typeof(p.members) = 'array' AND p.members @> jsonb_build_array(jsonb_build_object('userId', $1::text))))`},
		{"состоит в команде", `SELECT EXISTS (SELECT 1 FROM teams WHERE member_a = $1 OR member_b = $1)
			OR EXISTS (SELECT 1 FROM team_mmr WHERE member_a = $1 OR member_b = $1)`},
		{"есть история рейтинга", `SELECT EXISTS (SELECT 1 FROM mmr_history WHERE user_id = $1)
			OR EXISTS (SELECT 1 FROM mmr_corrections WHERE user_id = $1)`},
		{"стоит в очереди выступлений", `SELECT EXISTS (SELECT 1 FROM queue_entry_members WHERE user_id = $1 AND active)`},
		{"есть жетоны", `SELECT EXISTS (SELECT 1 FROM token_ledger WHERE user_id = $1)`},
		{"есть скриншоты на проверке", `SELECT EXISTS (SELECT 1 FROM stat_claims WHERE user_id = $1)`},
		{"есть хайлайты", `SELECT EXISTS (SELECT 1 FROM highlights WHERE user_id = $1)`},
	}
	for _, c := range checks {
		var found bool
		if err := tx.QueryRow(ctx, c.sql, id).Scan(&found); err != nil {
			return err
		}
		if found {
			return &NotEmptyError{Reason: c.reason}
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
