package api

import (
	"net/http/httptest"
	"testing"

	"github.com/battle-for-respect/backend/internal/config"
)

func TestCookieDomain(t *testing.T) {
	s := &Server{Cfg: config.Config{CookieDomains: []string{"brouhub.ru", "goarc.pro"}}}
	cases := []struct{ host, want string }{
		{"brouhub.ru", "brouhub.ru"},
		{"api.brouhub.ru", "brouhub.ru"},
		{"goarc.pro", "goarc.pro"},
		{"GoArc.pro:443", "goarc.pro"},
		{"www.goarc.pro", "goarc.pro"},
		{"notbrouhub.ru", ""},
		{"155.212.133.147", ""},
		{"localhost:8080", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/api/auth/login", nil)
		r.Host = c.host
		if got := s.cookieDomain(r); got != c.want {
			t.Errorf("host %q: домен %q, ждали %q", c.host, got, c.want)
		}
	}
}
