package api

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// beltNewsFresh - новость пишется только о событии не старше этого: давние события и правки старой
// истории пояса новостей не дают.
const beltNewsFresh = 48 * time.Hour

var beltMoscow = time.FixedZone("MSK", 3*3600)

var ruMonths = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}

// SyncBeltNews пишет новости о свежих событиях пояса лиги: кто забрал пояс, у кого он выпал, кто стал
// почётным носителем. Сбой только попадает в журнал: проверка повторится через минуту.
func (s *Server) SyncBeltNews(ctx context.Context) {
	b, err := s.Store.Belt(ctx)
	if err != nil {
		log.Printf("новости о поясе: %v", err)
		return
	}
	known, err := s.Store.KnownBeltEvents(ctx)
	if err != nil {
		log.Printf("новости о поясе: %v", err)
		return
	}
	now := time.Now()
	var stale []string
	for _, e := range store.BeltEvents(b) {
		if known[e.Key] {
			continue
		}
		if now.Sub(e.At) > beltNewsFresh {
			stale = append(stale, e.Key)
			continue
		}
		// Пояс выпал в полночь, а матч носителя, начатый накануне, ещё идёт: с его итогом пояс останется.
		if e.Kind == "drop" {
			if live, err := s.Store.InLiveMatch(ctx, b.Reigns[e.Reign].Holder.UserID); err != nil || live {
				continue
			}
		}
		n := beltNews(b, e)
		if _, err := s.Store.AddBeltNews(ctx, e.Key, n.title, newsHTML(n.body), n.art); err != nil {
			log.Printf("новость о поясе %s: %v", e.Key, err)
		}
	}
	if len(stale) > 0 {
		if err := s.Store.SkipBeltEvents(ctx, stale); err != nil {
			log.Printf("новости о поясе: %v", err)
		}
	}
}

type beltNewsItem struct {
	title, body string
	art         models.NewsBelt
}

// beltNews - заголовок, текст и рисунок новости о событии пояса. Ссылки на страницы сайта относительные:
// новость открывается на любом из его доменов.
func beltNews(b models.Belt, e store.BeltEvent) beltNewsItem {
	r := b.Reigns[e.Reign]
	x := beltName(r.Holder)
	var prev *models.BeltReign
	if e.Reign > 0 {
		prev = &b.Reigns[e.Reign-1]
	}
	gameDays := ruCount(b.RustLimit, "игровой день", "игровых дня", "игровых дней")
	out := beltNewsItem{art: models.NewsBelt{Name: x}}
	var p []string

	switch e.Kind {
	case "take":
		out.title = x + " забрал пояс лиги"
		p = append(p, fmt.Sprintf("%s обыграл носителя %s%s и забрал пояс лиги.",
			beltLink(r.Holder), beltLink(beltOpponent(r)), beltScore(r.Score)))
		if prev != nil && prev.To != nil {
			p = append(p, fmt.Sprintf("%s держал пояс %s и %s.", html.EscapeString(beltName(prev.Holder)),
				ruCount(beltDays(prev.From, *prev.To), "день", "дня", "дней"), beltDefended(len(prev.Defenses))))
		}
		p = append(p, beltNewcomer(r, html.EscapeString(x)))
	case "pick":
		out.title = x + " подобрал ничейный пояс"
		p = append(p, fmt.Sprintf("%s обыграл %s%s и подобрал ничейный пояс лиги.",
			beltLink(r.Holder), beltLink(beltOpponent(r)), beltScore(r.Score)))
		if prev != nil && prev.End == "drop" && prev.To != nil {
			p = append(p, fmt.Sprintf("Пояс был ничей с %s: %s %s подряд не выходил на регулярный матч, и пояс выпал.",
				ruDate(*prev.To), html.EscapeString(beltName(prev.Holder)), gameDays))
		}
		p = append(p, beltNewcomer(r, html.EscapeString(x)))
	case "drop":
		out.title = "Пояс лиги выпал у " + x
		out.art.Vacant = true
		p = append(p, fmt.Sprintf("%s %s подряд не выходил на регулярный матч, и пояс лиги выпал. Пояс был у него с %s: %s, %s.",
			beltLink(r.Holder), gameDays, ruDate(r.From), ruCount(beltDays(r.From, e.At), "день", "дня", "дней"),
			beltDefenses(len(r.Defenses))))
		if e.Reign == len(b.Reigns)-1 && b.Holder == nil {
			if len(b.Leaders) > 0 {
				names := make([]string, len(b.Leaders))
				for i, l := range b.Leaders {
					names[i] = beltLink(l)
				}
				p = append(p, "Теперь пояс ничей. Его разыграет ближайший регулярный матч лидера таблицы сезона: "+strings.Join(names, " или ")+
					". Выиграет лидер — пояс его, проиграет — пояс у того, кто его обыграл.")
			} else {
				p = append(p, "Теперь пояс ничей: его разыграет первый регулярный матч сезона.")
			}
		}
		p = append(p, "Игровой день — день, когда в лиге был хотя бы один регулярный матч.")
	case "honor":
		out.title = x + " — почётный носитель пояса"
		out.art.Stars = b.HonorAt
		p = append(p, fmt.Sprintf("%s защитил пояс лиги в %d-й раз и получил тег «Почётный носитель».", beltLink(r.Holder), b.HonorAt))
		if e.Honor == 1 {
			p = append(p, "Это первый почётный носитель в истории лиги.")
		} else {
			p = append(p, fmt.Sprintf("Это %d-й почётный носитель в истории лиги.", e.Honor))
		}
		p = append(p, "Защиты из разных владений складываются, а тег остаётся навсегда.")
	}

	links := `<a href="/belt">Пояс лиги: правила и вся история</a>`
	if e.Match != "" {
		links = fmt.Sprintf(`<a href="/tournament/%s">Разбор матча</a> · `, url.PathEscape(e.Match)) + links
	}
	p = append(p, links)
	out.body = "<p>" + strings.Join(p, "</p><p>") + "</p>"
	return out
}

