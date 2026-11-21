package api

import (
	"context"
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// Правила раздачи заданий через HTTP: читает и меняет только организатор, пределы проверяются на сервере.
func TestTaskSettingsAPI(t *testing.T) {
	a := newQueueAPI(t)
	boss := a.actor("boss", models.RoleSuperadmin)
	mod := a.actor("mod", models.RoleModerator)
	prev, err := a.st.TaskDealSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.st.SaveTaskDealSettings(context.Background(), prev) })

	a.want(actor{}, "GET", "/task-settings", nil, 401)
	a.want(mod, "GET", "/task-settings", nil, 403)
	a.want(mod, "PUT", "/task-settings", map[string]any{"timedEnabled": false, "maxPoints": 5, "maxTasks": 5}, 403)

	a.want(boss, "PUT", "/task-settings", map[string]any{"timedEnabled": false, "maxPoints": 0, "maxTasks": 5}, 400)
	a.want(boss, "PUT", "/task-settings", map[string]any{"timedEnabled": false, "maxPoints": 5, "maxTasks": 31}, 400)
	a.want(boss, "PUT", "/task-settings", map[string]any{"maxPoints": 5, "maxTasks": 5}, 400)

	got := a.want(boss, "PUT", "/task-settings", map[string]any{"timedEnabled": false, "maxPoints": 8, "maxTasks": 3}, 200)
	if got["timedEnabled"] != false || got["maxPoints"] != float64(8) || got["maxTasks"] != float64(3) {
		t.Errorf("после сохранения: %v", got)
	}
	cfg, err := a.st.TaskDealSettings(context.Background())
	if err != nil || cfg != (store.TaskDealSettings{TimedEnabled: false, MaxPoints: 8, MaxTasks: 3}) {
		t.Errorf("в базе: %+v %v", cfg, err)
	}
}
