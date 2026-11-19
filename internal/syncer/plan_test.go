package syncer

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var testTables = []Table{
	{Name: "tournaments", Winner: SideApp},
	{Name: "users", Winner: SideSite},
	{Name: "once", Winner: SideApp, InitOnly: true},
}

func h(table string, kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func ops(in []Op) []string {
	out := []string{}
	for _, o := range in {
		out = append(out, o.Table+"/"+o.Key)
	}
	return out
}

func TestPlan(t *testing.T) {
	cases := []struct {
		name                  string
		local, remote, base   Hashes
		pull, push, same, con []string
	}{
		{
			name:   "ничего не менялось",
			local:  Hashes{"tournaments": h("", "a", "1")},
			remote: Hashes{"tournaments": h("", "a", "1")},
			base:   Hashes{"tournaments": h("", "a", "1")},
		},
		{
			name:   "новая строка в приложении уходит на сайт",
			local:  Hashes{"tournaments": h("", "a", "1")},
			remote: Hashes{},
			base:   Hashes{},
			push:   []string{"tournaments/a"},
		},
		{
			name:   "новая строка на сайте приезжает в приложение",
			local:  Hashes{},
			remote: Hashes{"users": h("", "u", "1")},
			base:   Hashes{},
			pull:   []string{"users/u"},
		},
		{
			name:   "правка в приложении",
			local:  Hashes{"tournaments": h("", "a", "2")},
			remote: Hashes{"tournaments": h("", "a", "1")},
			base:   Hashes{"tournaments": h("", "a", "1")},
			push:   []string{"tournaments/a"},
		},
		{
			name:   "правка на сайте",
			local:  Hashes{"users": h("", "u", "1")},
			remote: Hashes{"users": h("", "u", "2")},
			base:   Hashes{"users": h("", "u", "1")},
			pull:   []string{"users/u"},
		},
		{
			name:   "удалили в приложении",
			local:  Hashes{},
			remote: Hashes{"tournaments": h("", "a", "1")},
			base:   Hashes{"tournaments": h("", "a", "1")},
			push:   []string{"tournaments/a"},
		},
		{
			name:   "удалили на сайте",
			local:  Hashes{"users": h("", "u", "1")},
			remote: Hashes{},
			base:   Hashes{"users": h("", "u", "1")},
			pull:   []string{"users/u"},
		},
		{
			name:   "обе стороны удалили",
			local:  Hashes{},
			remote: Hashes{},
			base:   Hashes{"users": h("", "u", "1")},
			same:   []string{"users/u"},
		},
		{
			name:   "обе стороны сделали одно и то же",
			local:  Hashes{"users": h("", "u", "2")},
			remote: Hashes{"users": h("", "u", "2")},
			base:   Hashes{"users": h("", "u", "1")},
			same:   []string{"users/u"},
		},
		{
			name:   "конфликт: матч правили на обеих сторонах - выигрывает приложение",
			local:  Hashes{"tournaments": h("", "a", "2")},
			remote: Hashes{"tournaments": h("", "a", "3")},
			base:   Hashes{"tournaments": h("", "a", "1")},
			push:   []string{"tournaments/a"},
			con:    []string{"tournaments/a"},
		},
		{
			name:   "конфликт: игрока правили на обеих сторонах - выигрывает сайт",
			local:  Hashes{"users": h("", "u", "2")},
			remote: Hashes{"users": h("", "u", "3")},
			base:   Hashes{"users": h("", "u", "1")},
			pull:   []string{"users/u"},
			con:    []string{"users/u"},
		},
		{
			name:   "конфликт с удалением: приложение удалило, сайт правил - матч удаляется (владелец приложение)",
			local:  Hashes{},
			remote: Hashes{"tournaments": h("", "a", "3")},
			base:   Hashes{"tournaments": h("", "a", "1")},
			push:   []string{"tournaments/a"},
			con:    []string{"tournaments/a"},
		},
		{
			name:   "строка без общей точки, но разная на сторонах - конфликт",
			local:  Hashes{"tournaments": h("", "a", "2")},
			remote: Hashes{"tournaments": h("", "a", "3")},
			base:   Hashes{},
			push:   []string{"tournaments/a"},
			con:    []string{"tournaments/a"},
		},
		{
			name:   "разовые таблицы в проходе не участвуют",
			local:  Hashes{"once": h("", "x", "1")},
			remote: Hashes{},
			base:   Hashes{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Plan(testTables, c.local, c.remote, c.base)
			conf := []string{}
			for _, x := range res.Conflicts {
				conf = append(conf, x.Table+"/"+x.Key)
			}
			want := func(s []string) []string {
				if s == nil {
					return []string{}
				}
				return s
			}
			if got := ops(res.Pull); !reflect.DeepEqual(got, want(c.pull)) {
				t.Errorf("pull = %v, want %v", got, want(c.pull))
			}
			if got := ops(res.Push); !reflect.DeepEqual(got, want(c.push)) {
				t.Errorf("push = %v, want %v", got, want(c.push))
			}
			if got := ops(res.Same); !reflect.DeepEqual(got, want(c.same)) {
				t.Errorf("same = %v, want %v", got, want(c.same))
			}
			if !reflect.DeepEqual(conf, want(c.con)) {
				t.Errorf("conflicts = %v, want %v", conf, want(c.con))
			}
		})
	}
}

func TestOpDeleteDirection(t *testing.T) {
	if !(Op{Local: "", Remote: "x"}).deleteOnPush() {
		t.Error("пустая локальная строка при отправке - это удаление на сайте")
	}
	if !(Op{Local: "x", Remote: ""}).deleteOnPull() {
		t.Error("пустая строка на сайте при загрузке - это удаление в приложении")
	}
	if (Op{Local: "x", Remote: "y"}).deleteOnPush() || (Op{Local: "x", Remote: "y"}).deleteOnPull() {
		t.Error("изменённая строка не удаление")
	}
}

func TestSafeMediaPath(t *testing.T) {
	dir := t.TempDir()
	// Что бы ни пришло в запросе, принятый путь обязан остаться внутри папки медиа.
	for _, in := range []string{"", "../x", "a/../../x", "/etc/passwd", `..\x`, `C:\Windows\x`, "news/a.png", "highlights/v.mp4"} {
		full, ok := safeMediaPath(dir, in)
		if ok && !strings.HasPrefix(full, filepath.Clean(dir)+string(filepath.Separator)) {
			t.Errorf("путь %q вышел из папки: %s", in, full)
		}
	}
	for _, bad := range []string{"", "../x", "a/../../x", `..\x`} {
		if _, ok := safeMediaPath(dir, bad); ok {
			t.Errorf("путь %q не должен приниматься", bad)
		}
	}
	if _, ok := safeMediaPath(dir, "news/a.png"); !ok {
		t.Error("обычный путь внутри папки должен приниматься")
	}
}
