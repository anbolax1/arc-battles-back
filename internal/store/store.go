package store

import "github.com/jackc/pgx/v5/pgxpool"

// Store — слой доступа к данным поверх пула pgx.
type Store struct {
	Pool   *pgxpool.Pool
	recaps *recapCache
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool, recaps: &recapCache{items: map[string]recapEntry{}}}
}
