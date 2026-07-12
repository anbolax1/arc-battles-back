// Команда importhistory — одноразовый импорт истории турниров «Битва за Респект» из выгрузок
// Google-таблицы (CSV) в БД: создаёт аккаунты по никам, проигрывает КАЖДУЮ строку истории как
// матч (1×1 и 2×2) в хронологическом порядке через store.ApplyTournamentMmr (Elo K=32), бэкдейтит
// время под дату матча — чтобы MMR, статистика и графики были заполнены реальной историей.
//
// Запуск (локально):
//
//	DATABASE_URL=postgres://respect:respect@localhost:5433/respect \
//	go run ./cmd/importhistory -csv "<путь к папке с CSV>" -force
//
// Ожидаемые файлы в -csv: 01_1x1_players.csv, 02_1x1_history.csv, 03_2x2_teams.csv,
// 04_2x2_ratings.csv, 05_2x2_history.csv.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Алиасы ников: в истории матчей встречается сокращённая форма, в ростере — полная.
var aliases = map[string]string{"QWERTY": "QWERTY345"}

func resolveNick(n string) string {
	n = strings.TrimSpace(n)
	if a, ok := aliases[strings.ToUpper(n)]; ok {
		return a
	}
	return n
}

func main() {
	csvDir := flag.String("csv", ".", "папка с CSV-выгрузками вкладок")
	force := flag.Bool("force", false, "сбросить прежний импорт ([история]) и MMR перед импортом")
	wipeAll := flag.Bool("wipe-all", false, "снести ВСЕ турниры/статистику сайта, оставить только импорт (для прода)")
	flag.Parse()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = os.Getenv("RESPECT_TEST_DB")
	}
	if url == "" {
		log.Fatal("не задан DATABASE_URL")
	}

	if err := db.Migrate(url); err != nil {
		log.Fatalf("миграции: %v", err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		log.Fatalf("подключение: %v", err)
	}
	defer pool.Close()
	st := store.New(pool)

	// Защита: не затирать реальные (не импортированные) матчи.
	var histCount int
	_ = pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM mmr_history) + (SELECT COUNT(*) FROM team_mmr_history)`).Scan(&histCount)
	if histCount > 0 && !*force && !*wipeAll {
		log.Fatalf("в БД уже есть %d записей MMR-истории — запустите с -force (сброс импорта) или -wipe-all (снести всё)", histCount)
	}
	if *wipeAll {
		// Снести ВСЮ старую статистику сайта: все турниры (каскадом — участники, раунды,
		// результаты, mmr_history/team_mmr_history) + кэши MMR. Пользователи/сезоны/каталог остаются.
		if _, err := pool.Exec(ctx, `DELETE FROM tournaments`); err != nil {
			log.Fatalf("wipe-all турниров: %v", err)
		}
		log.Println("ВСЕ турниры и статистика сайта удалены (-wipe-all)")
	} else if *force {
		if _, err := pool.Exec(ctx, `DELETE FROM tournaments WHERE title LIKE '[история]%'`); err != nil {
			log.Fatalf("сброс импорта: %v", err)
		}
		log.Println("прежний импорт ([история]) удалён (-force)")
	}
	if *force || *wipeAll {
		for _, q := range []string{`TRUNCATE mmr_history`, `TRUNCATE team_mmr_history`, `DELETE FROM user_mmr`, `DELETE FROM team_mmr`} {
			if _, err := pool.Exec(ctx, q); err != nil {
				log.Fatalf("сброс MMR (%s): %v", q, err)
			}
		}
	}

	userCache := map[string]string{} // login -> userID
	ensureUser := func(nick string) (string, error) {
		login := resolveNick(nick)
		if login == "" {
			return "", fmt.Errorf("пустой ник")
		}
		key := strings.ToLower(login)
		if id, ok := userCache[key]; ok {
			return id, nil
		}
		if u, err := st.GetUserByLogin(ctx, login); err == nil {
			userCache[key] = u.ID
			return u.ID, nil
		}
		// Плейсхолдер-аккаунт: пароль-хеш заведомо невалиден (войти нельзя), роль — обычная.
		u, err := st.CreateUser(ctx, login, login, "!imported", models.Role("user"))
		if err != nil {
			// возможна гонка/уже существует — попробуем прочитать
			if u2, e2 := st.GetUserByLogin(ctx, login); e2 == nil {
				userCache[key] = u2.ID
				return u2.ID, nil
			}
			return "", err
		}
		userCache[key] = u.ID
		return u.ID, nil
	}

	backdate := func(tid string, at time.Time) {
		_, _ = pool.Exec(ctx, `UPDATE mmr_history SET created_at=$2 WHERE tournament_id=$1`, tid, at)
		_, _ = pool.Exec(ctx, `UPDATE team_mmr_history SET created_at=$2 WHERE tournament_id=$1`, tid, at)
	}

	// ---------------- 1×1 ----------------
	rows1 := readCSV(filepath.Join(*csvDir, "02_1x1_history.csv"))
	seq := 0
	imported1, draws := 0, 0
	for _, r := range rows1[1:] {
		if len(r) < 7 || strings.TrimSpace(r[4]) == "" || strings.TrimSpace(r[5]) == "" {
			continue
		}
		aNick, bNick, win := strings.TrimSpace(r[4]), strings.TrimSpace(r[5]), strings.TrimSpace(r[6])
		mp := ""
		if len(r) > 7 {
			mp = strings.TrimSpace(r[7])
		}
		at := parseDate(r[2]).Add(time.Duration(seq) * time.Second)
		seq++
		aID, err := ensureUser(aNick)
		if err != nil {
			log.Fatalf("user %s: %v", aNick, err)
		}
		bID, err := ensureUser(bNick)
		if err != nil {
			log.Fatalf("user %s: %v", bNick, err)
		}
		t, err := st.CreateTournament(ctx, models.Tournament{
			Title: "[история] " + aNick + " vs " + bNick, Mode: "1x1", Maps: mapsOf(mp), StartsAt: &at,
		})
		if err != nil {
			log.Fatalf("турнир 1×1: %v", err)
		}
		pa, err := st.AddParticipant(ctx, models.Participant{TournamentID: t.ID, Kind: "player", UserID: &aID, Name: aNick, Seed: 1})
		if err != nil {
			log.Fatalf("участник A: %v", err)
		}
		pb, err := st.AddParticipant(ctx, models.Participant{TournamentID: t.ID, Kind: "player", UserID: &bID, Name: bNick, Seed: 2})
		if err != nil {
			log.Fatalf("участник B: %v", err)
		}
		if win == "" {
			// Ничья: матч есть, MMR никому.
			if _, err := st.UpdateTournamentStatus(ctx, t.ID, "finished"); err != nil {
				log.Fatalf("статус ничьи: %v", err)
			}
			draws++
			continue
		}
		winnerID := pa.ID
		if !sameNick(win, aNick) {
			winnerID = pb.ID
		}
		if _, err := st.SetTournamentWinner(ctx, t.ID, winnerID); err != nil {
			log.Fatalf("победитель 1×1: %v", err)
		}
		if err := st.ApplyTournamentMmr(ctx, t.ID); err != nil {
			log.Fatalf("MMR 1×1: %v", err)
		}
		backdate(t.ID, at)
		imported1++
	}

	// ---------------- 2×2 ----------------
	teamMembers := map[string][2]string{} // upper(teamName) -> [nickA, nickB]
	for _, r := range readCSV(filepath.Join(*csvDir, "03_2x2_teams.csv"))[1:] {
		if len(r) < 5 || strings.TrimSpace(r[2]) == "" {
			continue
		}
		teamMembers[strings.ToUpper(strings.TrimSpace(r[2]))] = [2]string{strings.TrimSpace(r[3]), strings.TrimSpace(r[4])}
	}

	rows2 := readCSV(filepath.Join(*csvDir, "05_2x2_history.csv"))
	seq2 := 0
	imported2, skipped2 := 0, 0
	for _, r := range rows2[1:] {
		if len(r) < 7 || strings.TrimSpace(r[4]) == "" || strings.TrimSpace(r[5]) == "" {
			continue
		}
		nameA, nameB, win := strings.TrimSpace(r[4]), strings.TrimSpace(r[5]), strings.TrimSpace(r[6])
		mp := ""
		if len(r) > 7 {
			mp = strings.TrimSpace(r[7])
		}
		ma, okA := teamMembers[strings.ToUpper(nameA)]
		mb, okB := teamMembers[strings.ToUpper(nameB)]
		if !okA || !okB {
			log.Printf("2×2: пропуск матча — не нашёл состав команды %q/%q", nameA, nameB)
			skipped2++
			continue
		}
		membersA, err := membersJSON(ensureUser, ma)
		if err != nil {
			log.Fatalf("состав %s: %v", nameA, err)
		}
		membersB, err := membersJSON(ensureUser, mb)
		if err != nil {
			log.Fatalf("состав %s: %v", nameB, err)
		}
		at := parseDate(r[2]).Add(time.Duration(seq2) * time.Second)
		seq2++
		t, err := st.CreateTournament(ctx, models.Tournament{
			Title: "[история] " + nameA + " vs " + nameB, Mode: "2x2", Maps: mapsOf(mp), StartsAt: &at,
		})
		if err != nil {
			log.Fatalf("турнир 2×2: %v", err)
		}
		pa, err := st.AddParticipant(ctx, models.Participant{TournamentID: t.ID, Kind: "team", Name: nameA, Seed: 1, Members: membersA})
		if err != nil {
			log.Fatalf("команда A: %v", err)
		}
		pb, err := st.AddParticipant(ctx, models.Participant{TournamentID: t.ID, Kind: "team", Name: nameB, Seed: 2, Members: membersB})
		if err != nil {
			log.Fatalf("команда B: %v", err)
		}
		if win == "" {
			if _, err := st.UpdateTournamentStatus(ctx, t.ID, "finished"); err != nil {
				log.Fatalf("статус ничьи 2×2: %v", err)
			}
			continue
		}
		winnerID := pa.ID
		if !sameNick(win, nameA) {
			winnerID = pb.ID
		}
		if _, err := st.SetTournamentWinner(ctx, t.ID, winnerID); err != nil {
			log.Fatalf("победитель 2×2: %v", err)
		}
		if err := st.ApplyTournamentMmr(ctx, t.ID); err != nil {
			log.Fatalf("MMR 2×2: %v", err)
		}
		backdate(t.ID, at)
		imported2++
	}

	log.Printf("ГОТОВО: 1×1 матчей=%d (ничьих=%d), 2×2 матчей=%d (пропущено=%d), аккаунтов=%d",
		imported1, draws, imported2, skipped2, len(userCache))
}

func membersJSON(ensure func(string) (string, error), nicks [2]string) (json.RawMessage, error) {
	arr := []map[string]string{}
	for _, n := range nicks {
		if strings.TrimSpace(n) == "" {
			continue
		}
		id, err := ensure(n)
		if err != nil {
			return nil, err
		}
		arr = append(arr, map[string]string{"userId": id, "name": strings.TrimSpace(n)})
	}
	return json.Marshal(arr)
}

func sameNick(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(resolveNick(a)), strings.TrimSpace(resolveNick(b)))
}

func mapsOf(m string) []string {
	if strings.TrimSpace(m) == "" {
		return []string{}
	}
	return []string{strings.TrimSpace(m)}
}

func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if len(parts) >= 2 {
		d, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		m, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if e1 == nil && e2 == nil && d >= 1 && d <= 31 && m >= 1 && m <= 12 {
			return time.Date(2026, time.Month(m), d, 12, 0, 0, 0, time.UTC)
		}
	}
	return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
}

func readCSV(path string) [][]string {
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("не открыть %s: %v", path, err)
	}
	defer f.Close()
	rd := csv.NewReader(f)
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true
	var out [][]string
	for {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("csv %s: %v", path, err)
		}
		out = append(out, rec)
	}
	return out
}
