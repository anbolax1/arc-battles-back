package api

import "testing"

func TestNewsHTML(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			"разметка редактора остаётся",
			`<p><strong>Ж</strong> <em>к</em> <u>п</u> <s>з</s> <code>м</code> <span class="tg-spoiler">с</span><br>строка</p><blockquote><p>ц</p></blockquote>`,
			`<p><strong>Ж</strong> <em>к</em> <u>п</u> <s>з</s> <code>м</code> <span class="tg-spoiler">с</span><br>строка</p><blockquote><p>ц</p></blockquote>`,
		},
		{
			"скрипты и обработчики срезаются",
			`<p onclick="x()">a<script>alert(1)</script><img src=x onerror=alert(1)><iframe src="https://e.com"></iframe></p>`,
			`<p>a</p>`,
		},
		{
			"ссылка открывается в новой вкладке без передачи страницы",
			`<a href="https://brouhub.ru/rules" target="_self" onclick="z()">правила</a>`,
			`<a href="https://brouhub.ru/rules" rel="nofollow noreferrer noopener" target="_blank">правила</a>`,
		},
		{"своя страница - относительной ссылкой в той же вкладке", `<a href="/belt">пояс</a>`, `<a href="/belt" rel="nofollow noreferrer">пояс</a>`},
		{"javascript-ссылка теряет адрес", `<a href="javascript:alert(1)">тык</a>`, `тык`},
		{"чужой класс и стиль у span срезаются", `<p><span class="x" style="color:red">т</span></p>`, `<p><span>т</span></p>`},
		{"пустой абзац - пустой текст", `<p></p><p> </p>`, ``},
	}
	for _, c := range cases {
		if got := newsHTML(c.in); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}
