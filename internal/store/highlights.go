package store

import (
	"context"
	"errors"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const hCols = `h.id, h.user_id, u.login, u.display_name, u.avatar_url,
	h.tournament_id, COALESCE(t.title, ''), h.title, h.source, h.source_url,
	h.file_path, h.thumb_path, h.preview_path, h.duration, h.status, h.reject_reason, h.created_at`

const hFrom = `FROM highlights h
	JOIN users u ON u.id = h.user_id
	LEFT JOIN tournaments t ON t.id = h.tournament_id`

// scanHighlight читает строку (+ опциональный total для пагинации) и формирует URL медиа.
func scanHighlight(row pgx.Row, total *int) (models.Highlight, error) {
	var h models.Highlight
	var tournamentID *string
	var filePath, thumbPath, previewPath string
	dest := []any{&h.ID, &h.UserID, &h.UserLogin, &h.UserName, &h.UserAvatarURL,
		&tournamentID, &h.TournamentTitle, &h.Title, &h.Source, &h.SourceURL,
		&filePath, &thumbPath, &previewPath, &h.Duration, &h.Status, &h.RejectReason, &h.CreatedAt}
	if total != nil {
		dest = append(dest, total)
	}
	if err := row.Scan(dest...); err != nil {
		return h, err
	}
	h.TournamentID = tournamentID
	if filePath != "" {
		h.VideoURL = "/media/" + filePath
	}
	if thumbPath != "" {
		h.ThumbURL = "/media/" + thumbPath
	}
	if previewPath != "" {
		h.PreviewURL = "/media/" + previewPath
	}
	return h, nil
}

// CreateHighlight создаёт запись хайлайта. Для твич-клипа обычно status='processing'
// (файл докачивается в фоне), для загруженного файла — сразу 'pending' с путями.
func (s *Store) CreateHighlight(ctx context.Context, userID string, tournamentID *string,
	title, source, sourceURL, filePath, thumbPath string, duration int, status string) (models.Highlight, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO highlights (user_id, tournament_id, title, source, source_url, file_path, thumb_path, duration, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`,
		userID, tournamentID, title, source, sourceURL, filePath, thumbPath, duration, status).Scan(&id)
	if err != nil {
		return models.Highlight{}, err
	}
	return s.GetHighlight(ctx, id)
}

func (s *Store) GetHighlight(ctx context.Context, id string) (models.Highlight, error) {
	h, err := scanHighlight(s.Pool.QueryRow(ctx, `SELECT `+hCols+` `+hFrom+` WHERE h.id = $1`, id), nil)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, ErrNotFound
	}
	return h, err
}

// SetHighlightProcessed — клип докачан: проставляем пути/длительность и статус 'pending'.
func (s *Store) SetHighlightProcessed(ctx context.Context, id, filePath, thumbPath, previewPath string, duration int) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE highlights SET file_path = $2, thumb_path = $3, preview_path = $4, duration = $5, status = 'pending' WHERE id = $1`,
		id, filePath, thumbPath, previewPath, duration)
	return err
}

// SetHighlightFailed — обработка не удалась.
func (s *Store) SetHighlightFailed(ctx context.Context, id, reason string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE highlights SET status = 'failed', reject_reason = $2 WHERE id = $1`, id, reason)
	return err
}

// ListApprovedHighlights — публичный список одобренных, с фильтрами и пагинацией.
// random=true — случайная выборка (для блока «лучшие моменты» на главной).
func (s *Store) ListApprovedHighlights(ctx context.Context, tournamentID, userID string, limit, offset int, random bool) ([]models.Highlight, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 24
	}
	if offset < 0 {
		offset = 0
	}
	orderBy := "h.created_at DESC"
	if random {
		orderBy = "random()"
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+hCols+`, COUNT(*) OVER() AS total `+hFrom+`
		WHERE h.status = 'approved'
		  AND ($1 = '' OR h.tournament_id = $1)
		  AND ($2 = '' OR h.user_id = $2)
		ORDER BY `+orderBy+`
		LIMIT $3 OFFSET $4`, tournamentID, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []models.Highlight{}
	total := 0
	for rows.Next() {
		h, err := scanHighlight(rows, &total)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, h)
	}
	return out, total, rows.Err()
}

// ListHighlightsByStatus — список для модерации (по статусу; пусто = все), пагинация.
func (s *Store) ListHighlightsByStatus(ctx context.Context, status string, limit, offset int) ([]models.Highlight, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 24
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+hCols+`, COUNT(*) OVER() AS total `+hFrom+`
		WHERE ($1 = '' OR h.status = $1)
		ORDER BY h.created_at ASC
		LIMIT $2 OFFSET $3`, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []models.Highlight{}
	total := 0
	for rows.Next() {
		h, err := scanHighlight(rows, &total)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, h)
	}
	return out, total, rows.Err()
}

// ModerateHighlight — одобрить/отклонить хайлайт организатором.
func (s *Store) ModerateHighlight(ctx context.Context, id, reviewerID string, approve bool, reason string) error {
	status := "rejected"
	if approve {
		status = "approved"
		reason = ""
	}
	res, err := s.Pool.Exec(ctx,
		`UPDATE highlights SET status = $2, reject_reason = $3, reviewed_by = $4, reviewed_at = $5 WHERE id = $1`,
		id, status, reason, reviewerID, time.Now())
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteHighlight удаляет запись и возвращает пути файлов для зачистки в хранилище.
func (s *Store) DeleteHighlight(ctx context.Context, id string) (filePath, thumbPath, previewPath string, err error) {
	err = s.Pool.QueryRow(ctx,
		`DELETE FROM highlights WHERE id = $1 RETURNING file_path, thumb_path, preview_path`, id).
		Scan(&filePath, &thumbPath, &previewPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	return filePath, thumbPath, previewPath, err
}
