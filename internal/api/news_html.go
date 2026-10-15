package api

import (
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// newsPolicy - разметка редактора новостей, как в Телеграме: жирный, курсив, подчёркнутый, зачёркнутый,
// моноширинный, спойлер, цитата и ссылки. Новость видят все, поэтому прочее срезается.
var newsPolicy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "strong", "em", "u", "s", "code", "blockquote")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^tg-spoiler$`)).OnElements("span")
	p.AllowAttrs("href").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto", "tg")
	// Свои страницы - относительной ссылкой: новость открывается на любом из доменов сайта, в той же вкладке.
	p.AllowRelativeURLs(true)
	p.RequireParseableURLs(true)
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}()

// newsHTML - текст новости, очищенный до разметки редактора; без видимого текста - пусто.
func newsHTML(raw string) string {
	html := strings.TrimSpace(newsPolicy.Sanitize(raw))
	if strings.TrimSpace(bluemonday.StrictPolicy().Sanitize(html)) == "" {
		return ""
	}
	return html
}
