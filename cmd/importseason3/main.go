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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	sheetID    = "1oDrIVsXs3MZnNobpvGR19el4P6zwsdDMH98vyr7xi2g"
	sheetGID   = "431193452" // «Сезон 3: Матчи 1х1»
	sheetFile  = "s3_1x1.csv"
	arenaAPI   = "https://arcarena.ru/api"
	arenaIndex = "matches.json"
	arenaTable = "leaderboard.json"
	// correctionSource - метка сверок этого импорта: повторный запуск заменяет их, а не копит.
	correctionSource = "arcarena"
)

// Ник в источнике -> логин аккаунта: игрок записан иначе или уже есть под другим логином.
var aliases = map[string]string{
	"1STW00D":  "Istwood",
	"JONNY_92": "J0NNY_92",
	"KUNAYO":   "KUNAY0",
	"MORES322": "M0RES322",
}

// Коды карт arcarena -> наши.
var arenaMaps = map[string]string{
	"stormy_flows":   "stormy_flows",
	"blue_gates":     "blue_gate",
	"buried_city":    "buried_city",
	"spaceport":      "spaceport",
	"battle_of_dam":  "dam",
	"stellar_montis": "stella_montis",
}

var moscow = time.FixedZone("MSK", 3*3600)

var nickJunk = regexp.MustCompile(`[^\p{L}\p{N}_\-\.]+`)

// cleanNick убирает украшения вроде кубка чемпиона и приводит ник к логину аккаунта.
func cleanNick(n string) string {
	n = strings.TrimSpace(nickJunk.ReplaceAllString(strings.TrimSpace(n), " "))
	n = strings.Join(strings.Fields(n), " ")
	if a, ok := aliases[strings.ToUpper(n)]; ok {
		return a
	}
	return n
}

func sameNick(a, b string) bool { return strings.EqualFold(cleanNick(a), cleanNick(b)) }

// importRound - раунд матча с картой, заданиями и ручными очками сторон.
type importRound struct {
	Number  int
	MapCode string
	Tasks   []importTask
	Manual  [2]int
}

type importTask struct {
	Side      int // 0 - сторона A, 1 - B
	Name      string
	Text      string
	Points    int
	Category  string // task | protocol
	MapCode   string
	Completed bool
}

type importVeto struct {
	Action  string
	MapCode string
	Round   int
}

