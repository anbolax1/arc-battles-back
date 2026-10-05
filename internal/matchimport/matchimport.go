// Package matchimport переносит сыгранные матчи из внешних источников (таблица организатора,
// arcarena.ru) в базу сайта.
package matchimport

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ник в источнике -> логин аккаунта: игрок записан иначе или уже есть под другим логином.
var aliases = map[string]string{
	"1STW00D":  "Istwood",
	"JONNY_92": "J0NNY_92",
	"KUNAYO":   "KUNAY0",
	"MORES322": "M0RES322",
}

var Moscow = time.FixedZone("MSK", 3*3600)

var nickJunk = regexp.MustCompile(`[^\p{L}\p{N}_\-\.]+`)

// CleanNick убирает украшения вроде кубка чемпиона и приводит ник к логину аккаунта.
func CleanNick(n string) string {
	n = strings.TrimSpace(nickJunk.ReplaceAllString(strings.TrimSpace(n), " "))
	n = strings.Join(strings.Fields(n), " ")
	if a, ok := aliases[strings.ToUpper(n)]; ok {
		return a
	}
	return n
}

func SameNick(a, b string) bool { return strings.EqualFold(CleanNick(a), CleanNick(b)) }

// Round - раунд матча с картой, заданиями и ручными очками сторон.
type Round struct {
	Number  int
	MapCode string
	Tasks   []Task
	Manual  [2]int
}

type Task struct {
	Side      int // 0 - сторона A, 1 - B
	Name      string
	Text      string
	Points    int
	Category  string // task | protocol
	MapCode   string
	Completed bool
}

type Veto struct {
	Action  string
	MapCode string
	Round   int
}

// Match - матч из любого источника в общем виде.
type Match struct {
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
	Rounds     []Round
	Veto       []Veto
}

// Importer записывает матчи в базу; игроков без аккаунта заводит заглушками, войти в них нельзя.
type Importer struct {
	pool  *pgxpool.Pool
	st    *store.Store
	users map[string]string
}

func NewImporter(pool *pgxpool.Pool, st *store.Store) *Importer {
	return &Importer{pool: pool, st: st, users: map[string]string{}}
}

// Players - сколько разных игроков прошло через импорт.
func (im *Importer) Players() int { return len(im.users) }

func (im *Importer) EnsureUser(ctx context.Context, nick string) (string, error) {
	login := CleanNick(nick)
	key := strings.ToLower(login)
	if id, ok := im.users[key]; ok {
		return id, nil
	}
	if u, err := im.st.GetUserByLogin(ctx, login); err == nil {
		im.users[key] = u.ID
		return u.ID, nil
	}
	u, err := im.st.CreateUser(ctx, login, login, "!imported", models.RoleUser)
	if err != nil {
		return "", err
	}
	log.Printf("новый игрок %s - заведена заглушка", login)
	im.users[key] = u.ID
	return u.ID, nil
}

// Upsert заводит матч или перезаписывает уже перенесённый с тем же ключом. MMR не начисляет:
// его пересчитывают целиком после импорта.
func (im *Importer) Upsert(ctx context.Context, m Match, seasonID string) error {
	aID, err := im.EnsureUser(ctx, m.A)
	if err != nil {
		return err
	}
	bID, err := im.EnsureUser(ctx, m.B)
	if err != nil {
		return err
	}
	title := CleanNick(m.A) + " vs " + CleanNick(m.B)
	maps := []string{}
	for _, r := range m.Rounds {
		if name := im.mapName(ctx, r.MapCode); name != "" {
			maps = append(maps, name)
		}
	}
	mapsJSON, _ := json.Marshal(maps)

	tx, err := im.pool.Begin(ctx)
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
	}{{CleanNick(m.A), aID}, {CleanNick(m.B), bID}} {
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
		if _, err := im.st.RecomputeParticipantPoints(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func (im *Importer) mapName(ctx context.Context, code string) string {
	var name string
	_ = im.pool.QueryRow(ctx, `SELECT name FROM maps WHERE code = $1`, code).Scan(&name)
	return name
}

// catalogTask находит задание в каталоге по названию (и карте); задания, которых в каталоге нет,
// добавляются выключенными - только для истории матчей.
func catalogTask(ctx context.Context, tx pgx.Tx, t Task) (string, error) {
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
