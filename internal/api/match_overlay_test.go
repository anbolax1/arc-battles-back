package api

import (
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
)

func TestMatchOverlayShowsProtocolOfRaidSide(t *testing.T) {
	tid, a, b := "t1", "pa", "pb"
	st := models.MatchState{
		Tournament: models.Tournament{
			ID:           tid,
			Participants: []models.Participant{{ID: a, Name: "EDEN", Seed: 1}, {ID: b, Name: "MAKAR", Seed: 2}},
		},
		Stage:        "round",
		CurrentRound: 1,
		Tasks: []models.RoundBonusTask{
			{RoundNumber: 1, ParticipantID: a, Category: "protocol", Text: "протокол EDEN", Points: 1},
			{RoundNumber: 1, ParticipantID: b, Category: "protocol", Text: "протокол MAKAR", Points: 1, CompletedBy: &b},
			{RoundNumber: 1, ParticipantID: a, Category: "task", Text: "задание EDEN", Points: 2},
			{RoundNumber: 2, ParticipantID: b, Category: "protocol", Text: "протокол MAKAR на второй раунд", Points: 1},
		},
	}
	ls := buildMatchLiveState(st, models.LiveState{TournamentID: &tid, CurrentParticipantID: &b})

	if len(ls.Complications) != 1 || ls.Complications[0].Text != "протокол MAKAR" || !ls.Complications[0].Done {
		t.Fatalf("протоколы в оверлее: %+v", ls.Complications)
	}
	if ls.Complication == nil || ls.Complication.Who != "MAKAR" {
		t.Fatalf("протокол стороны в рейде: %+v", ls.Complication)
	}
	// Задание соперника остаётся: за него сторона в рейде получает +1.
	if len(ls.BonusTasks) != 1 || !ls.BonusTasks[0].Opponent {
		t.Fatalf("задания в оверлее: %+v", ls.BonusTasks)
	}
}
