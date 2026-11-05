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

// createTask - одиночное добавление через общий путь создания (как в обработчике).
func createTask(ctx context.Context, st *store.Store, t models.CatalogTask) (models.CatalogTask, error) {
	res, err := st.CreateCatalogTasks(ctx, []models.CatalogTask{t})
	if err != nil {
		return models.CatalogTask{}, err
	}
	if res[0].Duplicate {
		return models.CatalogTask{}, fmt.Errorf("дубль")
	}
	return *res[0].Task, nil
}

// Классификация заданий: сохранение полей, дубли, формат матча и тип раунда при раздаче, совместимость
// со старыми строками. Запуск - как у TestMMRIntegration.
func TestTaskClassificationIntegration(t *testing.T) {
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
	// Формат и тип раунда проверяются на заданиях карты: в слот карты попадает единственное задание.
	defer store.SetMapTasksEnabled(true)()

	uniq := time.Now().UnixNano()
	prefix := fmt.Sprintf("cls-%d", uniq)
	var tourIDs []string
	defer func() {
		for _, id := range tourIDs {
			_ = st.DeleteTournament(ctx, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM catalog_tasks WHERE name LIKE $1`, prefix+"%")
	}()

	// Старая строка без новых колонок получает умолчания: раздаётся как раньше.
	var legacyID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO catalog_tasks (name, text, points, kind, active)
		VALUES ($1, 'старое', 2, 'pvpve', false) RETURNING id`, prefix+"-legacy").Scan(&legacyID); err != nil {
		t.Fatalf("старая строка: %v", err)
	}
	var f, r, rt, tag string
	if err := pool.QueryRow(ctx, `SELECT format, rarity, round_type, tag FROM catalog_tasks WHERE id = $1`, legacyID).Scan(&f, &r, &rt, &tag); err != nil {
		t.Fatal(err)
	}
	if f != "any" || r != "common" || rt != "any" || tag != "normal" {
		t.Errorf("умолчания старой строки: %s/%s/%s/%s", f, r, rt, tag)
	}

	// Пачка: все поля сохраняются, дубль внутри пачки и дубль каталога пропускаются, остальные добавляются.
	mk := func(name string) models.CatalogTask {
		return models.CatalogTask{
			Name: prefix + name, Text: "Описание " + name, Points: 1, ValueType: models.ValueFixed, Kind: "pvp",
			Source: "official", Tag: models.TaskTagTimed, Active: true,
			Format: models.TaskFormat2x2, Rarity: models.RarityLegendary, RoundType: models.RoundTypePaid,
		}
	}
	res, err := st.CreateCatalogTasks(ctx, []models.CatalogTask{mk("-a"), mk("-b"), mk("-a")})
	if err != nil {
		t.Fatalf("CreateCatalogTasks: %v", err)
	}
	if res[0].Task == nil || res[1].Task == nil || !res[2].Duplicate {
		t.Fatalf("итог пачки: %+v", res)
	}
	got := res[0].Task
	if got.Format != "2x2" || got.Rarity != "legendary" || got.RoundType != "paid" || got.Tag != "timed" || got.Points != 1 {
		t.Errorf("поля потеряны: %+v", got)
	}
	if again, err := st.CreateCatalogTasks(ctx, []models.CatalogTask{mk("-a")}); err != nil || !again[0].Duplicate {
		t.Errorf("дубль каталога не пойман: %+v %v", again, err)
	}
	// То же название и описание, но другой формат - не дубль.
	other := mk("-a")
	other.Format = models.TaskFormat1x1
	if o, err := st.CreateCatalogTasks(ctx, []models.CatalogTask{other}); err != nil || o[0].Duplicate {
		t.Errorf("другой формат принят за дубль: %+v %v", o, err)
	}

	// Раздача: у каждой проверки своя карта, на ней одно задание - в слот карты выпадает оно или ничего.
	mapSeq := 0
	totalRounds := 2 // сколько раундов в матче: бесплатный набор только у первого раунда матча из нескольких
	check := func(taskFormat, roundType, matchMode, matchFormat string, roundNumber int, wantDealt bool) {
		t.Helper()
		mapSeq++
		code := fmt.Sprintf("cls_%d_%d", uniq, mapSeq)
		if _, err := pool.Exec(ctx, `INSERT INTO maps (code, name, sort_order, active) VALUES ($1, $1, $2, false)`, code, 2000+mapSeq); err != nil {
			t.Fatalf("карта: %v", err)
		}
		defer pool.Exec(ctx, `DELETE FROM maps WHERE code = $1`, code) //nolint:errcheck
		name := fmt.Sprintf("%s-%s-%s-%s-%s-%d", prefix, taskFormat, roundType, matchMode, matchFormat, roundNumber)
		created, err := createTask(ctx, st, models.CatalogTask{
			Name: name, Text: "Проверка " + name, Points: 2, ValueType: models.ValueFixed, Kind: "pvp",
			Source: "official", Active: true, Format: taskFormat, Rarity: models.RarityCommon,
			RoundType: roundType, MapCodes: []string{code},
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		tour, err := st.CreateTournament(ctx, models.Tournament{Title: name, Mode: matchMode, PlayerType: "pvp"})
		if err != nil {
			t.Fatal(err)
		}
		tourIDs = append(tourIDs, tour.ID)
		if _, err := pool.Exec(ctx, `UPDATE tournaments SET format = $2, total_rounds = $3 WHERE id = $1`, tour.ID, matchFormat, totalRounds); err != nil {
			t.Fatal(err)
		}
		for i, n := range []string{"p1", "p2"} {
			if _, err := st.AddParticipant(ctx, models.Participant{TournamentID: tour.ID, Name: n, Seed: i + 1}); err != nil {
				t.Fatal(err)
			}
		}
		var rd models.Round
		for n := 1; n <= roundNumber; n++ {
			if rd, err = st.CreateRound(ctx, models.Round{TournamentID: tour.ID, Number: n}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pool.Exec(ctx, `UPDATE rounds SET map_code = $2 WHERE id = $1`, rd.ID, code); err != nil {
			t.Fatal(err)
		}
		if err := st.DealRoundTasks(ctx, rd.ID); err != nil {
			t.Fatal(err)
		}
		var dealt bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM round_bonus_tasks WHERE round_id = $1 AND task_id = $2)`, rd.ID, created.ID).Scan(&dealt); err != nil {
			t.Fatal(err)
		}
		if dealt != wantDealt {
			t.Errorf("%s: выдано=%v, ждали %v", name, dealt, wantDealt)
		}
	}
	const (
		match = store.FormatMatch
		show  = store.FormatShow
	)
	// Формат матча: any - везде, 1x1/2x2 - только в своём.
	check("any", "any", "1x1", match, 1, true)
	check("any", "any", "2x2", match, 1, true)
	check("1x1", "any", "1x1", match, 1, true)
	check("1x1", "any", "2x2", match, 1, false)
	check("2x2", "any", "2x2", match, 1, true)
	check("2x2", "any", "1x1", match, 1, false)
	// Тип раунда: бесплатный - первый раунд обычного матча, платный - остальные и весь шоу-матч.
	check("any", "free", "1x1", match, 1, true)
	check("any", "free", "1x1", match, 2, false)
	check("any", "free", "1x1", show, 1, false)
	check("any", "paid", "1x1", match, 1, false)
	check("any", "paid", "1x1", match, 2, true)
	check("any", "paid", "1x1", show, 1, true)
	// Комбинация: формат и тип раунда проверяются вместе.
	check("2x2", "paid", "2x2", match, 2, true)
	check("2x2", "paid", "1x1", match, 2, false)
	// Матч на один раунд - только платный, на своём наборе.
	totalRounds = 1
	check("any", "free", "2x2", match, 1, false)
	check("any", "paid", "2x2", match, 1, true)
}
