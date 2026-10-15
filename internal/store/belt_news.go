package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
)

// BeltEvent - событие пояса лиги, о котором сайт пишет новость.
type BeltEvent struct {
	Key   string // по нему сайт помнит, что о событии уже знает
	Kind  string // take - обыграл носителя, pick - подобрал ничей пояс, drop - пояс выпал, honor - почётный носитель
	At    time.Time
	Reign int    // владение в истории пояса, о котором событие
	Match string // матч события; у выпавшего пояса матча нет
	Honor int    // honor: какой по счёту почётный носитель в истории лиги
}

// BeltEvents - события пояса по времени. Первый носитель лиги новостью не отмечается: с него история
// начинается, а событием становится уже смена хозяина.
func BeltEvents(b models.Belt) []BeltEvent {
	var out []BeltEvent
	total := map[string]int{}
	honors := 0
	for i, r := range b.Reigns {
		if r.How == "take" || r.How == "pick" {
			out = append(out, BeltEvent{Key: r.How + ":" + r.MatchID, Kind: r.How, At: r.From, Reign: i, Match: r.MatchID})
		}
		for _, d := range r.Defenses {
			total[r.Holder.UserID]++
			if total[r.Holder.UserID] == b.HonorAt {
				honors++
				out = append(out, BeltEvent{Key: "honor:" + r.Holder.UserID, Kind: "honor", At: d.At, Reign: i, Match: d.MatchID, Honor: honors})
			}
		}
		if r.End == "drop" && r.To != nil {
			out = append(out, BeltEvent{Key: "drop:" + r.MatchID, Kind: "drop", At: *r.To, Reign: i})
		}
	}
	return out
}

// KnownBeltEvents - события пояса, о которых сайт уже знает: новость о них написана или уже не нужна.
func (s *Store) KnownBeltEvents(ctx context.Context) (map[string]bool, error) {
	rows, err := s.Pool.Query(ctx, `SELECT key FROM belt_news`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out[key] = true
	}
	return out, rows.Err()
}

// SkipBeltEvents запоминает события без новости: они случились давно, и писать о них поздно.
func (s *Store) SkipBeltEvents(ctx context.Context, keys []string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO belt_news (key) SELECT unnest($1::text[]) ON CONFLICT (key) DO NOTHING`, keys)
	return err
}

// AddBeltNews публикует новость о событии пояса. Событие, о котором уже знают, новости не даёт: так две
// проверки, сошедшиеся по времени, не напишут её дважды.
func (s *Store) AddBeltNews(ctx context.Context, key, title, body string, art models.NewsBelt) (bool, error) {
	pic, err := json.Marshal(art)
	if err != nil {
		return false, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	ct, err := tx.Exec(ctx, `INSERT INTO belt_news (key) VALUES ($1) ON CONFLICT (key) DO NOTHING`, key)
	if err != nil {
		return false, err
	}
	if ct.RowsAffected() == 0 {
		return false, nil
	}
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO news (title, body, published_at, belt) VALUES ($1, $2, now(), $3::jsonb) RETURNING id`,
		title, body, string(pic)).Scan(&id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE belt_news SET news_id = $2 WHERE key = $1`, key, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// InLiveMatch - идёт ли у игрока обычный матч 1×1: пока он не кончился, его итог ещё может поменять пояс.
func (s *Store) InLiveMatch(ctx context.Context, userID string) (bool, error) {
	var live bool
	err := s.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM tournaments t JOIN participants p ON p.tournament_id = t.id
			WHERE t.status = 'live' AND t.mode = '1x1' AND t.format <> 'show' AND p.user_id = $1)`, userID).Scan(&live)
	return live, err
}
