// Команда importhistory — импорт/синхронизация истории турниров «Битва за Респект» из
// Google-таблицы в БД.
//
// Режимы:
//
//	-wipe-all         снести ВСЕ турниры сайта и залить только данные таблицы (первичный импорт)
//	-force            пересобрать только импортированные ([история]) турниры
//	-sync             синхронизация: обновить [история]-турниры по ext_key (стабильные id),
//	                  не трогая турниры из админки/эфира; MMR пересчитывается по всем матчам
//
// Источник данных: локальные CSV из -csv ЛИБО прямая загрузка листов из Google по -sheet <id>.
//
// Пример (cron, каждые 10 минут):
//
//	DATABASE_URL=... importhistory -sheet <SHEET_ID> -csv /tmp/arc-sync -sync
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Алиасы ников → логин аккаунта: (1) в истории ник записан иначе, чем в ростере; (2) игрок уже
// зарегистрирован реальным аккаунтом под другим логином — историю вешаем на реальный аккаунт.
var aliases = map[string]string{
	"QWERTY":  "QWERTY345",
	"1STW00D": "Istwood",
}

func resolveNick(n string) string {
	n = strings.TrimSpace(n)
	if a, ok := aliases[strings.ToUpper(n)]; ok {
		return a
	}
	return n
}

// gids вкладок, нужных для импорта.
var gids = map[string]string{
	"02_1x1_history.csv": "364075199",
	"03_2x2_teams.csv":   "2142761644",
	"05_2x2_history.csv": "842608179",
}

// Side — сторона матча (игрок в 1×1 или команда в 2×2).
type Side struct {
	Name    string   // отображаемое имя (ник или название команды)
	UserID  string   // 1×1
	Members []string // 2×2 — userId состава
	IsTeam  bool
}

// Match — один матч из таблицы с детерминированным внешним ключом.
type Match struct {
	ExtKey string
	Mode   string // 1x1 | 2x2
	Date   time.Time
	Title  string
	Map    string
	Draw   bool
	A, B   Side
	WinA   bool // победила сторона A (если !Draw)
}