func beltName(p models.BeltPlayer) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Login
}

// beltLink - ник со ссылкой на профиль игрока.
func beltLink(p models.BeltPlayer) string {
	return fmt.Sprintf(`<a href="/profile/%s">%s</a>`, url.PathEscape(p.Login), html.EscapeString(beltName(p)))
}

func beltOpponent(r models.BeltReign) models.BeltPlayer {
	if r.Opponent != nil {
		return *r.Opponent
	}
	return models.BeltPlayer{}
}

// beltScore - счёт матча со стороны нового носителя; у матчей из таблицы организатора счёта нет.
func beltScore(score []int) string {
	if len(score) != 2 {
		return ""
	}
	return fmt.Sprintf(" со счётом %d:%d", score[0], score[1])
}

// beltNewcomer - каким по счёту носителем стал игрок или в который раз берёт пояс.
func beltNewcomer(r models.BeltReign, name string) string {
	if r.Time > 1 {
		return fmt.Sprintf("%s берёт пояс уже %d-й раз.", name, r.Time)
	}
	return fmt.Sprintf("%s — %d-й носитель в истории лиги.", name, r.Newcomer)
}

func beltDefenses(n int) string {
	if n == 0 {
		return "ни одной защиты"
	}
	return ruCount(n, "защита", "защиты", "защит")
}

func beltDefended(n int) string {
	if n == 0 {
		return "ни разу не отбился"
	}
	return "отбился " + ruCount(n, "раз", "раза", "раз")
}

// beltDays - сколько дней по Москве игрок держал пояс; отданный в день взятия - один день.
func beltDays(from, to time.Time) int {
	day := func(t time.Time) time.Time {
		m := t.In(beltMoscow)
		return time.Date(m.Year(), m.Month(), m.Day(), 0, 0, 0, 0, beltMoscow)
	}
	return max(1, int(day(to).Sub(day(from)).Hours()/24))
}

// ruDate - дата по Москве: «7 октября».
func ruDate(t time.Time) string {
	m := t.In(beltMoscow)
	return fmt.Sprintf("%d %s", m.Day(), ruMonths[m.Month()-1])
}

// ruCount - число со словом в нужной форме: 1 день, 3 дня, 5 дней.
func ruCount(n int, one, few, many string) string {
	word := many
	switch k := n % 100; {
	case k >= 11 && k <= 14:
	case k%10 == 1:
		word = one
	case k%10 >= 2 && k%10 <= 4:
		word = few
	}
	return fmt.Sprintf("%d %s", n, word)
}
