package store

import (
	"context"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const userCols = `id, twitch_id, login, display_name, avatar_url, email, role, embark_id, created_at`

func scanUser(row pgx.Row) (models.User, error) {
	var u models.User
	var role string
	err := row.Scan(&u.ID, &u.TwitchID, &u.Login, &u.DisplayName, &u.AvatarURL, &u.Email, &role, &u.EmbarkID, &u.CreatedAt)
	u.Role = models.Role(role)
	return u, err
}

// UpsertTwitchUser создаёт или обновляет пользователя по twitch_id.
// Роль существующего пользователя не трогаем; defaultRole применяется только при создании.
func (s *Store) UpsertTwitchUser(ctx context.Context, twitchID, login, displayName, avatar, email string, defaultRole models.Role) (models.User, error) {
	const q = `
		INSERT INTO users (twitch_id, login, display_name, avatar_url, email, role)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (twitch_id) DO UPDATE
			SET login = EXCLUDED.login,
			    display_name = EXCLUDED.display_name,
			    avatar_url = EXCLUDED.avatar_url,
			    email = EXCLUDED.email,
			    updated_at = now()
		RETURNING ` + userCols
	return scanUser(s.Pool.QueryRow(ctx, q, twitchID, login, displayName, avatar, email, string(defaultRole)))
}

func (s *Store) GetUser(ctx context.Context, id string) (models.User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

// GetUserByLogin — пользователь по логину (без учёта регистра). ErrNotFound если нет.
func (s *Store) GetUserByLogin(ctx context.Context, login string) (models.User, error) {
	u, err := scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(login) = lower($1)`, login))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// PlayerHistory — участия игрока (1×1 по user_id, 2×2 по составу members) с флагом победы (B6).
func (s *Store) PlayerHistory(ctx context.Context, userID string) ([]models.PlayerHistoryItem, error) {
	const q = `
		SELECT t.id, t.title, t.mode, t.status, t.starts_at, p.name, p.total_points,
		       (t.winner_participant_id IS NOT NULL AND t.winner_participant_id = p.id) AS win
		FROM participants p
		JOIN tournaments t ON t.id = p.tournament_id
		WHERE p.user_id = $1
		   OR (jsonb_typeof(p.members) = 'array'
		       AND p.members @> jsonb_build_array(jsonb_build_object('userId', $1::text)))
		ORDER BY COALESCE(t.starts_at, t.created_at) DESC`
	rows, err := s.Pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.PlayerHistoryItem{}
	for rows.Next() {
		var h models.PlayerHistoryItem
		if err := rows.Scan(&h.TournamentID, &h.Title, &h.Mode, &h.Status, &h.Date, &h.Name, &h.Points, &h.Win); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ListUsers — все пользователи (для выпадашек выбора участников в кабинете).
func (s *Store) ListUsers(ctx context.Context) ([]models.User, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY display_name, login`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) SetRole(ctx context.Context, userID string, role models.Role) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET role = $2, updated_at = now() WHERE id = $1`, userID, string(role))
	return err
}

func (s *Store) UpdateEmbarkID(ctx context.Context, userID, embark string) (models.User, error) {
	return scanUser(s.Pool.QueryRow(ctx,
		`UPDATE users SET embark_id = $2, updated_at = now() WHERE id = $1 RETURNING `+userCols,
		userID, embark))
}
