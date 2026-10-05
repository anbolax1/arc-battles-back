// Команда importseason3 переносит матчи 3 сезона в базу: из листа «Сезон 3: Матчи 1х1» таблицы
// организатора и с arcarena.ru (там есть раунды, пики-баны, задания и счёт). Матчи, которые есть
// в обоих источниках, берутся с arcarena.
//
// ×2 в таблице - две строки подряд с теми же игроками, датой и картой: один матч, который, как и
// в таблице, засчитывается за два. На arcarena ×2 - один матч с удвоенным изменением MMR, и это
// изменение берётся как есть. При переезде на arcarena рейтинг сверяется с её стартовыми
// цифрами, поэтому итог совпадает с таблицей лидеров arcarena.
//
//	DATABASE_URL=... importseason3 -csv /tmp/s3 -arena /tmp/arena [-fetch] [-dry-run]
//
// -fetch скачивает лист и данные arcarena в указанные папки; без него берутся уже скачанные файлы.
// Повторный запуск обновляет те же матчи (ключ ext_key), MMR пересчитывается целиком.
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/matchimport"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	sheetID    = "1oDrIVsXs3MZnNobpvGR19el4P6zwsdDMH98vyr7xi2g"
	sheetGID   = "431193452" // «Сезон 3: Матчи 1х1»
	sheetFile  = "s3_1x1.csv"
	arenaIndex = "matches.json"
	arenaTable = "leaderboard.json"
	// correctionSource - метка сверок этого импорта: повторный запуск заменяет их, а не копит.
	correctionSource = "arcarena"
)

func main() {
	csvDir := flag.String("csv", "", "папка с листом таблицы")
	arenaDir := flag.String("arena", "", "папка с данными arcarena")
	fetch := flag.Bool("fetch", false, "скачать лист и данные arcarena")
	dry := flag.Bool("dry-run", false, "только показать, что будет импортировано")
	flag.Parse()
	if *csvDir == "" || *arenaDir == "" {
		log.Fatal("укажите -csv и -arena")
	}
	if *fetch {
		if err := fetchSheet(*csvDir); err != nil {
			log.Fatalf("лист таблицы: %v", err)
		}
		if err := fetchArena(*arenaDir); err != nil {
			log.Fatalf("arcarena: %v", err)
		}
	}

	sheet := sheetMatches(filepath.Join(*csvDir, sheetFile))
	arena := arenaMatches(*arenaDir)
	all, dropped := merge(sheet, arena)
	log.Printf("таблица: %d матчей, arcarena: %d, совпало и взято с arcarena: %d, итого: %d",
		len(sheet), len(arena), dropped, len(all))
	if *dry {
		for _, m := range all {
			win := m.A
			if !m.WinA {
				win = m.B
			}
			if m.Draw {
				win = "ничья"
			}
			log.Printf("%s %-7s %s vs %s -> %s ×%d раундов=%d", m.Day, m.Source, m.A, m.B, win, m.Mult, len(m.Rounds))
		}
		return
	}

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL не задан")
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

	seasonID, err := seasonFor(ctx, pool, all)
	if err != nil {
		log.Fatalf("сезон: %v", err)
	}
	im := matchimport.NewImporter(pool, st)
	for _, m := range all {
		if err := im.Upsert(ctx, m, seasonID); err != nil {
			log.Fatalf("матч %s: %v", m.ExtKey, err)
		}
	}
	if err := fixImportedSeasons(ctx, pool); err != nil {
		log.Fatalf("сезоны импортированных матчей: %v", err)
	}
	if err := st.RecomputeAllMmr(ctx); err != nil {
		log.Fatalf("пересчёт MMR: %v", err)
	}
	if err := reconcile(ctx, pool, st, *arenaDir, seasonID, arena); err != nil {
		log.Fatalf("сверка с arcarena: %v", err)
	}
	if n, err := dropUnusedStubs(ctx, pool); err != nil {
		log.Fatalf("лишние заглушки: %v", err)
	} else if n > 0 {
		log.Printf("удалено заглушек без матчей: %d", n)
	}
	log.Printf("ГОТОВО: матчей 3 сезона %d, игроков %d", len(all), im.Players())
}

