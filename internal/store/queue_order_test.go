package store

import (
	"slices"
	"testing"
)

func qi(id, status string, performed int) queueItem {
	return queueItem{ID: id, Status: status, Today: performed}
}

func ids(items []queueItem) []string { return qIDs(items) }

func TestQueuePlaceNewGoesBeforeToday(t *testing.T) {
	// a, b ещё не выступали; c и d уже выступали и стоят в конце.
	items := []queueItem{qi("a", QEntryWaiting, 0), qi("b", QEntryWaiting, 0), qi("c", QEntryWaiting, 1), qi("d", QEntryWaiting, 2)}
	got := ids(qPlaceNew(items, qi("n", QEntryWaiting, 0)))
	if want := []string{"a", "b", "n", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("новичок: %v, want %v", got, want)
	}
}

func TestQueuePlaceNewIntoEmptyAndAllToday(t *testing.T) {
	if got := ids(qPlaceNew(nil, qi("n", QEntryWaiting, 0))); !slices.Equal(got, []string{"n"}) {
		t.Errorf("пустая очередь: %v", got)
	}
	items := []queueItem{qi("c", QEntryWaiting, 1), qi("d", QEntryWaiting, 1)}
	if got, want := ids(qPlaceNew(items, qi("n", QEntryWaiting, 0))), []string{"n", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("все выступали: %v, want %v", got, want)
	}
}

func TestQueuePlaceBackIsLast(t *testing.T) {
	items := []queueItem{qi("a", QEntryPerforming, 0), qi("b", QEntryWaiting, 0), qi("c", QEntryWaiting, 1)}
	got := ids(qPlaceBack(items, "a"))
	if want := []string{"b", "c", "a"}; !slices.Equal(got, want) {
		t.Errorf("после выступления: %v, want %v", got, want)
	}
	// Неизвестная запись ничего не меняет.
	if got := ids(qPlaceBack(items, "zzz")); !slices.Equal(got, ids(items)) {
		t.Errorf("чужая запись: %v", got)
	}
}

func TestQueuePlaceSkipped(t *testing.T) {
	// Не выступавший: за всеми не выступавшими, но перед выступавшими.
	items := []queueItem{qi("a", QEntrySkipped, 0), qi("b", QEntryWaiting, 0), qi("c", QEntryWaiting, 1)}
	if got, want := ids(qPlaceSkipped(items, "a")), []string{"b", "a", "c"}; !slices.Equal(got, want) {
		t.Errorf("пропуск новичка: %v, want %v", got, want)
	}
	// Выступавший: в самый конец.
	items = []queueItem{qi("c", QEntrySkipped, 1), qi("a", QEntryWaiting, 0), qi("d", QEntryWaiting, 1)}
	if got, want := ids(qPlaceSkipped(items, "c")), []string{"a", "d", "c"}; !slices.Equal(got, want) {
		t.Errorf("пропуск выступавшего: %v, want %v", got, want)
	}
}

func TestQueuePlaceCalledStaysBehindInflight(t *testing.T) {
	items := []queueItem{qi("a", QEntryInvited, 0), qi("b", QEntryWaiting, 0), qi("c", QEntryWaiting, 0), qi("d", QEntryWaiting, 0)}
	got := ids(qPlaceCalled(items, "d"))
	if want := []string{"a", "d", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("вызов из конца: %v, want %v", got, want)
	}
}

func TestQueueMoveToRespectsInflightAndBounds(t *testing.T) {
	items := []queueItem{qi("a", QEntryReady, 0), qi("b", QEntryWaiting, 0), qi("c", QEntryWaiting, 0), qi("d", QEntryWaiting, 0)}
	// Ждущего нельзя поставить перед вызванным.
	if got, want := ids(qMoveTo(items, "d", 1)), []string{"a", "d", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("перед вызванным: %v, want %v", got, want)
	}
	if got, want := ids(qMoveTo(items, "b", 4)), []string{"a", "c", "d", "b"}; !slices.Equal(got, want) {
		t.Errorf("в конец: %v, want %v", got, want)
	}
	// Позиция за пределами прижимается к концу, без паники.
	if got, want := ids(qMoveTo(items, "b", 99)), []string{"a", "c", "d", "b"}; !slices.Equal(got, want) {
		t.Errorf("за пределами: %v, want %v", got, want)
	}
	if got := qMoveTo(items, "b", -5); len(got) != len(items) {
		t.Errorf("отрицательная позиция потеряла записи: %v", ids(got))
	}
}

func TestQueueNextSkipsInflight(t *testing.T) {
	items := []queueItem{qi("a", QEntryInvited, 0), qi("b", QEntryPerforming, 0), qi("c", QEntrySkipped, 0), qi("d", QEntryWaiting, 0)}
	next, ok := qNext(items)
	if !ok || next.ID != "c" {
		t.Errorf("следующий = %v %v, want c", next, ok)
	}
	if _, ok := qNext([]queueItem{qi("a", QEntryReady, 0)}); ok {
		t.Error("вызванных повторно вызывать нельзя")
	}
}

// Любая последовательность операций сохраняет множество записей: никто не теряется и не двоится.
func TestQueueOperationsKeepMembers(t *testing.T) {
	items := []queueItem{qi("a", QEntryWaiting, 0), qi("b", QEntryWaiting, 0), qi("c", QEntryWaiting, 1)}
	items = qPlaceNew(items, qi("n", QEntryWaiting, 0))
	items = qMoveTo(items, "n", 1)
	items = qPlaceSkipped(items, "a")
	items = qPlaceBack(items, "b")
	items = qPlaceCalled(items, "c")
	got := ids(items)
	slices.Sort(got)
	if want := []string{"a", "b", "c", "n"}; !slices.Equal(got, want) {
		t.Errorf("состав изменился: %v", got)
	}
}

func TestQueuePlaceNewReturningPerformerGoesLast(t *testing.T) {
	items := []queueItem{qi("a", QEntryWaiting, 0), qi("c", QEntryWaiting, 1)}
	got := ids(qPlaceNew(items, qi("n", QEntryWaiting, 1)))
	if want := []string{"a", "c", "n"}; !slices.Equal(got, want) {
		t.Errorf("вернувшийся выступавший: %v, want %v", got, want)
	}
	if len(items) != 2 {
		t.Errorf("исходный список изменён: %v", ids(items))
	}
}
