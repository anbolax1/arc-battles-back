package store

import (
	"context"
	"errors"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// isPlaceholderHash — хеш-заглушка импортного аккаунта (вход невозможен). Настоящие bcrypt-хеши
// начинаются с "$"; заглушки мы ставим с префиксом "!".
func isPlaceholderHash(h string) bool { return strings.HasPrefix(h, "!") }

// userPlaceholder сообщает, является ли аккаунт заглушкой.
func (s *Store) userPlaceholder(ctx context.Context, userID string) (bool, error) {
	var hash string
	err := s.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, userID).Scan(&hash)
	if err != nil {
		return false, err
	}
	return isPlaceholderHash(hash), nil
}

// EnsureClaimToken возвращает актуальный токен активации заглушки (существующий или новый).
// ok=false — если аккаунт не заглушка (уже с настоящим паролем) или не найден. Если regenerate —
// прежние токены удаляются и создаётся свежий.
func (s *Store) EnsureClaimToken(ctx context.Context, userID, newToken string, regenerate bool) (string, bool, error) {
	ph, err := s.userPlaceholder(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if !ph {
		return "", false, nil
	}
	if regenerate {
		if _, err := s.Pool.Exec(ctx, `DELETE FROM account_claim_tokens WHERE user_id=$1`, userID); err != nil {
			return "", false, err
		}
	} else {
		var existing string
		err := s.Pool.QueryRow(ctx,
			`SELECT token FROM account_claim_tokens WHERE user_id=$1 ORDER BY created_at DESC LIMIT 1`, userID).Scan(&existing)
		if err == nil {
			return existing, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
	}
	if _, err := s.Pool.Exec(ctx,
		`INSERT INTO account_claim_tokens (token, user_id) VALUES ($1,$2)`, newToken, userID); err != nil {
		return "", false, err
	}
	return newToken, true, nil
}

// LookupClaimToken возвращает пользователя по токену активации для показа формы (без расхода).
// ok=false — токен недействителен или аккаунт уже активирован (тогда токен удаляется).
func (s *Store) LookupClaimToken(ctx context.Context, token string) (models.User, bool, error) {
	var uid string
	err := s.Pool.QueryRow(ctx, `SELECT user_id FROM account_claim_tokens WHERE token=$1`, token).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, false, nil
	}
	if err != nil {
		return models.User{}, false, err
	}
	if ph, err := s.userPlaceholder(ctx, uid); err != nil || !ph {
		if err == nil {
			_, _ = s.Pool.Exec(ctx, `DELETE FROM account_claim_tokens WHERE token=$1`, token)
		}
		return models.User{}, false, err
	}
	u, err := s.GetUser(ctx, uid)
	return u, err == nil, err
}

// ClaimAccount ставит игроку настоящий пароль по токену и гасит токен (одноразовость).
// Возвращает пользователя и его id для сессии. ok=false — токен недействителен/уже активирован.
func (s *Store) ClaimAccount(ctx context.Context, token, passwordHash string) (models.User, bool, error) {
	var uid string
	err := s.Pool.QueryRow(ctx, `SELECT user_id FROM account_claim_tokens WHERE token=$1`, token).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, false, nil
	}
	if err != nil {
		return models.User{}, false, err
	}
	if ph, err := s.userPlaceholder(ctx, uid); err != nil || !ph {
		return models.User{}, false, err
	}
	if _, err := s.Pool.Exec(ctx,
		`UPDATE users SET password_hash=$2, updated_at=now() WHERE id=$1`, uid, passwordHash); err != nil {
		return models.User{}, false, err
	}
	// Гасим все токены этого пользователя — ссылка одноразовая.
	if _, err := s.Pool.Exec(ctx, `DELETE FROM account_claim_tokens WHERE user_id=$1`, uid); err != nil {
		return models.User{}, false, err
	}
	u, err := s.GetUser(ctx, uid)
	return u, err == nil, err
}
