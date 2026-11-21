package store

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/battle-for-respect/backend/internal/models"
)

// Пределы настроек раздачи: больше - уже не «задания раунда», а гонка за очками.
const (
	MaxTaskBudget = 30            // очков заданий на сторону в раунде
	MaxTaskCount  = MaxTaskBudget // заданий на сторону в раунде: меньше одного очка на задание не бывает
)

// ErrBadTaskSettings - настройки раздачи вне допустимых пределов.
var ErrBadTaskSettings = errors.New("очки - от 1 до 30, заданий - от 1 до 30")

const (
	taskTimedKey  = "task_timed_enabled"
	taskBudgetKey = "task_round_max_points"
	taskCountKey  = "task_round_max_tasks"
)

// TaskDealSettings - общие правила раздачи заданий: задание на время (вне лимитов) и лимиты обычных заданий
// на сторону в раунде.
type TaskDealSettings struct {
	TimedEnabled bool `json:"timedEnabled"`
	MaxPoints    int  `json:"maxPoints"` // на сколько очков выдаются обычные задания
	MaxTasks     int  `json:"maxTasks"`  // сколько обычных заданий выдаётся самое большее
}

// DefaultTaskDealSettings - правила, пока организатор не задал свои. Количество заданий не ограничено:
// набор ограничивает только лимит очков, и он сам подстраивается под любой лимит.
func DefaultTaskDealSettings() TaskDealSettings {
	return TaskDealSettings{TimedEnabled: true, MaxPoints: models.RoundTaskBudget, MaxTasks: MaxTaskCount}
}

func (c TaskDealSettings) valid() bool {
	return c.MaxPoints >= 1 && c.MaxPoints <= MaxTaskBudget && c.MaxTasks >= 1 && c.MaxTasks <= MaxTaskCount
}

// TaskDealSettings читает правила раздачи; незаданные или испорченные значения заменяются умолчанием.
func (s *Store) TaskDealSettings(ctx context.Context) (TaskDealSettings, error) {
	out := DefaultTaskDealSettings()
	rows, err := s.Pool.Query(ctx, `SELECT key, value FROM site_settings WHERE key = ANY($1)`,
		[]string{taskTimedKey, taskBudgetKey, taskCountKey})
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		n, convErr := strconv.Atoi(v)
		switch k {
		case taskTimedKey:
			out.TimedEnabled = v != "0"
		case taskBudgetKey:
			if convErr == nil && n >= 1 && n <= MaxTaskBudget {
				out.MaxPoints = n
			}
		case taskCountKey:
			if convErr == nil && n >= 1 && n <= MaxTaskCount {
				out.MaxTasks = n
			}
		}
	}
	return out, rows.Err()
}

// SaveTaskDealSettings сохраняет правила раздачи; действуют со следующей раздачи, выданное не меняется.
func (s *Store) SaveTaskDealSettings(ctx context.Context, c TaskDealSettings) error {
	if !c.valid() {
		return ErrBadTaskSettings
	}
	timed := "0"
	if c.TimedEnabled {
		timed = "1"
	}
	for k, v := range map[string]string{taskTimedKey: timed, taskBudgetKey: strconv.Itoa(c.MaxPoints), taskCountKey: strconv.Itoa(c.MaxTasks)} {
		if _, err := s.Pool.Exec(ctx, `
			INSERT INTO site_settings (key, value, updated_at) VALUES ($1, $2, now())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, k, v); err != nil {
			return err
		}
	}
	return nil
}

// chooseTaskSet собирает набор обычных заданий: не больше maxTasks штук на budget очков (очки = редкость).
// Сначала набор только из желаемых редкостей (ranks), потом с обычными заданиями для добора, потом из любых
// редкостей; все наборы, на которые хватает заданий в пуле, равновероятны. Если точно на budget не собрать,
// выдаётся что есть: сначала самые дорогие.
func chooseTaskSet(budget, maxTasks int, ranks []int32, pool []poolTask) []poolTask {
	byCost := map[int][]poolTask{}
	for _, c := range pool {
		cost := models.RarityPoints(c.Rarity)
		byCost[cost] = append(byCost[cost], c)
	}
	wanted := make([]int, 0, len(ranks)+1)
	for _, r := range ranks {
		wanted = append(wanted, int(r))
	}
	// Обычные для добора годятся, только если в наборе есть и желаемая редкость: иначе набор не по просьбе игрока.
	tiers := []struct {
		allowed []int
		needOne bool
	}{{wanted, false}, {append(slices.Clone(wanted), 1), true}, {[]int{1, 2, 3, 4, 5}, false}}
	for _, tier := range tiers {
		var fit [][]int
		for _, set := range taskSets(budget, maxTasks, tier.allowed) {
			if setFits(set, byCost) && (!tier.needOne || slices.ContainsFunc(set, func(c int) bool { return slices.Contains(wanted, c) })) {
				fit = append(fit, set)
			}
		}
		if tier.needOne {
			fit = mostWanted(fit, wanted)
		}
		if len(fit) > 0 {
			return takeSet(fit[rand.IntN(len(fit))], byCost)
		}
	}
	var set []int
	left := budget
	for cost := 5; cost >= 1; cost-- {
		for n := len(byCost[cost]); n > 0 && cost <= left && len(set) < maxTasks; n-- {
			set = append(set, cost)
			left -= cost
		}
	}
	return takeSet(set, byCost)
}

// mostWanted оставляет наборы, где больше всего очков набрано желаемыми редкостями: обычные только добирают
// до лимита, а не заменяют выбор игрока (необычные при 5 очках - 1+2+2, а не 1+1+1+2).
func mostWanted(sets [][]int, wanted []int) [][]int {
	score := func(set []int) int {
		sum := 0
		for _, c := range set {
			if slices.Contains(wanted, c) {
				sum += c
			}
		}
		return sum
	}
	best := -1
	for _, set := range sets {
		best = max(best, score(set))
	}
	var out [][]int
	for _, set := range sets {
		if score(set) == best {
			out = append(out, set)
		}
	}
	return out
}

// taskSets - все наборы стоимостей из allowed, дающие ровно budget не более чем из maxTasks заданий
// (по неубыванию, без перестановок).
func taskSets(budget, maxTasks int, allowed []int) [][]int {
	costs := slices.Clone(allowed)
	slices.Sort(costs)
	costs = slices.Compact(costs)
	var out [][]int
	var walk func(from, left int, cur []int)
	walk = func(from, left int, cur []int) {
		if left == 0 {
			out = append(out, slices.Clone(cur))
			return
		}
		if len(cur) == maxTasks {
			return
		}
		for i := from; i < len(costs); i++ {
			if costs[i] <= left {
				walk(i, left-costs[i], append(cur, costs[i]))
			}
		}
	}
	walk(0, budget, nil)
	return out
}

// setFits - хватает ли в пуле заданий нужных редкостей на весь набор.
func setFits(set []int, byCost map[int][]poolTask) bool {
	need := map[int]int{}
	for _, c := range set {
		need[c]++
	}
	for c, n := range need {
		if len(byCost[c]) < n {
			return false
		}
	}
	return true
}

// takeSet выбирает случайные задания под каждую стоимость набора.
func takeSet(set []int, byCost map[int][]poolTask) []poolTask {
	var out []poolTask
	for _, cost := range slices.Compact(slices.Sorted(slices.Values(set))) {
		n := 0
		for _, c := range set {
			if c == cost {
				n++
			}
		}
		pool := slices.Clone(byCost[cost])
		rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		out = append(out, pool[:n]...)
	}
	return out
}
