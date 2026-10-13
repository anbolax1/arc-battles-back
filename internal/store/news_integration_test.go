package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/store"
)

// Черновик и запланированная новость не видны на сайте, пока не наступит дата публикации.
func TestNewsIntegration(t *testing.T) {
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
	st := store.New(pool)

	visible := func(id string) bool {
		items, _, err := st.ListNews(ctx, 50, 0)
		if err != nil {
			t.Fatalf("ListNews: %v", err)
		}
		for _, n := range items {
			if n.ID == id {
				return true
			}
		}
		return false
	}

	draft, err := st.CreateNews(ctx, "Черновик", "текст", nil)
	if err != nil {
		t.Fatalf("CreateNews: %v", err)
	}
	defer st.DeleteNews(ctx, draft.ID)
	if visible(draft.ID) || draft.Published {
		t.Error("черновик виден в ленте")
	}
	if _, err := st.GetNews(ctx, draft.ID, false); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("черновик открывается по ссылке: %v", err)
	}
	if _, err := st.GetNews(ctx, draft.ID, true); err != nil {
		t.Errorf("кабинет не видит черновик: %v", err)
	}

	later := time.Now().Add(time.Hour)
	if _, err := st.UpdateNews(ctx, draft.ID, "Анонс", "текст", &later); err != nil {
		t.Fatalf("UpdateNews: %v", err)
	}
	if visible(draft.ID) {
		t.Error("запланированная новость вышла раньше срока")
	}

	now := time.Now().Add(-time.Minute)
	pub, err := st.UpdateNews(ctx, draft.ID, "Анонс", "текст", &now)
	if err != nil {
		t.Fatalf("UpdateNews: %v", err)
	}
	if !visible(draft.ID) || !pub.Published {
		t.Error("опубликованной новости нет в ленте")
	}

	if _, err := st.DeleteNews(ctx, draft.ID); err != nil {
		t.Fatalf("DeleteNews: %v", err)
	}
	if _, err := st.DeleteNews(ctx, draft.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("повторное удаление: %v", err)
	}
}
