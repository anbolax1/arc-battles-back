package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/battle-for-respect/backend/internal/models"
)

// Управление аккаунтами игроков через HTTP: модератор правит только игроков, организатор - всех, кроме защищённого.
func TestAccountManagementAPI(t *testing.T) {
	a := newQueueAPI(t)
	ctx := context.Background()

	// Отдельные от actor() пользователи: у тех есть подтверждённые скриншоты, а они мешают удалению.
	mk := func(tag string, role models.Role, hash string) actor {
		t.Helper()
		login := fmt.Sprintf("am%d%s", a.uniq%1e7, tag)
		u, err := a.st.CreateUser(ctx, login, login, hash, role)
		if err != nil {
			t.Fatal(err)
		}
		tok, err := auth.IssueToken(a.secret, u.ID, string(role), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return actor{user: u, token: tok}
	}
	boss, boss2 := mk("boss", models.RoleSuperadmin, "x"), mk("bosstwo", models.RoleSuperadmin, "x")
	mod, mod2 := mk("mod", models.RoleModerator, "x"), mk("modtwo", models.RoleModerator, "x")
	plain := mk("plain", models.RoleUser, "x")
	ghost := mk("ghost", models.RoleUser, "!imported")
	empty, empty2 := mk("empty", models.RoleUser, "x"), mk("empty2", models.RoleUser, "x")
	played := mk("played", models.RoleUser, "x")
	var nobody actor

	// Ник, который уже занят: подходит под правила, поэтому упирается именно в занятость.
	takenNick := fmt.Sprintf("Zan%d", a.uniq%1e6)
	taken, err := a.st.CreateUser(ctx, takenNick, takenNick, "x", models.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.st.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, taken.ID) })

	pid := played.user.ID
	solo, err := a.st.CreateTournament(ctx, models.Tournament{Title: "itest аккаунты", Mode: "1x1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.st.DeleteTournament(ctx, solo.ID) })
	if _, err := a.st.AddParticipant(ctx, models.Participant{TournamentID: solo.ID, Kind: "player", UserID: &pid, Name: played.user.DisplayName, Seed: 1}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, x := range []actor{boss, boss2, mod, mod2, plain, ghost, empty, empty2, played} {
			_, _ = a.st.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, x.user.ID)
		}
	})

	path := func(x actor, tail string) string { return "/users/" + x.user.ID + tail }

	// Без входа и без роли модератора - не пускают.
	a.want(nobody, "POST", path(plain, "/reset-password"), nil, 401)
	a.want(plain, "POST", path(empty, "/reset-password"), nil, 403)
	a.want(plain, "PATCH", path(empty, "/nick"), map[string]any{"nick": "Захват"}, 403)
	a.want(plain, "DELETE", path(empty, ""), nil, 403)

	// Модератор: игроку - сброс пароля и ссылка.
	tok := a.want(mod, "POST", path(plain, "/reset-password"), nil, 200)["token"]
	if s, _ := tok.(string); len(s) < 20 {
		t.Errorf("токен сброса: %v", tok)
	}
	// После сброса пароля нет, и ссылку можно получить заново (новую - с regenerate).
	t1 := a.want(mod, "POST", path(plain, "/claim-link"), nil, 200)["token"]
	if t1 != tok {
		t.Errorf("ссылка нестабильна: %v != %v", t1, tok)
	}
	t2 := a.want(mod, "POST", path(plain, "/claim-link?regenerate=1"), nil, 200)["token"]
	if t2 == tok {
		t.Error("regenerate вернул прежнюю ссылку")
	}
	a.want(mod, "POST", path(ghost, "/claim-link"), nil, 200)
	a.want(mod, "POST", path(empty, "/claim-link"), nil, 400) // у него есть рабочий пароль: ссылка не нужна
	a.want(mod, "POST", path(boss, "/reset-password"), nil, 403)

	// Модератор не трогает ни модераторов, ни организаторов - иначе забрал бы их аккаунт себе.
	for _, victim := range []actor{mod2, boss, mod} {
		a.want(mod, "POST", path(victim, "/reset-password"), nil, 403)
		a.want(mod, "POST", path(victim, "/claim-link"), nil, 403)
		a.want(mod, "PATCH", path(victim, "/nick"), map[string]any{"nick": "Чужак"}, 403)
		a.want(mod, "DELETE", path(victim, ""), nil, 403)
	}
	var hash string
	_ = a.st.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, boss.user.ID).Scan(&hash)
	if strings.HasPrefix(hash, "!") {
		t.Error("пароль организатора сброшен модератором")
	}

	// Организатор: любому, кроме себя; ник и профиль организатора защищены.
	a.want(boss, "POST", path(mod2, "/reset-password"), nil, 200)
	a.want(boss, "POST", path(boss2, "/reset-password"), nil, 200)
	a.want(boss, "POST", path(boss, "/reset-password"), nil, 409)
	a.want(boss, "PATCH", path(boss2, "/nick"), map[string]any{"nick": "Босс"}, 409)
	a.want(boss, "DELETE", path(boss2, ""), nil, 409)
	a.want(boss, "DELETE", path(boss, ""), nil, 409)
	a.want(boss, "POST", "/users/net-takogo/reset-password", nil, 404)

	// Смена ника: правила регистрации, занятость, сам результат.
	a.want(mod, "PATCH", path(empty, "/nick"), map[string]any{"nick": "ab"}, 400)
	a.want(mod, "PATCH", path(empty, "/nick"), map[string]any{"nick": "с пробелом"}, 400)
	a.want(mod, "PATCH", path(empty, "/nick"), map[string]any{"nick": "1цифра"}, 400)
	a.want(mod, "PATCH", path(empty, "/nick"), "{не json", 400)
	a.want(mod, "PATCH", path(empty, "/nick"), map[string]any{"nick": strings.ToUpper(takenNick)}, 409)
	nick := fmt.Sprintf("Нов%d", a.uniq%1e7)
	got := a.want(mod, "PATCH", path(empty, "/nick"), map[string]any{"nick": "  " + nick + "  "}, 200)
	if got["login"] != nick || got["displayName"] != nick {
		t.Errorf("после смены ника: %v", got)
	}
	// Игрок виден по новому нику и в поиске, и в публичном профиле; по старому - нет.
	if p := a.want(nobody, "GET", "/players/"+nick, nil, 200); p["user"].(map[string]any)["id"] != empty.user.ID {
		t.Errorf("профиль по новому нику: %v", p)
	}
	// По прежнему нику отдаётся тот же профиль (страница сайта уводит на новый адрес).
	if p := a.want(nobody, "GET", "/players/"+empty.user.Login, nil, 200); p["user"].(map[string]any)["login"] != nick {
		t.Errorf("профиль по прежнему нику: %v", p)
	}
	if found, err := a.st.SearchPlayers(ctx, nick, 5); err != nil || len(found) != 1 || found[0].UserID != empty.user.ID {
		t.Errorf("поиск по новому нику: %v %v", found, err)
	}
	old, _ := a.st.SearchPlayers(ctx, empty.user.Login, 5)
	for _, f := range old {
		if f.UserID == empty.user.ID {
			t.Errorf("поиск находит игрока по старому нику: %v", f)
		}
	}

	// Удаление: профиль с матчем остаётся, пустой уходит.
	msg := a.want(mod, "DELETE", path(played, ""), nil, 409)["error"]
	if s, _ := msg.(string); !strings.Contains(s, "есть матчи") {
		t.Errorf("причина отказа: %v", msg)
	}
	a.want(nobody, "GET", "/players/"+played.user.Login, nil, 200)
	a.want(mod, "DELETE", path(empty2, ""), nil, 200)
	a.want(mod, "DELETE", path(empty2, ""), nil, 404)
	a.want(nobody, "GET", "/players/"+empty2.user.Login, nil, 404)
}
