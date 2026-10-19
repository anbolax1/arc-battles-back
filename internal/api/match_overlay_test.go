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

func TestMatchOverlayKeepsTaskTags(t *testing.T) {
	tid, a := "t1", "pa"
	st := models.MatchState{
		Tournament:   models.Tournament{ID: tid, Participants: []models.Participant{{ID: a, Name: "EDEN", Seed: 1}}},
		Stage:        "round",
		CurrentRound: 1,
		Tasks: []models.RoundBonusTask{
			{RoundNumber: 1, ParticipantID: a, Category: "protocol", Tag: "timed", Text: "протокол", Points: 1},
			{RoundNumber: 1, ParticipantID: a, Category: "task", Tag: "timed", Text: "в первые 5 минут", Points: 2},
			{RoundNumber: 1, ParticipantID: a, Category: "task", Tag: "normal", Text: "обычное", Points: 2},
		},
	}
	ls := buildMatchLiveState(st, models.LiveState{TournamentID: &tid, CurrentParticipantID: &a})

	if len(ls.Complications) != 1 || ls.Complications[0].Tag != "timed" {
		t.Fatalf("тег протокола: %+v", ls.Complications)
	}
	if len(ls.BonusTasks) != 2 || ls.BonusTasks[0].Tag != "timed" || ls.BonusTasks[1].Tag != "normal" {
		t.Fatalf("теги заданий: %+v", ls.BonusTasks)
	}
}

func TestRaidSideStartsFromSideA(t *testing.T) {
	tid, other, a, b := "t1", "t0", "pa", "pb"
	parts := sidesBySeed(models.Tournament{Participants: []models.Participant{{ID: b, Seed: 2}, {ID: a, Seed: 1}}})

	if p := raidSide(parts, tid, models.LiveState{}); p == nil || p.ID != a {
		t.Fatalf("пока сторону не выбрали, в рейде сторона A: %+v", p)
	}
	if p := raidSide(parts, tid, models.LiveState{TournamentID: &tid, CurrentParticipantID: &b}); p == nil || p.ID != b {
		t.Fatalf("выбранная в пульте сторона: %+v", p)
	}
	if p := raidSide(parts, tid, models.LiveState{TournamentID: &other, CurrentParticipantID: &b}); p == nil || p.ID != a {
		t.Fatalf("выбор из прошлого матча не в счёт: %+v", p)
	}
}
