package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const newsCols = `id, title, body, image_path, published_at, COALESCE(published_at <= now(), false), created_at, updated_at, belt`

func scanNews(row pgx.Row) (models.News, error) {
	var n models.News
	var image string
	var belt []byte
	if err := row.Scan(&n.ID, &n.Title, &n.Body, &image, &n.PublishedAt, &n.Published, &n.CreatedAt, &n.UpdatedAt, &belt); err != nil {
		return n, err
	}
	if image != "" {
		n.ImageURL = "/media/" + image
	}
	if len(belt) > 0 {
		n.Belt = &models.NewsBelt{}
		if err := json.Unmarshal(belt, n.Belt); err != nil {
			return n, err
		}
	}
	return n, nil
}

func collectNews(rows pgx.Rows) ([]models.News, error) {
	defer rows.Close()
	out := []models.News{}
	for rows.Next() {
		n, err := scanNews(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListNews - вышедшие новости, свежие сверху, и сколько их всего.
func (s *Store) ListNews(ctx context.Context, limit, offset int) ([]models.News, int, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM news WHERE published_at <= now()`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+newsCols+` FROM news WHERE published_at <= now()
		ORDER BY published_at DESC, created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	out, err := collectNews(rows)
	return out, total, err
}

// ListAllNews - все новости для кабинета: черновики сверху, дальше по дате публикации.
func (s *Store) ListAllNews(ctx context.Context) ([]models.News, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+newsCols+` FROM news ORDER BY published_at DESC NULLS FIRST, created_at DESC`)
	if err != nil {
		return nil, err
	}
	return collectNews(rows)
}

// GetNews - новость по id. Черновик и запланированную видит только кабинет (withDrafts).
func (s *Store) GetNews(ctx context.Context, id string, withDrafts bool) (models.News, error) {
	n, err := scanNews(s.Pool.QueryRow(ctx, `
		SELECT `+newsCols+` FROM news WHERE id = $1 AND ($2 OR published_at <= now())`, id, withDrafts))
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

func (s *Store) CreateNews(ctx context.Context, title, body string, publishedAt *time.Time) (models.News, error) {
	return scanNews(s.Pool.QueryRow(ctx, `
		INSERT INTO news (title, body, published_at) VALUES ($1, $2, $3) RETURNING `+newsCols, title, body, publishedAt))
}

func (s *Store) UpdateNews(ctx context.Context, id, title, body string, publishedAt *time.Time) (models.News, error) {
	n, err := scanNews(s.Pool.QueryRow(ctx, `
		UPDATE news SET title = $2, body = $3, published_at = $4, updated_at = now()
		WHERE id = $1 RETURNING `+newsCols, id, title, body, publishedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// DeleteNews удаляет новость и отдаёт путь её картинки, чтобы убрать файл из хранилища.
func (s *Store) DeleteNews(ctx context.Context, id string) (string, error) {
	var image string
	err := s.Pool.QueryRow(ctx, `DELETE FROM news WHERE id = $1 RETURNING image_path`, id).Scan(&image)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return image, err
}

// NewsImagePath - путь картинки новости в хранилище; пусто - картинки нет.
func (s *Store) NewsImagePath(ctx context.Context, id string) string {
	var path string
	_ = s.Pool.QueryRow(ctx, `SELECT image_path FROM news WHERE id = $1`, id).Scan(&path)
	return path
}

func (s *Store) SetNewsImage(ctx context.Context, id, path string) (models.News, error) {
	n, err := scanNews(s.Pool.QueryRow(ctx, `
		UPDATE news SET image_path = $2, updated_at = now() WHERE id = $1 RETURNING `+newsCols, id, path))
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}
