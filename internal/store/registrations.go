package store

import (
	"context"

	"github.com/battle-for-respect/backend/internal/models"
)

// CreateRegistration создаёт/обновляет заявку игрока в общий пул (одна на пользователя).
// Повторная подача обновляет embark/note и возвращает заявку в пул (status=pending).
func (s *Store) CreateRegistration(ctx context.Context, userID, embark, note string) (models.Registration, error) {
	const q = `
		INSERT INTO registrations (user_id, embark_id, note)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE
			SET embark_id = EXCLUDED.embark_id, note = EXCLUDED.note,
			    status = 'pending', tournament_id = NULL, decided_at = NULL,
			    created_at = now()
		RETURNING id, tournament_id, user_id, embark_id, status, note, created_at, decided_at`
	var r models.Registration
	err := s.Pool.QueryRow(ctx, q, userID, embark, note).Scan(
		&r.ID, &r.TournamentID, &r.UserID, &r.EmbarkID, &r.Status, &r.Note, &r.CreatedAt, &r.DecidedAt)
	return r, err
}

func (s *Store) GetRegistration(ctx context.Context, id string) (models.Registration, error) {
	const q = `
		SELECT r.id, r.tournament_id, r.user_id, r.embark_id, r.status, r.note, r.created_at, r.decided_at,
		       u.login, u.display_name, u.avatar_url
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		WHERE r.id = $1`
	var r models.Registration
	err := s.Pool.QueryRow(ctx, q, id).Scan(
		&r.ID, &r.TournamentID, &r.UserID, &r.EmbarkID, &r.Status, &r.Note, &r.CreatedAt, &r.DecidedAt,
		&r.UserLogin, &r.UserDisplayName, &r.UserAvatarURL)
	return r, err
}

func (s *Store) ListMyRegistrations(ctx context.Context, userID string) ([]models.Registration, error) {
	const q = `
		SELECT r.id, r.tournament_id, r.user_id, r.embark_id, r.status, r.note, r.created_at, r.decided_at,
		       t.title
		FROM registrations r
		LEFT JOIN tournaments t ON t.id = r.tournament_id
		WHERE r.user_id = $1
		ORDER BY r.created_at DESC`
	rows, err := s.Pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.Registration{}
	for rows.Next() {
		var r models.Registration
		var title *string
		if err := rows.Scan(&r.ID, &r.TournamentID, &r.UserID, &r.EmbarkID, &r.Status, &r.Note,
			&r.CreatedAt, &r.DecidedAt, &title); err != nil {
			return nil, err
		}
		if title != nil {
			r.TournamentTitle = *title
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListPool — открытые заявки (ещё не распределённые), кто подал раньше — выше.
func (s *Store) ListPool(ctx context.Context) ([]models.Registration, error) {
	const q = `
		SELECT r.id, r.tournament_id, r.user_id, r.embark_id, r.status, r.note, r.created_at, r.decided_at,
		       u.login, u.display_name, u.avatar_url
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		WHERE r.status = 'pending'
		ORDER BY r.created_at ASC`
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.Registration{}
	for rows.Next() {
		var r models.Registration
		if err := rows.Scan(&r.ID, &r.TournamentID, &r.UserID, &r.EmbarkID, &r.Status, &r.Note,
			&r.CreatedAt, &r.DecidedAt, &r.UserLogin, &r.UserDisplayName, &r.UserAvatarURL); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListPoolPage — страница открытых заявок (пагинация для бесконечной подгрузки).
// total — сколько всего pending-заявок (для индикатора «загружено N из total»).
func (s *Store) ListPoolPage(ctx context.Context, limit, offset int) ([]models.Registration, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	const q = `
		SELECT r.id, r.tournament_id, r.user_id, r.embark_id, r.status, r.note, r.created_at, r.decided_at,
		       u.login, u.display_name, u.avatar_url,
		       COUNT(*) OVER() AS total
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		WHERE r.status = 'pending'
		ORDER BY r.created_at ASC
		LIMIT $1 OFFSET $2`
	rows, err := s.Pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []models.Registration{}
	total := 0
	for rows.Next() {
		var r models.Registration
		if err := rows.Scan(&r.ID, &r.TournamentID, &r.UserID, &r.EmbarkID, &r.Status, &r.Note,
			&r.CreatedAt, &r.DecidedAt, &r.UserLogin, &r.UserDisplayName, &r.UserAvatarURL, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// SyncPendingRegistrationEmbark — обновляет Embark ID в незакрытой (pending) заявке игрока,
// чтобы пул показывал актуальный ID после смены его в профиле. У принятых/отклонённых
// заявок остаётся снимок на момент решения.
func (s *Store) SyncPendingRegistrationEmbark(ctx context.Context, userID, embark string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE registrations SET embark_id = $2 WHERE user_id = $1 AND status = 'pending'`,
		userID, embark)
	return err
}

// MarkRegistrationPlaced — игрок поставлен в турнир: заявка принята и уходит из пула.
// No-op, если у пользователя нет открытой заявки.
func (s *Store) MarkRegistrationPlaced(ctx context.Context, userID, tournamentID string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE registrations SET status = 'accepted', tournament_id = $2, decided_at = now()
		   WHERE user_id = $1 AND status = 'pending'`, userID, tournamentID)
	return err
}

// RevertRegistrationToPool — вернуть заявку в пул, если игрок был поставлен в этот
// турнир из заявки (его убрали из участников). No-op, если заявки/привязки нет.
func (s *Store) RevertRegistrationToPool(ctx context.Context, userID, tournamentID string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE registrations SET status = 'pending', tournament_id = NULL, decided_at = NULL
		   WHERE user_id = $1 AND tournament_id = $2 AND status = 'accepted'`, userID, tournamentID)
	return err
}

// DecideRegistration — отклонить/принять заявку вручную (используется для отклонения из пула).
func (s *Store) DecideRegistration(ctx context.Context, id, status string) (models.Registration, error) {
	_, err := s.Pool.Exec(ctx,
		`UPDATE registrations SET status = $2, decided_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return models.Registration{}, err
	}
	return s.GetRegistration(ctx, id)
}
