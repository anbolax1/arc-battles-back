package store

import (
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
)

func TestBeltEvents(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 20, 0, 0, 0, patchZone) }
	at := func(d int) *time.Time { v := time.Date(2026, 10, d, 0, 0, 0, 0, patchZone); return &v }
	p := func(id string) models.BeltPlayer { return models.BeltPlayer{UserID: id, Login: id, DisplayName: id} }
	def := func(d int, match string) models.BeltDefense { return models.BeltDefense{At: day(d), MatchID: match} }
	b := models.Belt{HonorAt: 3, Reigns: []models.BeltReign{
		{Holder: p("a"), From: day(1), How: "first", MatchID: "m1", End: "lost", Defenses: []models.BeltDefense{def(1, "d1")}},
		{Holder: p("b"), From: day(2), How: "take", MatchID: "m2", To: at(9), End: "drop", Defenses: []models.BeltDefense{def(3, "d2"), def(4, "d3")}},
		{Holder: p("a"), From: day(10), How: "pick", MatchID: "m3", End: "lost", Defenses: []models.BeltDefense{def(11, "d4"), def(12, "d5")}},
		{Holder: p("b"), From: day(13), How: "take", MatchID: "m4", Defenses: []models.BeltDefense{def(14, "d6")}},
	}}

	got := BeltEvents(b)
	want := []BeltEvent{
		{Key: "take:m2", Kind: "take", At: day(2), Reign: 1, Match: "m2"},
		{Key: "drop:m2", Kind: "drop", At: *at(9), Reign: 1},
		{Key: "pick:m3", Kind: "pick", At: day(10), Reign: 2, Match: "m3"},
		{Key: "honor:a", Kind: "honor", At: day(12), Reign: 2, Match: "d5", Honor: 1},
		{Key: "take:m4", Kind: "take", At: day(13), Reign: 3, Match: "m4"},
		{Key: "honor:b", Kind: "honor", At: day(14), Reign: 3, Match: "d6", Honor: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("событий %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("событие %d:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
}
