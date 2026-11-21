package store

import (
	"context"
	"os"
	"testing"

	"github.com/battle-for-respect/backend/internal/db"
)

// История для модели шанса читается из базы вместе с очередью заданий (запрос с UNION не ломается).
func TestLoadChanceHistoryIntegration(t *testing.T) {
	url := os.Getenv("RESPECT_TEST_DB")
	if url == "" {
		t.Skip("RESPECT_TEST_DB не задан")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer pool.Close()
	if _, err := New(pool).loadChanceHistory(ctx); err != nil {
		t.Fatalf("loadChanceHistory: %v", err)
	}
}
