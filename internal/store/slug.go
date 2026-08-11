package store

import "strings"

// translit — кириллица → латиница для адресов пресетов оверлея: ссылку
// /overlay/<slug> вставляют в OBS руками, поэтому в ней только ASCII.
var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

const slugMaxLen = 48

// Slugify превращает произвольное название в адрес: транслит кириллицы, нижний
// регистр, всё остальное — дефис (без повторов и без дефисов по краям).
// Пустая строка на выходе — валидного адреса из названия не вышло (вызывающий
// подставляет фолбэк).
func Slugify(s string) string {
	var b strings.Builder
	dash := false // последним записан дефис — второй подряд не пишем
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if t, ok := translit[r]; ok {
			if t != "" {
				b.WriteString(t)
				dash = false
			}
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > slugMaxLen {
		out = strings.Trim(out[:slugMaxLen], "-")
	}
	return out
}
