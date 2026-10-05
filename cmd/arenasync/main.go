// Команда arenasync переносит на сайт матчи, сыгранные на arcarena.ru, которых здесь ещё нет.
// Запускается по расписанию; уже перенесённые матчи не трогает, правки на arcarena после переноса
// не подхватывает.
//
//	arenasync [-dry-run]
//
// База - DATABASE_URL из окружения или из .env рабочей папки. Правила переноса:
//   - переносятся сыгранные матчи 1×1: идущий перенесётся после окончания, отменённый - никогда;
//   - изменение MMR сторон берётся как на arcarena, ×2 - один матч с удвоенным изменением;
//   - матч, уже заведённый на сайте (те же игроки и победитель в тот же день), не дублируется;
//   - матч попадает в сезон, который шёл в день матча;
//   - игрок без аккаунта заводится заглушкой, как при импорте сезона.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"

	"github.com/battle-for-respect/backend/internal/config"
	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/matchimport"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dry := flag.Bool("dry-run", false, "только показать, что будет перенесено")
	flag.Parse()

	cfg := config.Load()
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("подключение: %v", err)
	}
	defer pool.Close()
	st := store.New(pool)

	list, err := fetchList()
	if err != nil {
		log.Fatalf("список матчей arcarena: %v", err)
	}
	known, err := knownKeys(ctx, pool)
	if err != nil {
		log.Fatalf("перенесённые матчи: %v", err)
	}
	var fresh []matchimport.Match
	failed := 0
	for _, am := range list {
		if !am.Importable() || known[matchimport.ArenaKey(am.ID)] {
			continue
		}
		m, err := fetchMatch(am)
		if err != nil {
			log.Printf("матч %s: %v", am.ID, err)
			failed++
			continue
		}
		fresh = append(fresh, m)
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Date.Before(fresh[j].Date) })

	im := matchimport.NewImporter(pool, st)
	added, skipped := 0, 0
	for _, m := range fresh {
		label := describe(m)
		dup, err := playedHere(ctx, pool, st, m)
		if err != nil {
			log.Printf("%s: %v", label, err)
			failed++
			continue
		}
		if dup {
			log.Printf("%s - уже есть на сайте, пропускаю", label)
			skipped++
			continue
		}
		seasonID, season, err := seasonAt(ctx, pool, m)
		if err != nil {
			log.Printf("%s: сезон: %v", label, err)
			failed++
			continue
		}
		log.Printf("%s, сезон «%s»", label, season)
		if *dry {
			continue
		}
		if err := im.Upsert(ctx, m, seasonID); err != nil {
			log.Printf("%s: %v", label, err)
			failed++
			continue
		}
		added++
	}
	if added > 0 {
		if err := st.RecomputeAllMmr(ctx); err != nil {
			log.Fatalf("пересчёт MMR: %v", err)
		}
	}
	if *dry || added+skipped+failed > 0 {
		log.Printf("новых матчей: %d, перенесено: %d, уже были на сайте: %d, сбоев: %d", len(fresh), added, skipped, failed)
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func fetchList() ([]matchimport.ArenaMatch, error) {
	body, err := matchimport.Get(matchimport.ArenaAPI + "/matches")
	if err != nil {
		return nil, err
	}
	var idx struct {
		Matches []matchimport.ArenaMatch `json:"matches"`
	}
	err = json.Unmarshal(body, &idx)
	return idx.Matches, err
}

func fetchMatch(am matchimport.ArenaMatch) (matchimport.Match, error) {
	var live matchimport.ArenaLive
	var veto matchimport.ArenaVeto
	for part, dst := range map[string]any{"live": &live, "veto": &veto} {
		body, err := matchimport.Get(matchimport.ArenaAPI + "/matches/" + am.ID + "/" + part)
		if err != nil {
			return matchimport.Match{}, err
		}
		if err := json.Unmarshal(body, dst); err != nil {
			return matchimport.Match{}, err
		}
	}
	return matchimport.FromArena(am, live, veto), nil
}

func knownKeys(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT ext_key FROM tournaments WHERE ext_key LIKE 'arena|%'`)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(keys))
	for _, k := range keys {
		known[k] = true
	}
	return known, nil
}

// playedHere - матч уже сыгран на сайте: те же игроки, тот же победитель, тот же день по Москве.
func playedHere(ctx context.Context, pool *pgxpool.Pool, st *store.Store, m matchimport.Match) (bool, error) {
	var ids [2]string
	for i, nick := range []string{m.A, m.B} {
		u, err := st.GetUserByLogin(ctx, matchimport.CleanNick(nick))
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		ids[i] = u.ID
	}
	var winner *string
	if !m.Draw {
		winner = &ids[1]
		if m.WinA {
			winner = &ids[0]
		}
	}
	var dup bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM tournaments t
			JOIN participants pa ON pa.tournament_id = t.id AND pa.user_id = $1
			JOIN participants pb ON pb.tournament_id = t.id AND pb.user_id = $2
			LEFT JOIN participants w ON w.id = t.winner_participant_id
			WHERE t.mode = '1x1' AND t.status = 'finished' AND t.ext_key NOT LIKE 'arena|%'
			  AND (COALESCE(t.starts_at, t.created_at) AT TIME ZONE 'Europe/Moscow')::date = $3::date
			  AND w.user_id IS NOT DISTINCT FROM $4)`,
		ids[0], ids[1], m.Day, winner).Scan(&dup)
	return dup, err
}

// seasonAt - последний сезон, начавшийся до матча.
func seasonAt(ctx context.Context, pool *pgxpool.Pool, m matchimport.Match) (id, name string, err error) {
	err = pool.QueryRow(ctx, `
		SELECT id, name FROM seasons WHERE started_at <= $1 ORDER BY started_at DESC LIMIT 1`, m.Date).Scan(&id, &name)
	return id, name, err
}

func describe(m matchimport.Match) string {
	a, b := matchimport.CleanNick(m.A), matchimport.CleanNick(m.B)
	win := b
	if m.WinA {
		win = a
	}
	if m.Draw {
		win = "ничья"
	}
	return fmt.Sprintf("%s %s vs %s -> %s ×%d", m.Day, a, b, win, m.Mult)
}
