package api

import "testing"

func TestTaskTagDefaultsByCategory(t *testing.T) {
	cases := []struct {
		body taskBody
		want string
	}{
		{taskBody{Text: "задание", Category: "task"}, "normal"},
		{taskBody{Text: "протокол", Category: "protocol"}, "timed"},
		{taskBody{Text: "задание на время", Category: "task", Tag: "timed"}, "timed"},
		{taskBody{Text: "протокол без спешки", Category: "protocol", Tag: "normal"}, "normal"},
		{taskBody{Text: "незнакомый тег", Category: "task", Tag: "hard"}, "normal"},
	}
	for _, c := range cases {
		got, _, ok := c.body.toModel()
		if !ok || got.Tag != c.want {
			t.Errorf("%q: тег %q, ждали %q", c.body.Text, got.Tag, c.want)
		}
	}
}
