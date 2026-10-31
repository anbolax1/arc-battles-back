package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Ник при регистрации - и логин, и имя на сайте; войти можно по нему, а старым аккаунтам - ещё и по имени.
func TestRegisteredNickIsLogin(t *testing.T) {
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
		t.Fatal(err)
	}
	defer pool.Close()
	st := store.New(pool)

	uniq := time.Now().UnixNano() % 1_000_000_000
	nick := fmt.Sprintf("Ник%d", uniq)
	embark := fmt.Sprintf("Ник#%04d", uniq%10000)

	u, err := st.CreateRegisteredUser(ctx, nick, embark, "hash")
	if err != nil {
		t.Fatalf("CreateRegisteredUser: %v", err)
	}
	if u.Login != nick || u.DisplayName != nick || u.EmbarkID != embark || u.Role != models.RoleUser {
		t.Errorf("аккаунт: %+v", u)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, u.ID) })

	// Войти можно по нику, регистр не важен.
	for _, in := range []string{nick, "ник" + nick[len("Ник"):]} {
		id, _, hash, err := st.GetUserAuthByLogin(ctx, in)
		if err != nil || id != u.ID || hash != "hash" {
			t.Errorf("вход по %q: %q %v", in, id, err)
		}
	}

	// Тот же ник в другом регистре, как и тот же Embark ID, занят.
	if _, err := st.CreateRegisteredUser(ctx, "ник"+nick[len("Ник"):], "Другой#9999", "h"); !errors.Is(err, store.ErrNickTaken) {
		t.Errorf("повтор ника: %v, want ErrNickTaken", err)
	}
	if _, err := st.CreateRegisteredUser(ctx, fmt.Sprintf("Иной%d", uniq), embark, "h"); !errors.Is(err, store.ErrEmbarkTaken) {
		t.Errorf("повтор Embark ID: %v, want ErrEmbarkTaken", err)
	}

	// Старый аккаунт: логин не совпадает с именем - вход и по логину, и по имени.
	legacyLogin := fmt.Sprintf("legacy_%d", uniq)
	legacyName := fmt.Sprintf("Старый%d", uniq)
	old, err := st.CreateUser(ctx, legacyLogin, legacyName, "hash2", models.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, old.ID) })
	for _, in := range []string{legacyLogin, legacyName} {
		if id, _, _, err := st.GetUserAuthByLogin(ctx, in); err != nil || id != old.ID {
			t.Errorf("вход старого аккаунта по %q: %q %v", in, id, err)
		}
	}

	// Одно и то же имя у двоих: по имени не войти, по логину можно.
	twin, err := st.CreateUser(ctx, fmt.Sprintf("twin_%d", uniq), legacyName, "hash3", models.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, twin.ID) })
	if _, _, _, err := st.GetUserAuthByLogin(ctx, legacyName); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("общее имя не должно открывать вход: %v", err)
	}
	if id, _, _, err := st.GetUserAuthByLogin(ctx, legacyLogin); err != nil || id != old.ID {
		t.Errorf("вход по логину при общем имени: %q %v", id, err)
	}
	// Логин важнее чужого имени: имя не перехватывает чужой логин.
	if _, _, _, err := st.GetUserAuthByLogin(ctx, "нет_такого_"+nick); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("несуществующий ник: %v", err)
	}
}
