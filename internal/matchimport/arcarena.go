package matchimport

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/battle-for-respect/backend/internal/store"
)

const ArenaAPI = "https://arcarena.ru/api"

// Коды карт arcarena -> наши.
var arenaMaps = map[string]string{
	"stormy_flows":   "stormy_flows",
	"blue_gates":     "blue_gate",
	"buried_city":    "buried_city",
	"spaceport":      "spaceport",
	"battle_of_dam":  "dam",
	"stellar_montis": "stella_montis",
}

type ArenaParticipant struct {
	Nickname  string `json:"nickname"`
	MmrChange *int   `json:"mmrChange"`
}

// ArenaMatch - матч из списка /matches.
type ArenaMatch struct {
	ID               string     `json:"id"`
	Mode             string     `json:"mode"`
	RoundCount       int        `json:"roundCount"`
	RatingMultiplier int        `json:"ratingMultiplier"`
	Status           string     `json:"status"`
	StartedAt        *time.Time `json:"startedAt"`
	FinishedAt       *time.Time `json:"finishedAt"`
	CreatedAt        time.Time  `json:"createdAt"`
	Sides            []struct {
		Side         string             `json:"side"`
		Score        int                `json:"score"`
		Participants []ArenaParticipant `json:"participants"`
	} `json:"sides"`
}

// ArenaLive - раунды матча: карты, задания и счёт (/matches/{id}/live).
type ArenaLive struct {
	Rounds []struct {
		RoundNumber int `json:"roundNumber"`
		Map         *struct {
			Code string `json:"code"`
		} `json:"map"`
		Score map[string]int `json:"score"`
		Tasks map[string][]struct {
			Name          string `json:"name"`
			Description   string `json:"description"`
			Points        int    `json:"points"`
			Type          string `json:"type"`
			Status        string `json:"status"`
			PointsAwarded int    `json:"pointsAwarded"`
		} `json:"tasks"`
	} `json:"rounds"`
}

// ArenaVeto - пики и баны карт (/matches/{id}/veto).
type ArenaVeto struct {
	Actions []struct {
		MapCode     string `json:"mapCode"`
		Action      string `json:"action"`
		RoundNumber *int   `json:"roundNumber"`
	} `json:"actions"`
}

func ArenaKey(id string) string { return "arena|" + id }

// Importable - сыгранный матч 1×1, такие переносятся; отменённые и незаконченные - нет.
func (am ArenaMatch) Importable() bool {
	return am.Status == "finished" && am.Mode == "1v1" && len(am.Sides) == 2 &&
		len(am.Sides[0].Participants) == 1 && len(am.Sides[1].Participants) == 1
}

// FromArena собирает матч arcarena в общий вид; на матче, не прошедшем Importable, падает.
func FromArena(am ArenaMatch, live ArenaLive, veto ArenaVeto) Match {
	pa, pb := am.Sides[0].Participants[0], am.Sides[1].Participants[0]
	when := am.CreatedAt
	if am.StartedAt != nil {
		when = *am.StartedAt
	}
	m := Match{
		ExtKey: ArenaKey(am.ID), Source: "arcarena", Date: when, Day: when.In(Moscow).Format("2006-01-02"),
		A: pa.Nickname, B: pb.Nickname, Mult: max(am.RatingMultiplier, 1), Games: 1, PlayerType: "pvp",
		Pins: [2]*int{pa.MmrChange, pb.MmrChange},
	}
	switch {
	case pa.MmrChange != nil && *pa.MmrChange > 0:
		m.WinA = true
	case pb.MmrChange != nil && *pb.MmrChange > 0:
		m.WinA = false
	case am.Sides[0].Score != am.Sides[1].Score:
		m.WinA = am.Sides[0].Score > am.Sides[1].Score
	default:
		m.Draw = true
	}
	for _, lr := range live.Rounds {
		r := Round{Number: lr.RoundNumber}
		if lr.Map != nil {
			r.MapCode = arenaMaps[lr.Map.Code]
		}
		for side, key := range []string{"A", "B"} {
			taskPts := 0
			for _, t := range lr.Tasks[key] {
				it := Task{Side: side, Name: t.Name, Text: t.Description, Points: t.Points, Completed: t.Status == "completed"}
				switch t.Type {
				case "universal_1":
					it.Category = "protocol"
				case "map":
					it.Category, it.MapCode = "task", r.MapCode
				default:
					it.Category = "task"
				}
				if it.Completed {
					taskPts += t.Points
				}
				r.Tasks = append(r.Tasks, it)
			}
			if manual := lr.Score[key] - taskPts; manual > 0 {
				// Ручные очки на arcarena - ноки по 3 и задания соперника по 1, а таких заданий не больше двух.
				r.Manual[side], r.Knocks[side] = manual, manual/store.KnockPoints
			}
		}
		m.Rounds = append(m.Rounds, r)
	}
	if len(m.Rounds) == 0 {
		m.Rounds = []Round{{Number: 1}}
	}
	for i, a := range veto.Actions {
		v := Veto{Action: a.Action, MapCode: arenaMaps[a.MapCode]}
		if a.RoundNumber != nil {
			v.Round = *a.RoundNumber
		}
		// В порядке 3 сезона последний ход - оставшаяся карта, а не выбор стороны.
		if len(veto.Actions) == 6 && i == 5 {
			v.Action = "rest"
		}
		if v.MapCode != "" {
			m.Veto = append(m.Veto, v)
		}
	}
	return m
}

func Get(url string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (respect-import)")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return body, nil
}