func main() {
	csvDir := flag.String("csv", ".", "папка с CSV (или куда скачивать при -sheet)")
	sheet := flag.String("sheet", "", "ID Google-таблицы — скачать листы напрямую")
	doSync := flag.Bool("sync", false, "синхронизация: обновить [история] по ext_key, MMR пересчитать по всем")
	force := flag.Bool("force", false, "пересобрать только [история]")
	wipeAll := flag.Bool("wipe-all", false, "снести ВСЕ турниры сайта, оставить только импорт")
	flag.Parse()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = os.Getenv("RESPECT_TEST_DB")
	}
	if url == "" {
		log.Fatal("не задан DATABASE_URL")
	}

	// Загрузку делаем ДО подключения к БД — сбой сети не должен трогать данные.
	if *sheet != "" {
		if err := fetchSheet(*sheet, *csvDir); err != nil {
			log.Fatalf("загрузка таблицы: %v", err)
		}
		log.Println("листы таблицы загружены в", *csvDir)
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

	if !*doSync && !*force && !*wipeAll {
		var hist int
		_ = pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM mmr_history)+(SELECT COUNT(*) FROM team_mmr_history)`).Scan(&hist)
		if hist > 0 {
			log.Fatalf("в БД есть данные MMR — укажите режим: -sync (штатный), -force или -wipe-all")
		}
	}

	userCache := map[string]string{}
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
		u, err := st.CreateUser(ctx, login, login, "!imported", models.Role("user"))
		if err != nil {
			if u2, e2 := st.GetUserByLogin(ctx, login); e2 == nil {
				userCache[key] = u2.ID
				return u2.ID, nil
			}
			return "", err
		}
		userCache[key] = u.ID
		return u.ID, nil
	}

	matches, skipped := buildMatches(*csvDir, ensureUser)

	// Режим reset: wipe-all сносит все турниры, force — только [история].
	if *wipeAll {
		if _, err := pool.Exec(ctx, `DELETE FROM tournaments`); err != nil {
			log.Fatalf("wipe-all: %v", err)
		}
		log.Println("ВСЕ турниры сайта удалены (-wipe-all)")
	} else if *force {
		if _, err := pool.Exec(ctx, `DELETE FROM tournaments WHERE title LIKE '[история]%'`); err != nil {
			log.Fatalf("force reset: %v", err)
		}
	}

	// Upsert каждого матча (стабильный id по ext_key).
	keys := make([]string, 0, len(matches))
	for _, m := range matches {
		if err := upsertMatch(ctx, pool, st, m); err != nil {
			log.Fatalf("upsert %s: %v", m.ExtKey, err)
		}
		keys = append(keys, m.ExtKey)
	}

	// Удаляем импортированные турниры, которых больше нет в таблице (актуально для -sync).
	// Страховка: при пустом наборе (напр. сбой/пустая выгрузка) НЕ трогаем существующие.
	if len(keys) > 0 {
		if _, err := pool.Exec(ctx,
			`DELETE FROM tournaments WHERE title LIKE '[история]%' AND ext_key <> ALL($1)`, keys); err != nil {
			log.Fatalf("удаление устаревших: %v", err)
		}
	} else {
		log.Println("ВНИМАНИЕ: 0 матчей из таблицы — устаревшие НЕ удаляю (страховка)")
	}

	// Полный пересчёт MMR по всем завершённым турнирам (импорт + админские).
	if err := st.RecomputeAllMmr(ctx); err != nil {
		log.Fatalf("пересчёт MMR: %v", err)
	}

	// Бэкдейт истории MMR под дату матча (для графика/ленты).
	for _, q := range []string{
		`UPDATE mmr_history h SET created_at=t.starts_at FROM tournaments t WHERE t.id=h.tournament_id AND t.starts_at IS NOT NULL`,
		`UPDATE team_mmr_history h SET created_at=t.starts_at FROM tournaments t WHERE t.id=h.tournament_id AND t.starts_at IS NOT NULL`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			log.Fatalf("бэкдейт: %v", err)
		}
	}

	// Чистка заглушек-сирот (аккаунты импорта, ни на что не завязанные, — напр. мэпнутые на реальные).
	if _, err := pool.Exec(ctx, `
		DELETE FROM users u WHERE u.password_hash='!imported'
		  AND NOT EXISTS (SELECT 1 FROM team_mmr t WHERE t.member_a=u.id OR t.member_b=u.id)
		  AND NOT EXISTS (SELECT 1 FROM participants p WHERE p.user_id=u.id)
		  AND NOT EXISTS (SELECT 1 FROM participants p WHERE p.members @> jsonb_build_array(jsonb_build_object('userId', u.id)))`); err != nil {
		log.Fatalf("чистка сирот: %v", err)
	}

	mode := "import"
	if *doSync {
		mode = "sync"
	}
	log.Printf("ГОТОВО (%s): матчей=%d (пропущено 2×2=%d), аккаунтов=%d", mode, len(matches), skipped, len(userCache))
}

// upsertMatch создаёт или обновляет [история]-турнир по ext_key (id стабилен), заменяя состав и раунд.
func upsertMatch(ctx context.Context, pool *pgxpool.Pool, st *store.Store, m Match) error {
	mapsJSON := "[]"
	if m.Map != "" {
		b, _ := json.Marshal([]string{m.Map})
		mapsJSON = string(b)
	}
	var tid string
	err := pool.QueryRow(ctx, `SELECT id FROM tournaments WHERE ext_key=$1`, m.ExtKey).Scan(&tid)
	if err != nil {
		// нет — создаём
		if err := pool.QueryRow(ctx, `
			INSERT INTO tournaments (title, mode, player_type, status, total_rounds, maps, starts_at, rating_multiplier, ext_key, season_id)
			VALUES ($1,$2,'pvpve','finished',1,$3,$4,1,$5,(SELECT id FROM seasons WHERE status='active' LIMIT 1))
			RETURNING id`, m.Title, m.Mode, mapsJSON, m.Date, m.ExtKey).Scan(&tid); err != nil {
			return err
		}
	} else {
		if _, err := pool.Exec(ctx, `
			UPDATE tournaments SET title=$2, mode=$3, maps=$4, starts_at=$5, status='finished', updated_at=now()
			WHERE id=$1`, tid, m.Title, m.Mode, mapsJSON, m.Date); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM participants WHERE tournament_id=$1`, tid); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM rounds WHERE tournament_id=$1`, tid); err != nil {
			return err
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rounds (tournament_id, number, map, status) VALUES ($1,1,$2,'finished')`, tid, m.Map); err != nil {
		return err
	}

	mkPart := func(s Side, seed int) (string, error) {
		p := models.Participant{TournamentID: tid, Name: s.Name, Seed: seed}
		if s.IsTeam {
			p.Kind = "team"
			arr := []map[string]string{}
			for _, id := range s.Members {
				arr = append(arr, map[string]string{"userId": id, "name": id})
			}
			b, _ := json.Marshal(arr)
			p.Members = b
		} else {
			p.Kind = "player"
			uid := s.UserID
			p.UserID = &uid
		}
		created, err := st.AddParticipant(ctx, p)
		return created.ID, err
	}
	paID, err := mkPart(m.A, 1)
	if err != nil {
		return err
	}
	pbID, err := mkPart(m.B, 2)
	if err != nil {
		return err
	}
	if !m.Draw {
		winner := paID
		if !m.WinA {
			winner = pbID
		}
		if _, err := pool.Exec(ctx, `UPDATE tournaments SET winner_participant_id=$2 WHERE id=$1`, tid, winner); err != nil {
			return err
		}
	} else {
		if _, err := pool.Exec(ctx, `UPDATE tournaments SET winner_participant_id=NULL WHERE id=$1`, tid); err != nil {
			return err
		}
	}
	return nil
}