type arenaTableFile struct {
	Season struct {
		StartsAt time.Time `json:"startsAt"`
	} `json:"season"`
	Entries []struct {
		Nickname      string `json:"nickname"`
		Mmr           int    `json:"mmr"`
		MatchesPlayed int    `json:"matchesPlayed"`
	} `json:"entries"`
}

// reconcile подгоняет рейтинг к таблице лидеров arcarena: на старте arcarena у каждого её стартовые
// цифры (текущий MMR минус изменения за её матчи), разница с нашим счётом по таблице - сверка.
func reconcile(ctx context.Context, pool *pgxpool.Pool, st *store.Store, dir, seasonID string, arena []matchimport.Match) error {
	var tbl arenaTableFile
	readJSON(filepath.Join(dir, arenaTable), &tbl)
	if len(tbl.Entries) == 0 || tbl.Season.StartsAt.IsZero() {
		log.Printf("нет таблицы лидеров arcarena - сверку пропускаю")
		return nil
	}
	cutoff := tbl.Season.StartsAt
	played := map[string]int{}
	for _, m := range arena {
		for side, nick := range []string{m.A, m.B} {
			if m.Pins[side] != nil {
				played[strings.ToLower(matchimport.CleanNick(nick))] += *m.Pins[side]
			}
		}
	}
	var start int
	if err := pool.QueryRow(ctx, `SELECT start_mmr FROM seasons WHERE id = $1`, seasonID).Scan(&start); err != nil {
		return err
	}
	var items []store.MmrCorrection
	for _, e := range tbl.Entries {
		login := matchimport.CleanNick(e.Nickname)
		u, err := st.GetUserByLogin(ctx, login)
		if err != nil {
			continue
		}
		var before, games int
		if err := pool.QueryRow(ctx, `
			SELECT $3::int + COALESCE(SUM(delta), 0), COUNT(tournament_id) FROM mmr_history
			WHERE user_id = $1 AND mode = '1x1' AND season_key = $2 AND created_at < $4`,
			u.ID, seasonID, start, cutoff).Scan(&before, &games); err != nil {
			return err
		}
		if e.MatchesPlayed == 0 && games > 0 {
			log.Printf("у arcarena нет матчей %s, у нас %d - рейтинг оставляю по таблице", login, games)
			continue
		}
		seed := e.Mmr - played[strings.ToLower(login)]
		if d := seed - before; d != 0 {
			items = append(items, store.MmrCorrection{
				UserID: u.ID, SeasonID: seasonID, Delta: d, At: cutoff, Note: "Сверка с рейтингом arcarena.ru при переезде",
			})
		}
	}
	if err := st.ReplaceMmrCorrections(ctx, correctionSource, items); err != nil {
		return err
	}
	if err := st.RecomputeAllMmr(ctx); err != nil {
		return err
	}
	bad := 0
	for _, e := range tbl.Entries {
		u, err := st.GetUserByLogin(ctx, matchimport.CleanNick(e.Nickname))
		if err != nil || e.MatchesPlayed == 0 {
			continue
		}
		var now int
		if err := pool.QueryRow(ctx, `
			SELECT $3::int + COALESCE(SUM(delta), 0) FROM mmr_history WHERE user_id = $1 AND mode = '1x1' AND season_key = $2`,
			u.ID, seasonID, start).Scan(&now); err != nil {
			return err
		}
		if now != e.Mmr {
			bad++
			log.Printf("расхождение с arcarena: %s у нас %d, там %d", e.Nickname, now, e.Mmr)
		}
	}
	log.Printf("сверок: %d, расхождений с таблицей лидеров arcarena: %d", len(items), bad)
	return nil
}

