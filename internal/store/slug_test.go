package store

import "testing"

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Табло", "tablo"},
		{"Основной оверлей", "osnovnoy-overley"},
		{"Счёт 2×2", "schet-2-2"},
		{"  Контракты / Протоколы  ", "kontrakty-protokoly"},
		{"Scene 1", "scene-1"},
		{"ъь", ""},          // только немые знаки — адреса не выходит
		{"---", ""},         // разделители по краям срезаются
		{"Щи & Борщ", "schi-borsch"},
	}
	for _, c := range cases {
		if got := Slugify(c.in); got != c.want {
			t.Errorf("Slugify(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

func TestSlugifyMaxLen(t *testing.T) {
	long := ""
	for range 40 {
		long += "аб"
	}
	got := Slugify(long)
	if len(got) > slugMaxLen {
		t.Errorf("длина %d > %d: %q", len(got), slugMaxLen, got)
	}
}
