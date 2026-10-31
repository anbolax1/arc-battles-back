package api

import "testing"

func TestValidateSignupNick(t *testing.T) {
	cases := []struct {
		nick string
		ok   bool
	}{
		{"ZeRgRuSh", true},
		{"Abc", true},
		{"Abcdefghijkl", true},   // 12 символов
		{"Abcdefghijklm", false}, // 13
		{"Ab", false},
		{"1Abc", false}, // начинается с цифры
		{"Abc1", true},
		{"Jörgen", true},      // диакритика
		{"Игрок99", true},     // кириллица
		{"Ab c", false},       // пробел
		{"Ab_c", false},       // знак
		{"Ab-c", false},       // знак
		{"Ab😀c", false},       // не буква
		{"Привет12345", true}, // 11 символов, считаем символы, а не байты
	}
	for _, c := range cases {
		_, _, msg := validateSignup(signupBody{Password: "password123", Nick: c.nick, EmbarkID: "Nick#1234"})
		if (msg == "") != c.ok {
			t.Errorf("ник %q: ok=%v, сообщение %q", c.nick, c.ok, msg)
		}
	}
}

func TestValidateSignupEmbark(t *testing.T) {
	for embark, ok := range map[string]bool{
		"Nick#1234":  true,
		"Nick#123":   false,
		"Nick#12345": false,
		"Nick1234":   false,
		"":           false,
	} {
		_, _, msg := validateSignup(signupBody{Password: "password123", Nick: "Nickname", EmbarkID: embark})
		if (msg == "") != ok {
			t.Errorf("embark %q: ok=%v, сообщение %q", embark, ok, msg)
		}
	}
}

// Ник - единственное имя: он же и логин, пароль не должен на него походить.
func TestValidateSignupNickIsTheLogin(t *testing.T) {
	nick, embark, msg := validateSignup(signupBody{Password: "password123", Nick: "  Raider07 ", EmbarkID: " Raider#4321 "})
	if msg != "" || nick != "Raider07" || embark != "Raider#4321" {
		t.Errorf("очистка полей: %q %q %q", nick, embark, msg)
	}
	if _, _, msg := validateSignup(signupBody{Password: "RAIDER07", Nick: "Raider07", EmbarkID: "Nick#1234"}); msg != "пароль не должен совпадать с ником" {
		t.Errorf("пароль как ник: %q", msg)
	}
	if _, _, msg := validateSignup(signupBody{Password: "short", Nick: "Raider07", EmbarkID: "Nick#1234"}); msg == "" {
		t.Error("короткий пароль допущен")
	}
	if _, _, msg := validateSignup(signupBody{Password: "password123", Nick: "", EmbarkID: "Nick#1234"}); msg != "ник: от 3 до 12 символов" {
		t.Errorf("пустой ник: %q", msg)
	}
}
