package store_test

import (
	"context"
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// playOutTaskQueue делает раунды матча идущими и выполняет открытые обычные задания, пока очередь не опустеет;
// отдаёт всё, что выдали стороне за раунд: так видно полный набор, а не только первое задание.
func playOutTaskQueue(ctx context.Context, t *testing.T, pool *pgxpool.Pool, st *store.Store, tournamentID string) []models.RoundBonusTask {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE rounds SET status = 'live' WHERE tournament_id = $1`, tournamentID); err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 50; step++ {
		list, err := st.ListTournamentBonusTasks(ctx, tournamentID)
		if err != nil {
			t.Fatal(err)
		}
		marked := false
		for _, d := range list {
			if d.Tag != models.TaskTagTimed && d.CompletedBy == nil {
				if _, err := st.MarkTask(ctx, d.ID, "owner", ""); err != nil {
					t.Fatal(err)
				}
				marked = true
			}
		}
		if !marked {
			return list
		}
	}
	t.Fatal("очередь заданий не кончается")
	return nil
}
