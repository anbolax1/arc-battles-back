package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Принудительная смена ника и удаление пустого профиля против живой Postgres.
// Запуск - как у TestMMRIntegration (нужен RESPECT_TEST_DB).
func TestUserAdminIntegration(t *testing.T) {
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

	uniq := time.Now().UnixNano()
	mkUser := func(tag string) models.User {
		login := fmt.Sprintf("ua_%d_%s", uniq, tag)
		u, err := st.CreateUser(ctx, login, login, "x", models.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", tag, err)
		}
		return u
	}
	player, partner, other, empty := mkUser("p"), mkUser("q"), mkUser("o"), mkUser("e")

	// Матчи: 1×1 и 2×2, в обоих игрок записан по старому нику.
	solo, err := st.CreateTournament(ctx, models.Tournament{Title: "соло", Mode: "1x1"})
	if err != nil {
		t.Fatalf("CreateTournament: %v", err)
	}
	pid := player.ID
	soloPart, err := st.AddParticipant(ctx, models.Participant{TournamentID: solo.ID, Kind: "player", UserID: &pid, Name: player.DisplayName, Seed: 1})
	if err != nil {
		t.Fatalf("AddParticipant 1x1: %v", err)
	}
	duo, err := st.CreateTournament(ctx, models.Tournament{Title: "дуэт", Mode: "2x2"})
	if err != nil {
		t.Fatalf("CreateTournament 2x2: %v", err)
	}
	members, _ := json.Marshal([]map[string]string{
		{"name": player.DisplayName, "userId": player.ID},
		{"name": partner.DisplayName, "userId": partner.ID},
	})
	duoPart, err := st.AddParticipant(ctx, models.Participant{TournamentID: duo.ID, Kind: "team", Name: "Пара", Seed: 1, Members: members})
	if err != nil {
		t.Fatalf("AddParticipant 2x2: %v", err)
	}

	// Занятый ник (чужой логин или чужое имя) не отдаётся, сам игрок может менять только регистр своего.
	if _, err := st.RenameUser(ctx, player.ID, other.Login); !errors.Is(err, store.ErrNickTaken) {
		t.Errorf("ник другого игрока: %v, want ErrNickTaken", err)
	}
	if _, err := st.RenameUser(ctx, "нет-такого", fmt.Sprintf("Nw%d", uniq%1e9)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("несуществующий игрок: %v, want ErrNotFound", err)
	}

	nick := fmt.Sprintf("Nova%d", uniq%1e7)
	u, err := st.RenameUser(ctx, player.ID, nick)
	if err != nil {
		t.Fatalf("RenameUser: %v", err)
	}
	if u.Login != nick || u.DisplayName != nick {
		t.Errorf("после смены login=%q display=%q, want %q", u.Login, u.DisplayName, nick)
	}
	if got, err := st.GetUserByLogin(ctx, nick); err != nil || got.ID != player.ID {
		t.Errorf("поиск по новому нику: %+v %v", got, err)
	}
	if _, err := st.GetUserByLogin(ctx, player.Login); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("старый ник ещё находится: %v", err)
	}
	// Входить по прежнему нику можно (без учёта регистра), но занять его другой игрок не может.
	for _, old := range []string{player.Login, strings.ToUpper(player.Login)} {
		if id, _, _, err := st.GetUserAuthByLogin(ctx, old); err != nil || id != player.ID {
			t.Errorf("вход по прежнему нику %q: id=%q err=%v", old, id, err)
		}
	}
	if _, err := st.RenameUser(ctx, other.ID, player.Login); !errors.Is(err, store.ErrNickTaken) {
		t.Errorf("чужой прежний ник при смене: %v, want ErrNickTaken", err)
	}
	if _, err := st.CreateRegisteredUser(ctx, player.Login, fmt.Sprintf("Old#%04d", uniq%10000), "h"); !errors.Is(err, store.ErrNickTaken) {
		t.Errorf("чужой прежний ник при регистрации: %v, want ErrNickTaken", err)
	}
	if id, _, _, err := st.GetUserAuthByLogin(ctx, nick); err != nil || id != player.ID {
		t.Errorf("вход по новому нику: id=%q err=%v", id, err)
	}
	// Только регистр своего ника - не «занято».
	if _, err := st.RenameUser(ctx, player.ID, fmt.Sprintf("nOVA%d", uniq%1e7)); err != nil {
		t.Errorf("смена регистра своего ника: %v", err)
	}
	nick = fmt.Sprintf("nOVA%d", uniq%1e7)
	// Вторая смена: первый прежний ник не теряется, второй добавляется.
	if id, _, _, err := st.GetUserAuthByLogin(ctx, player.Login); err != nil || id != player.ID {
		t.Errorf("после второй смены вход по первому нику: id=%q err=%v", id, err)
	}
	if id, _, _, err := st.GetUserAuthByLogin(ctx, fmt.Sprintf("Nova%d", uniq%1e7)); err != nil || id != player.ID {
		t.Errorf("вход по промежуточному нику: id=%q err=%v", id, err)
	}

	// Копии имени в матчах переписаны, чужое имя в составе пары осталось.
	if p, err := st.GetParticipant(ctx, soloPart.ID); err != nil || p.Name != nick {
		t.Errorf("участник 1×1: %+v %v, want name=%q", p, err, nick)
	}
	p2, err := st.GetParticipant(ctx, duoPart.ID)
	if err != nil {
		t.Fatalf("GetParticipant 2x2: %v", err)
	}
	var got []map[string]string
	if err := json.Unmarshal(p2.Members, &got); err != nil || len(got) != 2 {
		t.Fatalf("состав пары: %s %v", p2.Members, err)
	}
	if got[0]["name"] != nick || got[0]["userId"] != player.ID {
		t.Errorf("игрок в составе пары: %v, want name=%q", got[0], nick)
	}
	if got[1]["name"] != partner.DisplayName {
		t.Errorf("партнёр в составе пары задет: %v", got[1])
	}

	// Профиль с матчами не удаляется (и участник остаётся привязанным), пустой - удаляется.
	var ne *store.NotEmptyError
	if err := st.DeleteEmptyUser(ctx, player.ID); !errors.As(err, &ne) {
		t.Errorf("игрок с матчами: %v, want NotEmptyError", err)
	}
	if err := st.DeleteEmptyUser(ctx, partner.ID); !errors.As(err, &ne) {
		t.Errorf("игрок в составе пары: %v, want NotEmptyError", err)
	}
	if p, err := st.GetParticipant(ctx, soloPart.ID); err != nil || p.UserID == nil {
		t.Errorf("участник отвязался после отказа: %+v %v", p, err)
	}
	if err := st.DeleteEmptyUser(ctx, empty.ID); err != nil {
		t.Errorf("пустой профиль: %v", err)
	}
	if _, err := st.GetUser(ctx, empty.ID); err == nil {
		t.Error("пустой профиль остался в базе")
	}
	if err := st.DeleteEmptyUser(ctx, empty.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("повторное удаление: %v, want ErrNotFound", err)
	}

	// Каскад: ссылка входа пустого профиля уходит вместе с ним.
	cascadeLogin := fmt.Sprintf("ua_%d_c", uniq)
	cascade, err := st.CreateUser(ctx, cascadeLogin, cascadeLogin, "!imported", models.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser(заглушка): %v", err)
	}
	if _, _, err := st.EnsureClaimToken(ctx, cascade.ID, fmt.Sprintf("tok-%d", uniq), true); err != nil {
		t.Fatalf("EnsureClaimToken: %v", err)
	}
	if err := st.DeleteEmptyUser(ctx, cascade.ID); err != nil {
		t.Errorf("пустой профиль со ссылкой: %v", err)
	}
	if _, ok, _ := st.LookupClaimToken(ctx, fmt.Sprintf("tok-%d", uniq)); ok {
		t.Error("ссылка входа пережила удаление профиля")
	}

	_ = st.DeleteTournament(ctx, solo.ID)
	_ = st.DeleteTournament(ctx, duo.ID)
}