// match - матч из любого источника в общем виде.
type match struct {
	ExtKey     string
	Source     string
	Date       time.Time
	Day        string // дата по Москве, для склейки источников
	A, B       string
	WinA, Draw bool
	Mult       int
	Games      int     // сколько матчей засчитывает: ×2 таблицы - два
	Pins       [2]*int // изменение MMR сторон с arcarena - берётся как есть
	PlayerType string
	Rounds     []importRound
	Veto       []importVeto
}

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
	users := map[string]string{}
	ensureUser := func(nick string) (string, error) {
		login := cleanNick(nick)
		key := strings.ToLower(login)
		if id, ok := users[key]; ok {
			return id, nil
		}
		if u, err := st.GetUserByLogin(ctx, login); err == nil {
			users[key] = u.ID
			return u.ID, nil
		}
		u, err := st.CreateUser(ctx, login, login, "!imported", models.RoleUser)
		if err != nil {
			return "", err
		}
		users[key] = u.ID
		return u.ID, nil
	}

	for _, m := range all {
		if err := upsert(ctx, pool, st, m, seasonID, ensureUser); err != nil {
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
	log.Printf("ГОТОВО: матчей 3 сезона %d, игроков %d", len(all), len(users))
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
func reconcile(ctx context.Context, pool *pgxpool.Pool, st *store.Store, dir, seasonID string, arena []match) error {
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
				played[strings.ToLower(cleanNick(nick))] += *m.Pins[side]
			}
		}
	}
	var start int
	if err := pool.QueryRow(ctx, `SELECT start_mmr FROM seasons WHERE id = $1`, seasonID).Scan(&start); err != nil {
		return err
	}
	var items []store.MmrCorrection
	for _, e := range tbl.Entries {
		login := cleanNick(e.Nickname)
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
		u, err := st.GetUserByLogin(ctx, cleanNick(e.Nickname))
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
func seasonFor(ctx context.Context, pool *pgxpool.Pool, all []match) (string, error) {
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

func upsert(ctx context.Context, pool *pgxpool.Pool, st *store.Store, m match, seasonID string, ensureUser func(string) (string, error)) error {
	aID, err := ensureUser(m.A)
	if err != nil {
		return err
	}
	bID, err := ensureUser(m.B)
	if err != nil {
		return err
	}
	title := cleanNick(m.A) + " vs " + cleanNick(m.B)
	maps := []string{}
	for _, r := range m.Rounds {
		if name := mapName(ctx, pool, r.MapCode); name != "" {
			maps = append(maps, name)
		}
	}
	mapsJSON, _ := json.Marshal(maps)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var tid string
	err = tx.QueryRow(ctx, `SELECT id FROM tournaments WHERE ext_key = $1`, m.ExtKey).Scan(&tid)
	if err == pgx.ErrNoRows {
		err = tx.QueryRow(ctx, `
			INSERT INTO tournaments (title, mode, player_type, status, total_rounds, maps, starts_at, rating_multiplier, games, ext_key, season_id)
			VALUES ($1, '1x1', $2, 'finished', $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			title, m.PlayerType, len(m.Rounds), string(mapsJSON), m.Date, m.Mult, max(m.Games, 1), m.ExtKey, seasonID).Scan(&tid)
	} else if err == nil {
		_, err = tx.Exec(ctx, `
			UPDATE tournaments SET title=$2, player_type=$3, status='finished', total_rounds=$4, maps=$5, starts_at=$6,
			       rating_multiplier=$7, games=$8, season_id=$9, winner_participant_id=NULL, updated_at=now()
			WHERE id=$1`, tid, title, m.PlayerType, len(m.Rounds), string(mapsJSON), m.Date, m.Mult, max(m.Games, 1), seasonID)
		if err == nil {
			for _, q := range []string{`DELETE FROM participants WHERE tournament_id=$1`, `DELETE FROM rounds WHERE tournament_id=$1`,
				`DELETE FROM match_veto WHERE tournament_id=$1`} {
				if _, err = tx.Exec(ctx, q, tid); err != nil {
					break
				}
			}
		}
	}
	if err != nil {
		return err
	}

	pids := [2]string{}
	for i, side := range []struct {
		name, uid string
	}{{cleanNick(m.A), aID}, {cleanNick(m.B), bID}} {
		if err := tx.QueryRow(ctx, `
			INSERT INTO participants (tournament_id, kind, user_id, name, seed, mmr_delta) VALUES ($1, 'player', $2, $3, $4, $5) RETURNING id`,
			tid, side.uid, side.name, i+1, m.Pins[i]).Scan(&pids[i]); err != nil {
			return err
		}
	}
	if !m.Draw {
		winner := pids[0]
		if !m.WinA {
			winner = pids[1]
		}
		if _, err := tx.Exec(ctx, `UPDATE tournaments SET winner_participant_id=$2 WHERE id=$1`, tid, winner); err != nil {
			return err
		}
	}
	for _, r := range m.Rounds {
		var rid string
		var code *string
		if r.MapCode != "" {
			c := r.MapCode
			code = &c
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO rounds (tournament_id, number, map, map_code, status)
			VALUES ($1, $2, COALESCE((SELECT name FROM maps WHERE code = $3), ''), $3, 'finished') RETURNING id`,
			tid, r.Number, code).Scan(&rid); err != nil {
			return err
		}
		for side, pts := range r.Manual {
			if pts > 0 {
				if _, err := tx.Exec(ctx, `INSERT INTO round_entries (round_id, participant_id, points) VALUES ($1, $2, $3)`,
					rid, pids[side], pts); err != nil {
					return err
				}
			}
		}
		for _, t := range r.Tasks {
			taskID, err := catalogTask(ctx, tx, t)
			if err != nil {
				return err
			}
			var done *string
			if t.Completed {
				done = &pids[t.Side]
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO round_bonus_tasks (round_id, participant_id, task_id, completed_by) VALUES ($1, $2, $3, $4)
				ON CONFLICT (round_id, participant_id, task_id) DO NOTHING`, rid, pids[t.Side], taskID, done); err != nil {
				return err
			}
		}
	}
	sides := []string{"A", "B", "A", "B", "A", ""}
	for i, v := range m.Veto {
		side := ""
		if len(m.Veto) == len(sides) {
			side = sides[i]
		}
		var round *int
		if v.Round > 0 {
			r := v.Round
			round = &r
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO match_veto (tournament_id, seq, action, side, map_code, round_number) VALUES ($1, $2, $3, $4, $5, $6)`,
			tid, i+1, v.Action, side, v.MapCode, round); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, p := range pids {
		if _, err := st.RecomputeParticipantPoints(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func mapName(ctx context.Context, pool *pgxpool.Pool, code string) string {
	var name string
	_ = pool.QueryRow(ctx, `SELECT name FROM maps WHERE code = $1`, code).Scan(&name)
	return name
}

// catalogTask находит задание в каталоге по названию (и карте); задания, которых в каталоге нет,
// добавляются выключенными - только для истории матчей.
func catalogTask(ctx context.Context, tx pgx.Tx, t importTask) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id FROM catalog_tasks
		WHERE lower(name) = lower($1) AND category = $2 AND COALESCE(map_code, '') = $3
		ORDER BY (kind = 'pvp') DESC, active DESC LIMIT 1`, t.Name, t.Category, t.MapCode).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	var code *string
	if t.MapCode != "" {
		c := t.MapCode
		code = &c
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO catalog_tasks (name, text, points, value_type, kind, source, category, map_code, active, sort_order)
		VALUES ($1, $2, $3, 'fixed', 'pvp', 'official', $4, $5, false, 5000) RETURNING id`,
		t.Name, t.Text, t.Points, t.Category, code).Scan(&id)
	return id, err
}

// ---------- таблица ----------

func sheetMatches(path string) []match {
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
	var out []match
	for i := 0; i < len(list); i++ {
		r := list[i]
		mult := 1
		if i+1 < len(list) {
			nx := list[i+1]
			samePair := (sameNick(nx.a, r.a) && sameNick(nx.b, r.b)) || (sameNick(nx.a, r.b) && sameNick(nx.b, r.a))
			if nx.day == r.day && samePair && strings.EqualFold(nx.mp, r.mp) && sameNick(nx.win, r.win) {
				mult = 2
				i++
			}
		}
		date := sheetDate(r.day).Add(time.Duration(len(out)) * time.Second)
		pt := "pvp"
		if r.format == "pve" {
			pt = "pve"
		}
		out = append(out, match{
			ExtKey: fmt.Sprintf("s3sheet|%d|%s|%s|%s", r.n, r.day, strings.ToUpper(cleanNick(r.a)), strings.ToUpper(cleanNick(r.b))),
			Source: "таблица", Date: date, Day: date.In(moscow).Format("2006-01-02"),
			A: r.a, B: r.b, WinA: sameNick(r.win, r.a), Draw: r.win == "", Mult: mult, Games: mult, PlayerType: pt,
			Rounds: []importRound{{Number: 1, MapCode: mapCodeByName(r.mp)}},
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
			return time.Date(2026, time.Month(m), d, 15, 0, 0, 0, moscow)
		}
	}
	return time.Date(2026, 8, 17, 15, 0, 0, 0, moscow)
}

// ---------- arcarena ----------

type arenaParticipant struct {
	Nickname  string `json:"nickname"`
	MmrChange *int   `json:"mmrChange"`
}

type arenaMatch struct {
	ID               string     `json:"id"`
	Mode             string     `json:"mode"`
	RoundCount       int        `json:"roundCount"`
	RatingMultiplier int        `json:"ratingMultiplier"`
	Status           string     `json:"status"`
	StartedAt        *time.Time `json:"startedAt"`
	FinishedAt       *time.Time `json:"finishedAt"`
	CreatedAt        time.Time  `json:"createdAt"`
	Sides            []struct {
		Side         string             `json:"side"`
		Score        int                `json:"score"`
		Participants []arenaParticipant `json:"participants"`
	} `json:"sides"`
}

type arenaLive struct {
	Rounds []struct {
		RoundNumber int `json:"roundNumber"`
		Map         *struct {
			Code string `json:"code"`
		} `json:"map"`
		Score map[string]int `json:"score"`
		Tasks map[string][]struct {
			Name          string `json:"name"`
			Description   string `json:"description"`
			Points        int    `json:"points"`
			Type          string `json:"type"`
			Status        string `json:"status"`
			PointsAwarded int    `json:"pointsAwarded"`
		} `json:"tasks"`
	} `json:"rounds"`
}

type arenaVeto struct {
	Actions []struct {
		MapCode     string `json:"mapCode"`
		Action      string `json:"action"`
		RoundNumber *int   `json:"roundNumber"`
	} `json:"actions"`
}

func arenaMatches(dir string) []match {
	var idx struct {
		Matches []arenaMatch `json:"matches"`
	}
	readJSON(filepath.Join(dir, arenaIndex), &idx)
	var out []match
	for _, am := range idx.Matches {
		if am.Status != "finished" || am.Mode != "1v1" || len(am.Sides) != 2 ||
			len(am.Sides[0].Participants) != 1 || len(am.Sides[1].Participants) != 1 {
			continue
		}
		var live arenaLive
		var veto arenaVeto
		readJSON(filepath.Join(dir, "live_"+am.ID+".json"), &live)
		readJSON(filepath.Join(dir, "veto_"+am.ID+".json"), &veto)

		pa, pb := am.Sides[0].Participants[0], am.Sides[1].Participants[0]
		when := am.CreatedAt
		if am.StartedAt != nil {
			when = *am.StartedAt
		}
		m := match{
			ExtKey: "arena|" + am.ID, Source: "arcarena", Date: when, Day: when.In(moscow).Format("2006-01-02"),
			A: pa.Nickname, B: pb.Nickname, Mult: max(am.RatingMultiplier, 1), Games: 1, PlayerType: "pvp",
			Pins: [2]*int{pa.MmrChange, pb.MmrChange},
		}
		switch {
		case pa.MmrChange != nil && *pa.MmrChange > 0:
			m.WinA = true
		case pb.MmrChange != nil && *pb.MmrChange > 0:
			m.WinA = false
		case am.Sides[0].Score != am.Sides[1].Score:
			m.WinA = am.Sides[0].Score > am.Sides[1].Score
		default:
			m.Draw = true
		}
		for _, lr := range live.Rounds {
			r := importRound{Number: lr.RoundNumber}
			if lr.Map != nil {
				r.MapCode = arenaMaps[lr.Map.Code]
			}
			for side, key := range []string{"A", "B"} {
				taskPts := 0
				for _, t := range lr.Tasks[key] {
					it := importTask{Side: side, Name: t.Name, Text: t.Description, Points: t.Points, Completed: t.Status == "completed"}
					switch t.Type {
					case "universal_1":
						it.Category = "protocol"
					case "map":
						it.Category, it.MapCode = "task", r.MapCode
					default:
						it.Category = "task"
					}
					if it.Completed {
						taskPts += t.Points
					}
					r.Tasks = append(r.Tasks, it)
				}
				if manual := lr.Score[key] - taskPts; manual > 0 {
					r.Manual[side] = manual
				}
			}
			m.Rounds = append(m.Rounds, r)
		}
		if len(m.Rounds) == 0 {
			m.Rounds = []importRound{{Number: 1}}
		}
		for i, a := range veto.Actions {
			v := importVeto{Action: a.Action, MapCode: arenaMaps[a.MapCode]}
			if a.RoundNumber != nil {
				v.Round = *a.RoundNumber
			}
			// В порядке 3 сезона последний ход - оставшаяся карта, а не выбор стороны.
			if len(veto.Actions) == 6 && i == 5 {
				v.Action = "rest"
			}
			if v.MapCode != "" {
				m.Veto = append(m.Veto, v)
			}
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// merge склеивает источники: матч из таблицы, который есть и на arcarena (тот же день, те же
// игроки и победитель), берётся с arcarena.
func merge(sheet, arena []match) ([]match, int) {
	used := make([]bool, len(arena))
	var out []match
	dropped := 0
	for _, s := range sheet {
		dup := false
		for i, a := range arena {
			if used[i] || a.Day != s.Day {
				continue
			}
			samePair := (sameNick(a.A, s.A) && sameNick(a.B, s.B)) || (sameNick(a.A, s.B) && sameNick(a.B, s.A))
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
			if sameNick(winS, winA) {
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

func get(url string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (respect-import)")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return body, nil
}

func fetchSheet(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := get(fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s/gviz/tq?tqx=out:csv&headers=0&gid=%s", sheetID, sheetGID))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sheetFile), body, 0o644)
}

func fetchArena(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := get(arenaAPI + "/matches")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, arenaIndex), body, 0o644); err != nil {
		return err
	}
	table, err := get(arenaAPI + "/leaderboard")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, arenaTable), table, 0o644); err != nil {
		return err
	}
	var idx struct {
		Matches []arenaMatch `json:"matches"`
	}
	if err := json.Unmarshal(body, &idx); err != nil {
		return err
	}
	for _, m := range idx.Matches {
		if m.Status != "finished" {
			continue
		}
		for _, part := range []string{"live", "veto"} {
			b, err := get(arenaAPI + "/matches/" + m.ID + "/" + part)
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
