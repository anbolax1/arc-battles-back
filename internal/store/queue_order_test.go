package store

import (
	"slices"
	"testing"
	"time"
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

func qp(id, status string, today int, prio time.Time) queueItem {
	return queueItem{ID: id, Status: status, Today: today, Prio: prio}
}

// «Без очереди»: запись встаёт за вызванными и за теми, кто активировал жетон раньше; без жетона - остаётся позади.
func TestQueuePlacePriorityOrdersByActivationTime(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

	items := []queueItem{qi("x", QEntryPerforming, 0), qi("a", QEntryWaiting, 0), qi("b", QEntryWaiting, 0), qi("c", QEntryWaiting, 0)}
	items[3].Prio = at(1)
	items = qPlacePriority(items, "c")
	if want := []string{"x", "c", "a", "b"}; !slices.Equal(ids(items), want) {
		t.Fatalf("первый с жетоном: %v, want %v", ids(items), want)
	}

	// Второй активировал позже - встаёт за первым, но выше всех без жетона.
	items[3].Prio = at(5)
	items = qPlacePriority(items, "b")
	if want := []string{"x", "c", "b", "a"}; !slices.Equal(ids(items), want) {
		t.Fatalf("второй с жетоном: %v, want %v", ids(items), want)
	}

	// Тот, кто активировал раньше всех, но записан позже, обходит остальных.
	items[3].Prio = at(0)
	items = qPlacePriority(items, "a")
	if want := []string{"x", "a", "c", "b"}; !slices.Equal(ids(items), want) {
		t.Fatalf("активировал раньше всех: %v, want %v", ids(items), want)
	}
}

func TestQueuePlacePriorityLeavesOthersInPlace(t *testing.T) {
	now := time.Now()
	base := func() []queueItem {
		return []queueItem{qi("x", QEntryPerforming, 0), qi("a", QEntryWaiting, 0), qi("b", QEntryWaiting, 1), qi("c", QEntryWaiting, 1)}
	}
	cases := map[string]queueItem{
		"без жетона":        qp("c", QEntryWaiting, 0, time.Time{}),
		"уже играл сегодня": qp("c", QEntryWaiting, 1, now),
		"вызван на матч":    qp("c", QEntryInvited, 0, now),
	}
	for name, it := range cases {
		items := base()
		items[3] = it
		if got := ids(qPlacePriority(items, "c")); !slices.Equal(got, []string{"x", "a", "b", "c"}) {
			t.Errorf("%s: %v", name, got)
		}
	}
	if got := ids(qPlacePriority(base(), "нет-такой")); !slices.Equal(got, []string{"x", "a", "b", "c"}) {
		t.Errorf("неизвестная запись: %v", got)
	}
}

// Сыгравшие сегодня стоят по времени последнего матча: кто играл давнее, тот выше; при равенстве - кто раньше записался.
func TestQueuePlaceNewPlayedByLastMatch(t *testing.T) {
	t0 := time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC) // 09:00 МСК
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }
	played := func(id string, last, joined int) queueItem {
		return queueItem{ID: id, Status: QEntryWaiting, Today: 1, Last: at(last), Joined: at(joined)}
	}
	// Пример организатора: A играл в 09:00, B в 10:30, C в 12:15, D сегодня не играл.
	var items []queueItem
	for _, it := range []queueItem{played("C", 195, 300), played("A", 0, 301), {ID: "D", Status: QEntryWaiting, Joined: at(302)}, played("B", 90, 303)} {
		items = qPlaceNew(items, it)
	}
	if want := []string{"D", "A", "B", "C"}; !slices.Equal(ids(items), want) {
		t.Fatalf("по последнему матчу: %v, want %v", ids(items), want)
	}
	// Тот же матч, что у B, но записался позже - встаёт сразу за B.
	items = qPlaceNew(items, played("B2", 90, 304))
	if want := []string{"D", "A", "B", "B2", "C"}; !slices.Equal(ids(items), want) {
		t.Fatalf("равное время матча: %v, want %v", ids(items), want)
	}
}

// Выше вызванных на матч не встаёт никто, даже если вызванный уже играл сегодня.
func TestQueuePlaceNewNeverAboveInflight(t *testing.T) {
	items := []queueItem{qi("x", QEntryInvited, 1), qi("y", QEntryReady, 2)}
	if got, want := ids(qPlaceNew(items, qi("n", QEntryWaiting, 0))), []string{"x", "y", "n"}; !slices.Equal(got, want) {
		t.Errorf("новичок при вызванных: %v, want %v", got, want)
	}
}

// Сыгравший в другом месте встаёт среди сыгравших; вызванных не трогают.
func TestQueuePlaceAfterMatch(t *testing.T) {
	now := time.Now()
	items := []queueItem{qi("a", QEntryWaiting, 0), qi("b", QEntryWaiting, 0), {ID: "c", Status: QEntryWaiting, Today: 1, Last: now.Add(-time.Hour)}}
	moved := queueItem{ID: "a", Status: QEntryWaiting, Today: 1, Last: now}
	if got, want := ids(qPlaceAfterMatch(items, moved)), []string{"b", "c", "a"}; !slices.Equal(got, want) {
		t.Errorf("сыграл вне очереди: %v, want %v", got, want)
	}
	items[0].Status = QEntryInvited
	if got := ids(qPlaceAfterMatch(items, moved)); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("вызванного переставили: %v", got)
	}
}

// Запись на кулдауне не вызывают: следующей становится та, что за ней.
func TestQueueNextSkipsCooldown(t *testing.T) {
	items := []queueItem{{ID: "a", Status: QEntryWaiting, Cooldown: true}, qi("b", QEntrySkipped, 1), qi("c", QEntryWaiting, 0)}
	if next, ok := qNext(items); !ok || next.ID != "b" {
		t.Errorf("следующий = %v %v, want b", next.ID, ok)
	}
	if _, ok := qNext(items[:1]); ok {
		t.Error("единственный на кулдауне не должен вызываться")
	}
}