// buildMatches парсит CSV в список матчей (создавая аккаунты). skipped — сколько 2×2 пропущено
// (не нашёлся состав команды).
func buildMatches(csvDir string, ensureUser func(string) (string, error)) ([]Match, int) {
	var out []Match
	occ := map[string]int{}
	extKey := func(mode, date, a, b string) string {
		base := mode + "|" + date + "|" + strings.ToUpper(strings.TrimSpace(a)) + "|" + strings.ToUpper(strings.TrimSpace(b))
		k := base + "|" + strconv.Itoa(occ[base])
		occ[base]++
		return k
	}
	// starts_at = дата матча + посекундный сдвиг по глобальному порядку строк, чтобы порядок
	// MMR-пересчёта был точным и стабильным (не зависел от created_at, который после upsert путается).
	seq := 0
	nextDate := func(s string) time.Time {
		d := parseDate(s).Add(time.Duration(seq) * time.Second)
		seq++
		return d
	}

	// 1×1
	rows1 := readCSV(filepath.Join(csvDir, "02_1x1_history.csv"))
	for _, r := range rows1[1:] {
		if len(r) < 7 || strings.TrimSpace(r[4]) == "" || strings.TrimSpace(r[5]) == "" {
			continue
		}
		a, b, win := strings.TrimSpace(r[4]), strings.TrimSpace(r[5]), strings.TrimSpace(r[6])
		mp := ""
		if len(r) > 7 {
			mp = strings.TrimSpace(r[7])
		}
		aID, err := ensureUser(a)
		if err != nil {
			log.Fatalf("user %s: %v", a, err)
		}
		bID, err := ensureUser(b)
		if err != nil {
			log.Fatalf("user %s: %v", b, err)
		}
		out = append(out, Match{
			ExtKey: extKey("1x1", strings.TrimSpace(r[2]), a, b),
			Mode:   "1x1", Date: nextDate(r[2]), Title: "[история] " + a + " vs " + b, Map: mp,
			Draw: win == "", WinA: sameNick(win, a),
			A: Side{Name: a, UserID: aID}, B: Side{Name: b, UserID: bID},
		})
	}

	// 2×2 составы
	teamMembers := map[string][2]string{}
	for _, r := range readCSV(filepath.Join(csvDir, "03_2x2_teams.csv"))[1:] {
		if len(r) < 5 || strings.TrimSpace(r[2]) == "" {
			continue
		}
		teamMembers[strings.ToUpper(strings.TrimSpace(r[2]))] = [2]string{strings.TrimSpace(r[3]), strings.TrimSpace(r[4])}
	}
	skipped := 0
	for _, r := range readCSV(filepath.Join(csvDir, "05_2x2_history.csv"))[1:] {
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
			log.Printf("2×2: пропуск — нет состава %q/%q", nameA, nameB)
			skipped++
			continue
		}
		membersOf := func(nn [2]string) []string {
			var ids []string
			for _, nk := range nn {
				if strings.TrimSpace(nk) == "" {
					continue
				}
				id, err := ensureUser(nk)
				if err != nil {
					log.Fatalf("состав %s: %v", nk, err)
				}
				ids = append(ids, id)
			}
			return ids
		}
		out = append(out, Match{
			ExtKey: extKey("2x2", strings.TrimSpace(r[2]), nameA, nameB),
			Mode:   "2x2", Date: nextDate(r[2]), Title: "[история] " + nameA + " vs " + nameB, Map: mp,
			Draw: win == "", WinA: sameNick(win, nameA),
			A: Side{Name: nameA, IsTeam: true, Members: membersOf(ma)},
			B: Side{Name: nameB, IsTeam: true, Members: membersOf(mb)},
		})
	}
	return out, skipped
}

func fetchSheet(id, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	for name, gid := range gids {
		url := fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s/gviz/tq?tqx=out:csv&gid=%s", id, gid)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: HTTP %d", name, resp.StatusCode)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func sameNick(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(resolveNick(a)), strings.TrimSpace(resolveNick(b)))
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