// dropUnusedStubs удаляет созданные импортом аккаунты-заглушки, у которых не осталось матчей.
func dropUnusedStubs(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	ct, err := pool.Exec(ctx, `
		DELETE FROM users u WHERE u.password_hash = '!imported'
		  AND NOT EXISTS (SELECT 1 FROM participants p WHERE p.user_id = u.id)
		  AND NOT EXISTS (SELECT 1 FROM participants p, jsonb_array_elements(p.members) m WHERE m->>'userId' = u.id)
		  AND NOT EXISTS (SELECT 1 FROM registrations r WHERE r.user_id = u.id)`)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// seasonFor - сезон, в который ложатся матчи: тот, чьи даты их покрывают (обычно текущий).
func seasonFor(ctx context.Context, pool *pgxpool.Pool, all []matchimport.Match) (string, error) {
	if len(all) == 0 {
		return "", fmt.Errorf("нет матчей")
	}
	first := all[0].Date
	var id string
	err := pool.QueryRow(ctx, `
		SELECT id FROM seasons
		WHERE started_at <= $1::timestamptz + interval '10 days' AND (ended_at IS NULL OR ended_at >= $1::timestamptz)
		ORDER BY started_at DESC LIMIT 1`, first).Scan(&id)
	return id, err
}

// fixImportedSeasons раскладывает импортированные матчи по сезонам их дат: матч конца прошлого
// сезона, заведённый уже в новом, возвращается в свой.
func fixImportedSeasons(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		UPDATE tournaments t SET season_id = s.id
		FROM seasons s
		WHERE t.ext_key IS NOT NULL AND t.starts_at IS NOT NULL
		  AND t.starts_at >= s.started_at AND (s.ended_at IS NULL OR t.starts_at < s.ended_at + interval '1 day')
		  AND t.season_id IS DISTINCT FROM s.id
		  AND NOT EXISTS (
		      SELECT 1 FROM seasons s2 WHERE s2.started_at > s.started_at AND t.starts_at >= s2.started_at)`)
	return err
}

// ---------- таблица ----------

func sheetMatches(path string) []matchimport.Match {
	rows := readCSV(path)
	type row struct {
		n                          int
		day, format, a, b, win, mp string
	}
	var list []row
	for _, r := range rows {
		if len(r) < 8 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(r[1]))
		if err != nil || strings.TrimSpace(r[4]) == "" || strings.TrimSpace(r[5]) == "" {
			continue
		}
		list = append(list, row{n, strings.TrimSpace(r[2]), strings.ToLower(strings.TrimSpace(r[3])),
			strings.TrimSpace(r[4]), strings.TrimSpace(r[5]), strings.TrimSpace(r[6]), strings.TrimSpace(r[7])})
	}
	var out []matchimport.Match
	for i := 0; i < len(list); i++ {
		r := list[i]
		mult := 1
		if i+1 < len(list) {
			nx := list[i+1]
			samePair := (matchimport.SameNick(nx.a, r.a) && matchimport.SameNick(nx.b, r.b)) || (matchimport.SameNick(nx.a, r.b) && matchimport.SameNick(nx.b, r.a))
			if nx.day == r.day && samePair && strings.EqualFold(nx.mp, r.mp) && matchimport.SameNick(nx.win, r.win) {
				mult = 2
				i++
			}
		}
		date := sheetDate(r.day).Add(time.Duration(len(out)) * time.Second)
		pt := "pvp"
		if r.format == "pve" {
			pt = "pve"
		}
		out = append(out, matchimport.Match{
			ExtKey: fmt.Sprintf("s3sheet|%d|%s|%s|%s", r.n, r.day, strings.ToUpper(matchimport.CleanNick(r.a)), strings.ToUpper(matchimport.CleanNick(r.b))),
			Source: "таблица", Date: date, Day: date.In(matchimport.Moscow).Format("2006-01-02"),
			A: r.a, B: r.b, WinA: matchimport.SameNick(r.win, r.a), Draw: r.win == "", Mult: mult, Games: mult, PlayerType: pt,
			Rounds: []matchimport.Round{{Number: 1, MapCode: mapCodeByName(r.mp)}},
		})
	}
	return out
}

func mapCodeByName(name string) string {
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), "Ё", "Е"))
	switch {
	case strings.Contains(n, "БУРН"):
		return "stormy_flows"
	case strings.Contains(n, "СИНИЕ"):
		return "blue_gate"
	case strings.Contains(n, "ПОГРЕБ"):
		return "buried_city"
	case strings.Contains(n, "КОСМОПОРТ"):
		return "spaceport"
	case strings.Contains(n, "ДАМБ"):
		return "dam"
	case strings.Contains(n, "СТЕЛЛА"):
		return "stella_montis"
	}
	return ""
}

func sheetDate(s string) time.Time {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) >= 2 {
		d, e1 := strconv.Atoi(parts[0])
		m, e2 := strconv.Atoi(parts[1])
		if e1 == nil && e2 == nil {
			return time.Date(2026, time.Month(m), d, 15, 0, 0, 0, matchimport.Moscow)
		}
	}
	return time.Date(2026, 8, 17, 15, 0, 0, 0, matchimport.Moscow)
}

// ---------- arcarena ----------

func arenaMatches(dir string) []matchimport.Match {
	var idx struct {
		Matches []matchimport.ArenaMatch `json:"matches"`
	}
	readJSON(filepath.Join(dir, arenaIndex), &idx)
	var out []matchimport.Match
	for _, am := range idx.Matches {
		if !am.Importable() {
			continue
		}
		var live matchimport.ArenaLive
		var veto matchimport.ArenaVeto
		readJSON(filepath.Join(dir, "live_"+am.ID+".json"), &live)
		readJSON(filepath.Join(dir, "veto_"+am.ID+".json"), &veto)
		out = append(out, matchimport.FromArena(am, live, veto))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// merge склеивает источники: матч из таблицы, который есть и на arcarena (тот же день, те же
// игроки и победитель), берётся с arcarena.
func merge(sheet, arena []matchimport.Match) ([]matchimport.Match, int) {
	used := make([]bool, len(arena))
	var out []matchimport.Match
	dropped := 0
	for _, s := range sheet {
		dup := false
		for i, a := range arena {
			if used[i] || a.Day != s.Day {
				continue
			}
			samePair := (matchimport.SameNick(a.A, s.A) && matchimport.SameNick(a.B, s.B)) || (matchimport.SameNick(a.A, s.B) && matchimport.SameNick(a.B, s.A))
			if !samePair {
				continue
			}
			winS, winA := s.B, a.B
			if s.WinA {
				winS = s.A
			}
			if a.WinA {
				winA = a.A
			}
			if matchimport.SameNick(winS, winA) {
				used[i], dup = true, true
				break
			}
		}
		if dup {
			dropped++
			continue
		}
		out = append(out, s)
	}
	out = append(out, arena...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, dropped
}

// ---------- загрузка ----------

func fetchSheet(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := matchimport.Get(fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s/gviz/tq?tqx=out:csv&headers=0&gid=%s", sheetID, sheetGID))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sheetFile), body, 0o644)
}

func fetchArena(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := matchimport.Get(matchimport.ArenaAPI + "/matches")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, arenaIndex), body, 0o644); err != nil {
		return err
	}
	table, err := matchimport.Get(matchimport.ArenaAPI + "/leaderboard")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, arenaTable), table, 0o644); err != nil {
		return err
	}
	var idx struct {
		Matches []matchimport.ArenaMatch `json:"matches"`
	}
	if err := json.Unmarshal(body, &idx); err != nil {
		return err
	}
	for _, m := range idx.Matches {
		if m.Status != "finished" {
			continue
		}
		for _, part := range []string{"live", "veto"} {
			b, err := matchimport.Get(matchimport.ArenaAPI + "/matches/" + m.ID + "/" + part)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, part+"_"+m.ID+".json"), b, 0o644); err != nil {
				return err
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	return nil
}

func readJSON(path string, dst any) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, dst); err != nil {
		log.Printf("разбор %s: %v", path, err)
	}
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
