package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// О событии пояса пишется одна новость: повторная проверка и удалённая организатором новость второй не дают.
func TestBeltNewsIntegration(t *testing.T) {
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

	stamp := time.Now().Format("150405.000000")
	key, old := "take:it-"+stamp, "drop:it-"+stamp
	defer pool.Exec(ctx, `DELETE FROM belt_news WHERE key = ANY($1)`, []string{key, old})
	known := func(k string) bool {
		m, err := st.KnownBeltEvents(ctx)
		if err != nil {
			t.Fatalf("KnownBeltEvents: %v", err)
		}
		return m[k]
	}

	title := "Тест " + stamp + " забрал пояс лиги"
	added, err := st.AddBeltNews(ctx, key, title, "<p>текст</p>", models.NewsBelt{Name: "Тест", Stars: 2})
	if err != nil || !added {
		t.Fatalf("AddBeltNews: %v, added %v", err, added)
	}
	if again, err := st.AddBeltNews(ctx, key, title, "<p>текст</p>", models.NewsBelt{Name: "Тест"}); err != nil || again {
		t.Fatalf("вторая новость о том же событии: %v, added %v", err, again)
	}
	items, err := st.ListAllNews(ctx)
	if err != nil {
		t.Fatalf("ListAllNews: %v", err)
	}
	var news *models.News
	for i := range items {
		if items[i].Title == title {
			if news != nil {
				t.Fatal("новость о событии записана дважды")
			}
			news = &items[i]
		}
	}
	if news == nil || !news.Published || news.Belt == nil || *news.Belt != (models.NewsBelt{Name: "Тест", Stars: 2}) {
		t.Fatalf("новость о поясе: %+v", news)
	}

	if _, err := st.DeleteNews(ctx, news.ID); err != nil {
		t.Fatalf("DeleteNews: %v", err)
	}
	if !known(key) {
		t.Error("удалённая новость сделала событие неизвестным")
	}
	if again, err := st.AddBeltNews(ctx, key, title, "<p>текст</p>", models.NewsBelt{Name: "Тест"}); err != nil || again {
		t.Errorf("после удаления новость появилась снова: %v, added %v", err, again)
	}

	if err := st.SkipBeltEvents(ctx, []string{old}); err != nil {
		t.Fatalf("SkipBeltEvents: %v", err)
	}
	if !known(old) {
		t.Error("давнее событие не запомнено")
	}
}
