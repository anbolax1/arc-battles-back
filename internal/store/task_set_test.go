package store

import (
	"fmt"
	"testing"

	"github.com/battle-for-respect/backend/internal/models"
)

func rarityPool(counts map[string]int) []poolTask {
	var out []poolTask
	for r, n := range counts {
		for i := 0; i < n; i++ {
			out = append(out, poolTask{ID: fmt.Sprintf("%s-%d", r, i), Rarity: r})
		}
	}
	return out
}

func setPoints(set []poolTask) int {
	sum := 0
	for _, c := range set {
		sum += models.RarityPoints(c.Rarity)
	}
	return sum
}

func TestTaskSets(t *testing.T) {
	// Все способы набрать 5 из 1..5: 5, 4+1, 3+2, 3+1+1, 2+2+1, 2+1+1+1, 1×5.
	if got := len(taskSets(5, 5, []int{1, 2, 3, 4, 5})); got != 7 {
		t.Errorf("наборов на 5: %d, ждали 7", got)
	}
	// Не больше двух заданий: 5, 4+1, 3+2.
	if got := len(taskSets(5, 2, []int{1, 2, 3, 4, 5})); got != 3 {
		t.Errorf("наборов на 5 из двух заданий: %d, ждали 3", got)
	}
}

func TestChooseTaskSet(t *testing.T) {
	full := rarityPool(map[string]int{"common": 8, "uncommon": 8, "rare": 8, "epic": 8, "legendary": 8})
	all := []int32{1, 2, 3, 4, 5}

	// По умолчанию - только обычные: пять обычных.
	for i := 0; i < 20; i++ {
		set := chooseTaskSet(5, 5, []int32{1}, full)
		if len(set) != 5 || setPoints(set) != 5 {
			t.Fatalf("умолчание: %v", set)
		}
	}
	// Любые редкости: всегда ровно 5 очков и не больше лимита заданий.
	for i := 0; i < 200; i++ {
		set := chooseTaskSet(5, 3, all, full)
		if setPoints(set) != 5 || len(set) > 3 {
			t.Fatalf("любые редкости, не больше 3 заданий: %v", set)
		}
	}
	// Только легендарные: одно задание на 5 очков.
	if set := chooseTaskSet(5, 5, []int32{5}, full); len(set) != 1 || set[0].Rarity != models.RarityLegendary {
		t.Errorf("легендарные: %v", set)
	}
	// Только редкие: три очка редкого + обычные для добора.
	if set := chooseTaskSet(5, 5, []int32{3}, full); len(set) != 3 || setPoints(set) != 5 {
		t.Errorf("редкие с добором: %v", set)
	}
	// Своё число очков: 7 = 5+2, 4+3, ... не больше двух заданий.
	for i := 0; i < 50; i++ {
		set := chooseTaskSet(7, 2, all, full)
		if setPoints(set) != 7 || len(set) != 2 {
			t.Fatalf("7 очков из двух заданий: %v", set)
		}
	}
	// Мало заданий в пуле: выдаётся что есть, ничего лишнего и без повторов.
	few := rarityPool(map[string]int{"common": 3, "rare": 1})
	set := chooseTaskSet(5, 5, []int32{1}, few)
	if setPoints(set) != 5 || len(set) != 3 {
		t.Errorf("мало заданий: %v (ждали редкое + 2 обычных)", set)
	}
	seen := map[string]bool{}
	for _, c := range set {
		if seen[c.ID] {
			t.Errorf("повтор задания %s", c.ID)
		}
		seen[c.ID] = true
	}
	if set := chooseTaskSet(5, 5, []int32{1}, nil); len(set) != 0 {
		t.Errorf("пустой пул: %v", set)
	}
}

// Лимит очков меняется без правок кода: набор всегда точно на лимит, а выбор игрока сохраняется,
// пока количество заданий не ограничено (по умолчанию).
func TestChooseTaskSetFollowsBudget(t *testing.T) {
	full := rarityPool(map[string]int{"common": 12, "uncommon": 12, "rare": 12, "epic": 12, "legendary": 12})
	for _, budget := range []int{3, 5, 10} {
		for _, only := range [][]int32{{1}, {2}, {3}, {4}, {5}, {1, 5}, {2, 3}} {
			for i := 0; i < 50; i++ {
				set := chooseTaskSet(budget, MaxTaskCount, only, full)
				if setPoints(set) != budget {
					t.Fatalf("лимит %d, выбор %v: набор %v на %d очков", budget, only, set, setPoints(set))
				}
			}
		}
	}
	// Только обычные при лимите 10: десять обычных, а не «любые редкости» вместо выбора игрока.
	if set := chooseTaskSet(10, MaxTaskCount, []int32{1}, full); len(set) != 10 {
		t.Errorf("обычные на 10 очков: %v", set)
	}
	// Примеры из правил на 5 очков: необычные - обычное + два необычных, эпические - обычное + эпическое.
	if set := chooseTaskSet(5, MaxTaskCount, []int32{2}, full); len(set) != 3 {
		t.Errorf("необычные на 5: %v", set)
	}
	if set := chooseTaskSet(5, MaxTaskCount, []int32{4}, full); len(set) != 2 {
		t.Errorf("эпические на 5: %v", set)
	}
}
