package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const userCols = `id, login, display_name, avatar_url, email, role, embark_id, created_at`

// ErrLoginTaken — логин уже занят (нарушение уникального индекса users_login_lower_key).
var ErrLoginTaken = errors.New("логин уже занят")

func scanUser(row pgx.Row) (models.User, error) {
	var u models.User
	var role string
	err := row.Scan(&u.ID, &u.Login, &u.DisplayName, &u.AvatarURL, &u.Email, &role, &u.EmbarkID, &u.CreatedAt)
	u.Role = models.Role(role)
	return u, err
}

// CreateUser создаёт пользователя с хешем пароля. Логин уникален без учёта регистра —
// при конфликте возвращается ErrLoginTaken.
func (s *Store) CreateUser(ctx context.Context, login, displayName, passwordHash string, role models.Role) (models.User, error) {
	const q = `
		INSERT INTO users (login, display_name, password_hash, role)
		VALUES ($1, $2, $3, $4)
		RETURNING ` + userCols
	u, err := scanUser(s.Pool.QueryRow(ctx, q, login, displayName, passwordHash, string(role)))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return models.User{}, ErrLoginTaken
		}
		return models.User{}, err
	}
	return u, nil
}

// GetUserAuthByLogin возвращает идентификатор, роль и хеш пароля по логину (без учёта
// регистра) — для проверки пароля при входе. ErrNotFound, если пользователя нет.
func (s *Store) GetUserAuthByLogin(ctx context.Context, login string) (id string, role models.Role, passwordHash string, err error) {
	var r string
	err = s.Pool.QueryRow(ctx,
		`SELECT id, role, password_hash FROM users WHERE lower(login) = lower($1)`, login).
		Scan(&id, &r, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	if err != nil {
		return "", "", "", err
	}
	return id, models.Role(r), passwordHash, nil
}

// EnsureSuperadmin гарантирует аккаунт-организатора: создаёт пользователя login с переданным
// хешем пароля и ролью superadmin, либо (если логин уже существует) выставляет ему роль
// superadmin И этот пароль. Пароль организатора, таким образом, управляется через .env
// (SUPERADMIN_PASSWORD): задаётся/меняется на старте. Чтобы перестать управлять паролем из env
// (например после добавления самостоятельной смены пароля) — оставьте SUPERADMIN_PASSWORD пустым,
// тогда бутстрап пропускается (см. cmd/server/main.go). Вызывается ДО приёма запросов, чтобы
// организаторский логин нельзя было «застолбить» через открытую регистрацию.
func (s *Store) EnsureSuperadmin(ctx context.Context, login, passwordHash string) error {
	const q = `
		INSERT INTO users (login, display_name, password_hash, role)
		VALUES ($1, $1, $2, $3)
		ON CONFLICT (lower(login)) DO UPDATE SET role = $3, password_hash = $2, updated_at = now()`
	_, err := s.Pool.Exec(ctx, q, login, passwordHash, string(models.RoleSuperadmin))
	return err
}

// GetUserSession возвращает пользователя и его tokens_valid_after одним запросом — для
// проверки сессии в middleware (свежая роль из БД + серверная ревокация по эпохе токенов).
func (s *Store) GetUserSession(ctx context.Context, id string) (models.User, time.Time, error) {
	var u models.User
	var role string
	var validAfter time.Time
	err := s.Pool.QueryRow(ctx, `SELECT `+userCols+`, tokens_valid_after FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Login, &u.DisplayName, &u.AvatarURL, &u.Email, &role, &u.EmbarkID, &u.CreatedAt, &validAfter)
	u.Role = models.Role(role)
	return u, validAfter, err
}

// RevokeSessions двигает эпоху токенов пользователя на текущий момент — все ранее
// выпущенные токены становятся недействительными (logout, при желании — смена пароля/роли).
func (s *Store) RevokeSessions(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET tokens_valid_after = now(), updated_at = now() WHERE id = $1`, id)
	return err
}

// CountSuperadmins — число аккаунтов с ролью superadmin (для инварианта «нельзя снять последнего»).
func (s *Store) CountSuperadmins(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = $1`, string(models.RoleSuperadmin)).Scan(&n)
	return n, err
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

// ListUsersOverview — страница пользователей с агрегатами участия (раздел «Пользователи»).
// Пагинация (limit/offset), поиск (q по логину/имени/Embark ID/email) и сортировка (sort)
// делаются на бэке. total — всего подходящих под поиск. Участие считаем как в PlayerHistory.
func (s *Store) ListUsersOverview(ctx context.Context, limit, offset int, q, sort string) ([]models.UserOverview, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 15
	}
	if offset < 0 {
		offset = 0
	}
	// ORDER BY — из белого списка (не из сырого ввода), чтобы исключить инъекцию.
	orderBy := "COALESCE(s.points, 0) DESC, u.display_name, u.login"
	switch sort {
	case "tournaments":
		orderBy = "COALESCE(s.tournaments, 0) DESC, u.display_name, u.login"
	case "wins":
		orderBy = "COALESCE(s.wins, 0) DESC, u.display_name, u.login"
	case "joined":
		orderBy = "u.created_at DESC"
	case "name":
		orderBy = "u.display_name, u.login"
	}
	query := `
		SELECT u.id, u.login, u.display_name, u.avatar_url, u.email, u.role, u.embark_id, u.created_at,
		       COALESCE(s.tournaments, 0), COALESCE(s.wins, 0), COALESCE(s.points, 0), COALESCE(s.participations, 0),
		       COUNT(*) OVER() AS total
		FROM users u
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*) FILTER (WHERE t.status = 'finished')                                   AS tournaments,
				COUNT(*) FILTER (WHERE t.status = 'finished' AND t.winner_participant_id = p.id) AS wins,
				COALESCE(SUM(p.total_points) FILTER (WHERE t.status = 'finished'), 0)            AS points,
				COUNT(*)                                                                        AS participations
			FROM participants p
			JOIN tournaments t ON t.id = p.tournament_id
			WHERE p.user_id = u.id
			   OR (jsonb_typeof(p.members) = 'array'
			       AND p.members @> jsonb_build_array(jsonb_build_object('userId', u.id::text)))
		) s ON true
		WHERE ($3 = '' OR u.login ILIKE $4 OR u.display_name ILIKE $4 OR u.embark_id ILIKE $4 OR u.email ILIKE $4)
		ORDER BY ` + orderBy + `
		LIMIT $1 OFFSET $2`
	qt := strings.TrimSpace(q)
	// Экранируем спецсимволы LIKE (\, %, _) — это поиск-подстрока, а не паттерн (escape по умолчанию '\').
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(qt) + "%"
	rows, err := s.Pool.Query(ctx, query, limit, offset, qt, like)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []models.UserOverview{}
	total := 0
	for rows.Next() {
		var o models.UserOverview
		var role string
		if err := rows.Scan(&o.User.ID, &o.User.Login, &o.User.DisplayName, &o.User.AvatarURL,
			&o.User.Email, &role, &o.User.EmbarkID, &o.User.CreatedAt,
			&o.Tournaments, &o.Wins, &o.Points, &o.Participations, &total); err != nil {
			return nil, 0, err
		}
		o.User.Role = models.Role(role)
		o.Email = o.User.Email
		out = append(out, o)
	}
	return out, total, rows.Err()
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
