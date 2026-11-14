package store

import (
	"testing"
	"time"
)

func TestCooldownEnd(t *testing.T) {
	last := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) // матч завершился в 15:00 МСК
	at := func(min int) time.Time { return last.Add(time.Duration(min) * time.Minute) }
	ptr := func(t time.Time) *time.Time { return &t }
	n := func(v int) *int { return &v }
	cases := []struct {
		name string
		rule cooldownRule
		last time.Time
		def  int
		want time.Time
	}{
		{"общий кулдаун 3 часа: можно с 18:00", cooldownRule{}, last, 180, at(180)},
		{"матчей не было", cooldownRule{}, time.Time{}, 180, time.Time{}},
		{"общий выключен", cooldownRule{}, last, 0, time.Time{}},
		{"своя длительность важнее общей", cooldownRule{Minutes: n(60)}, last, 180, at(60)},
		{"своя длительность при выключенном общем", cooldownRule{Minutes: n(30)}, last, 0, at(30)},
		{"отключён у игрока", cooldownRule{Disabled: true, ManualUntil: ptr(at(500))}, last, 180, time.Time{}},
		{"снят после матча", cooldownRule{ClearedAt: ptr(at(10))}, last, 180, time.Time{}},
		{"снят до матча - новый матч даёт новый кулдаун", cooldownRule{ClearedAt: ptr(at(-10))}, last, 180, at(180)},
		{"поставлен вручную дольше матчевого", cooldownRule{ClearedAt: ptr(at(5)), ManualUntil: ptr(at(400))}, last, 180, at(400)},
		{"поставлен вручную, а потом сыгран матч", cooldownRule{ClearedAt: ptr(at(-60)), ManualUntil: ptr(at(30))}, last, 180, at(180)},
	}
	for _, c := range cases {
		if got := cooldownEnd(c.rule, c.last, c.def); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMskClock(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	if got := mskClock(now.Add(3*time.Hour+30*time.Minute), now); got != "18:30" {
		t.Errorf("сегодня: %q", got)
	}
	if got := mskClock(now.Add(14*time.Hour), now); got != "15.11 05:00" {
		t.Errorf("завтра: %q", got)
	}
}
