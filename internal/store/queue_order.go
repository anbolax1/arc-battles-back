package store

// Порядок очереди считается здесь, без базы: список записей по позициям превращается в новый список,
// а в БД он записывается разом (см. queueWriteOrder).

// Статусы записи в очереди.
const (
	QEntryWaiting    = "waiting"
	QEntryInvited    = "invited"
	QEntryReady      = "ready"
	QEntryPerforming = "performing"
	QEntrySkipped    = "skipped"
	QEntryLeft       = "left"
	QEntryRemoved    = "removed"
)

// queueItem - запись очереди для расчёта порядка.
type queueItem struct {
	ID        string
	Status    string
	Performed int // сколько раз уже выступала в этой сессии
}

// qInMatch - запись вызвана на матч или выступает: она стоит в начале списка.
func qInMatch(status string) bool {
	return status == QEntryInvited || status == QEntryReady || status == QEntryPerforming
}

// qEligible - записи, которых можно вызвать на матч.
func qEligible(status string) bool { return status == QEntryWaiting || status == QEntrySkipped }

func qIndex(items []queueItem, id string) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}

func qWithout(items []queueItem, id string) ([]queueItem, queueItem, bool) {
	i := qIndex(items, id)
	if i < 0 {
		return items, queueItem{}, false
	}
	out := make([]queueItem, 0, len(items)-1)
	out = append(out, items[:i]...)
	out = append(out, items[i+1:]...)
	return out, items[i], true
}

func qInsertAt(items []queueItem, at int, it queueItem) []queueItem {
	if at < 0 {
		at = 0
	}
	if at > len(items) {
		at = len(items)
	}
	out := make([]queueItem, 0, len(items)+1)
	out = append(out, items[:at]...)
	out = append(out, it)
	out = append(out, items[at:]...)
	return out
}

// qFreshEnd - место сразу за последним, кто ещё не выступал: новичок встаёт сюда, перед выступавшими.
func qFreshEnd(items []queueItem) int {
	end := 0
	for i := range items {
		if items[i].Performed == 0 {
			end = i + 1
		}
	}
	return end
}

// qPlaceNew - новая запись: за всеми, кто ещё не выступал, но перед уже выступавшими. Вернувшийся в очередь
// после выступления в этой сессии встаёт в самый конец: выйти и записаться заново, чтобы обойти очередь, нельзя.
func qPlaceNew(items []queueItem, it queueItem) []queueItem {
	if it.Performed > 0 {
		return append(items[:len(items):len(items)], it)
	}
	return qInsertAt(items, qFreshEnd(items), it)
}

// qPlaceBack - запись, закончившая выступление: в самый конец.
func qPlaceBack(items []queueItem, id string) []queueItem {
	rest, it, ok := qWithout(items, id)
	if !ok {
		return items
	}
	return append(rest, it)
}

// qPlaceSkipped - пропущенная запись уходит в конец своей группы: не выступавшая - за всеми не выступавшими,
// выступавшая - в самый конец. Так пропуск не даёт ей обойти тех, кто уже ждёт и ещё не выходил.
func qPlaceSkipped(items []queueItem, id string) []queueItem {
	rest, it, ok := qWithout(items, id)
	if !ok {
		return items
	}
	if it.Performed == 0 {
		return qInsertAt(rest, qFreshEnd(rest), it)
	}
	return append(rest, it)
}

// qInflight - сколько записей в начале списка уже вызвано на матч.
func qInflight(items []queueItem) int {
	n := 0
	for n < len(items) && qInMatch(items[n].Status) {
		n++
	}
	return n
}

// qPlaceCalled - вызванная запись встаёт сразу за уже вызванными: вызов из середины списка не оставляет
// выступающих в разных местах очереди.
func qPlaceCalled(items []queueItem, id string) []queueItem {
	rest, it, ok := qWithout(items, id)
	if !ok {
		return items
	}
	return qInsertAt(rest, qInflight(rest), it)
}

// qMoveTo - ручная перестановка: запись встаёт на позицию pos (с 1), остальные сдвигаются. Ждущая запись
// не встаёт перед вызванными на матч: они остаются в начале списка.
func qMoveTo(items []queueItem, id string, pos int) []queueItem {
	rest, it, ok := qWithout(items, id)
	if !ok {
		return items
	}
	at := pos - 1
	if floor := qInflight(rest); !qInMatch(it.Status) && at < floor {
		at = floor
	}
	return qInsertAt(rest, at, it)
}

// qNext - первая запись, которую можно вызвать: ждущая или пропущенная.
func qNext(items []queueItem) (queueItem, bool) {
	for _, it := range items {
		if qEligible(it.Status) {
			return it, true
		}
	}
	return queueItem{}, false
}

func qIDs(items []queueItem) []string {
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].ID
	}
	return out
}
