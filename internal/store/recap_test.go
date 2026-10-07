package store

import (
	"math"
	"testing"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
)

// Маленький сезон на три игрока: матч из таблицы, подробный матч с жетоном ×2 и камбэком, сверка рейтинга
// и ×2 из таблицы, после которого у двоих равный MMR.
func TestBuildRecap(t *testing.T) {
	at := func(day, hour int) time.Time { return time.Date(2026, 8, day, hour, 0, 0, 0, moscow) }
	in := recapInput{
		season: models.Season{ID: "s", Status: "active"},
		start:  1000,
		logins: map[string]string{"a": "ALPHA", "b": "BETA", "g": "GAMMA"},
		maps:   []models.MapInfo{{Code: "dam", Name: "Дамба"}, {Code: "spaceport", Name: "Космопорт"}},
		matches: []recapRaw{
			{id: "m1", at: at(17, 15), mult: 1, games: 1, winner: 0, users: [2]string{"a", "b"},
				rounds: []recapRawRound{{number: 1, mapCode: "dam", played: true}}},
			{id: "m2", at: at(18, 19), mult: 2, games: 1, winner: 1, users: [2]string{"a", "g"}, total: [2]int{5, 9},
				rounds: []recapRawRound{
					{number: 1, mapCode: "dam", played: true, entries: true, points: [2]int{5, 2}, knocks: [2]int{1, 0},
						tasks: []recapRawTask{{side: 1, name: "Т", category: "task", doneBy: 1}, {side: 0, name: "П", category: "protocol", doneBy: -1}}},
					{number: 2, mapCode: "spaceport", played: true, entries: true, points: [2]int{0, 7}, knocks: [2]int{0, 2}},
				},
				veto: []recapRawVeto{{"ban", "dam"}, {"pick", "spaceport"}}},
			{id: "m3", at: at(20, 15), mult: 2, games: 2, winner: 0, users: [2]string{"b", "g"},
				rounds: []recapRawRound{{number: 1, mapCode: "spaceport", played: true}}},
		},
		history: []recapHist{
			{user: "a", tournament: "m1", at: at(17, 15), delta: 50},
			{user: "b", tournament: "m1", at: at(17, 15), delta: -50},
			{user: "a", tournament: "m2", at: at(18, 19), delta: -108},
			{user: "g", tournament: "m2", at: at(18, 19), delta: 108},
			{user: "a", at: at(20, 12), delta: 10},
			{user: "b", tournament: "m3", at: at(20, 15), delta: 79},
			{user: "g", tournament: "m3", at: at(20, 15), delta: -79},
		},
	}
	r := buildRecap(in)

	if s := r.Summary; s.Matches != 3 || s.Games != 4 || s.X2 != 2 || s.Detailed != 1 || s.GameDays != 3 || s.Knocks != 3 || s.Players != 3 {
		t.Errorf("сводка: %+v", s)
	}
	// Равный MMR в таблице делят победы: у BETA их две за ×2 из таблицы.
	wantTable := []struct {
		login           string
		mmr, wins, loss int
	}{{"BETA", 1029, 2, 1}, {"GAMMA", 1029, 1, 2}, {"ALPHA", 952, 1, 1}}
	for i, w := range wantTable {
		p := r.Players[i]
		if p.Login != w.login || p.Mmr != w.mmr || p.Wins != w.wins || p.Losses != w.loss || p.Rank != i+1 {
			t.Errorf("место %d: %+v, ждали %+v", i+1, p, w)
		}
	}
	alpha := r.Players[2]
	if len(alpha.Curve) != 3 || alpha.Curve[1][2] != 0 || alpha.Curve[2][2] != 1 || alpha.Peak != 1050 || alpha.Low != 942 {
		t.Errorf("кривая ALPHA со сверкой: %+v пик %d дно %d", alpha.Curve, alpha.Peak, alpha.Low)
	}
	if len(r.Days) != 4 || r.Days[2].Matches != 0 {
		t.Fatalf("дни: %+v", r.Days)
	}
	// На ничьей по MMR лидер не меняется: GAMMA остаётся первой и 20-го.
	gamma, beta := 1, 0
	if got := r.Leaders; len(got) != 2 || got[0].Player != 2 || got[1].Player != gamma || got[1].From != 1 || got[1].To != 3 {
		t.Errorf("лидеры: %+v", got)
	}
	if r.Days[3].Order[0] != gamma || r.Days[3].Order[1] != beta {
		t.Errorf("порядок на 20-е: %v", r.Days[3].Order)
	}
	if r.Decisive != 1 {
		t.Errorf("решающий матч: %d", r.Decisive)
	}
	if len(r.Comebacks) != 1 || r.Comebacks[0].Match != 1 || r.Comebacks[0].Round1 != [2]int{2, 5} {
		t.Errorf("камбэки: %+v", r.Comebacks)
	}
	if r.Favorites != [2]int{0, 2} || len(r.Upsets) != 2 || r.Upsets[0].Match != 2 ||
		math.Abs(r.Upsets[0].Chance-expectedScore(950, 1108)) > 1e-9 {
		t.Errorf("фавориты %v, неожиданные победы %+v", r.Favorites, r.Upsets)
	}
	if r.Tasks.All != [2]int{1, 2} || r.Tasks.General != [2]int{1, 1} || r.Tasks.Protocols != [2]int{0, 1} ||
		len(r.Tasks.Doers) != 1 || r.Tasks.Doers[0].Player != gamma {
		t.Errorf("задания: %+v", r.Tasks)
	}
	if r.Maps[0].Ban != 1 || r.Maps[1].Pick != 1 || r.Maps[0].Rounds != 1 || r.Maps[1].Rounds != 1 || r.Summary.Vetoed != 1 {
		t.Errorf("карты: %+v", r.Maps)
	}
	if r.BestKnock == nil || r.BestKnock.Player != gamma || r.BestKnock.Knocks != 2 {
		t.Errorf("рекорд ноков: %+v", r.BestKnock)
	}
	if len(r.Rounds) != 2 || r.Rounds[0].Points != 7 || r.Rounds[1].Knocks != 2 {
		t.Errorf("раунды: %+v", r.Rounds)
	}
	if r.Weekday[0] != 1 || r.Weekday[1] != 1 || r.Weekday[3] != 1 || r.Hours[19] != 1 {
		t.Errorf("дни недели %v, часы %v", r.Weekday, r.Hours)
	}
}
