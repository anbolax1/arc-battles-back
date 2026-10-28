package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Дизайны публичной части сайта: прежний и новый в стиле ARC Raiders.
const (
	DesignClassic = "classic"
	DesignSurface = "surface"
)

// ValidDesign - известен ли сайту такой дизайн.
func ValidDesign(d string) bool { return d == DesignClassic || d == DesignSurface }

// SiteDesign - дизайн, который сейчас видят посетители; без записи в базе - прежний.
func (s *Store) SiteDesign(ctx context.Context) (string, error) {
	var d string
	err := s.Pool.QueryRow(ctx, `SELECT value FROM site_settings WHERE key = 'design'`).Scan(&d)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !ValidDesign(d)) {
		return DesignClassic, nil
	}
	return d, err
}

// SetSiteDesign включает дизайн для всех посетителей сразу.
func (s *Store) SetSiteDesign(ctx context.Context, d string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO site_settings (key, value, updated_at) VALUES ('design', $1, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, d)
	return err
}

// DefaultSubscriberName - как называется роль подписчика, пока организатор её не переименовал.
const DefaultSubscriberName = "Подписчик"

// SubscriberRoleName - название роли подписчика на сайте и в кабинете.
func (s *Store) SubscriberRoleName(ctx context.Context) (string, error) {
	var name string
	err := s.Pool.QueryRow(ctx, `SELECT value FROM site_settings WHERE key = 'role_name_subscriber'`).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && name == "") {
		return DefaultSubscriberName, nil
	}
	return name, err
}

// SetSubscriberRoleName переименовывает роль подписчика сразу для всех.
func (s *Store) SetSubscriberRoleName(ctx context.Context, name string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO site_settings (key, value, updated_at) VALUES ('role_name_subscriber', $1, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, name)
	return err
}
