package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Проверка активации заглушки по ссылке против живой Postgres.
func TestClaimIntegration(t *testing.T) {
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

	login := fmt.Sprintf("claim_%d", time.Now().UnixNano())
	u, err := st.CreateUser(ctx, login, login, "!imported", models.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Токен выдаётся и стабилен (повторный вызов возвращает тот же, без regenerate).
	tok, ok, err := st.EnsureClaimToken(ctx, u.ID, "tok-"+login, false)
	if err != nil || !ok || tok == "" {
		t.Fatalf("EnsureClaimToken: ok=%v tok=%q err=%v", ok, tok, err)
	}
	tok2, _, _ := st.EnsureClaimToken(ctx, u.ID, "other", false)
	if tok2 != tok {
		t.Errorf("токен нестабилен: %q != %q", tok2, tok)
	}

	// Lookup находит пользователя.
	lu, ok, err := st.LookupClaimToken(ctx, tok)
	if err != nil || !ok || lu.ID != u.ID {
		t.Fatalf("LookupClaimToken: ok=%v err=%v", ok, err)
	}

	// Активация: ставим настоящий пароль.
	cu, ok, err := st.ClaimAccount(ctx, tok, "$2y$12$fakehashfakehashfakehashfakehashfakehashfa")
	if err != nil || !ok || cu.ID != u.ID {
		t.Fatalf("ClaimAccount: ok=%v err=%v", ok, err)
	}

	// Токен сгорел (одноразовость), аккаунт больше не заглушка.
	if _, ok, _ := st.LookupClaimToken(ctx, tok); ok {
		t.Errorf("токен всё ещё валиден после активации")
	}
	if _, ok, _ := st.EnsureClaimToken(ctx, u.ID, "x", false); ok {
		t.Errorf("для активированного аккаунта не должно выдаваться ссылки")
	}
}
