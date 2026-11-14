package store

import "time"

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
	ID     string
	Status string
	Today  int // сколько матчей игрок уже сыграл сегодня: кто играл, стоит позади тех, кто нет
	// Prio - когда игрок активировал «Без очереди» (у команды - самый ранний из двоих). Нулевое значение - жетона нет.
	// Заполняется только у ждущих записей, сегодня ещё не игравших: сыграв, запись встаёт в общий порядок.
	Prio time.Time
	// Last - когда завершился последний матч (у команды - самый поздний из двух): среди сыгравших сегодня
	// выше тот, кто играл давнее. Joined - когда запись встала в очередь: решает при равном Last.
	Last, Joined time.Time
	// Cooldown - у кого-то из игроков идёт кулдаун: запись стоит на своём месте, но её не вызывают.
	Cooldown bool
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

// qFreshEnd - место сразу за последним, кто сегодня ещё не играл: новичок встаёт сюда, перед сыгравшими.
func qFreshEnd(items []queueItem) int {
	end := 0
	for i := range items {
		if items[i].Today == 0 {
			end = i + 1
		}
	}
	return end
}

// qPlaceNew - новая запись. Кто сегодня не играл, встаёт за всеми такими же (кто раньше записался, тот выше),
// но перед сыгравшими. Сыгравший встаёт среди сыгравших по времени последнего матча: кто играл давнее, тот выше,
// поэтому выйти и записаться заново, чтобы обойти очередь, нельзя. Выше вызванных на матч не встаёт никто.
func qPlaceNew(items []queueItem, it queueItem) []queueItem {
	from := max(qInflight(items), qFreshEnd(items))
	if it.Today == 0 {
		return qInsertAt(items, from, it)
	}
	for i := from; i < len(items); i++ {
		if items[i].Today > 0 && qPlayedEarlier(it, items[i]) {
			return qInsertAt(items, i, it)
		}
	}
	return append(items[:len(items):len(items)], it)
}

// qPlayedEarlier - a стоит выше b среди сыгравших сегодня: его последний матч раньше, при равенстве - раньше записался.
func qPlayedEarlier(a, b queueItem) bool {
	if !a.Last.Equal(b.Last) {
		return a.Last.Before(b.Last)
	}
	return a.Joined.Before(b.Joined)
}

// qPlaceAfterMatch - игрок записи только что сыграл матч в другом месте (другой очереди или вне очереди): запись
// встаёт среди сыгравших сегодня туда же, куда встала бы, записавшись сейчас. Вызванных на матч не трогает.
func qPlaceAfterMatch(items []queueItem, it queueItem) []queueItem {
	rest, old, ok := qWithout(items, it.ID)
	if !ok || !qEligible(old.Status) {
		return items
	}
	return qPlaceNew(rest, it)
}

// qPlaceSkipped - пропущенная запись уходит в конец своей группы: не выступавшая - за всеми не выступавшими,
// выступавшая - в самый конец. Так пропуск не даёт ей обойти тех, кто уже ждёт и ещё не выходил.
func qPlaceSkipped(items []queueItem, id string) []queueItem {
	rest, it, ok := qWithout(items, id)
	if !ok {
		return items
	}
	if it.Today == 0 {
		return qInsertAt(rest, qFreshEnd(rest), it)
	}
	return append(rest, it)
}

// qPrioIndex - куда встаёт запись с «Без очереди», активированным в момент at: за вызванными на матч и за теми,
// кто активировал жетон раньше. Остальные, у кого жетона нет, остаются позади.
func qPrioIndex(items []queueItem, at time.Time) int {
	i := qInflight(items)
	for i < len(items) && !items[i].Prio.IsZero() && !items[i].Prio.After(at) {
		i++
	}
	return i
}

// qPlacePriority - запись с «Без очереди» встаёт в начало очереди по времени активации: кто раньше, тот выше.
// Записи без жетона, уже сыгравшие сегодня или вызванные на матч остаются на месте.
func qPlacePriority(items []queueItem, id string) []queueItem {
	rest, it, ok := qWithout(items, id)
	if !ok || it.Prio.IsZero() || !qEligible(it.Status) || it.Today > 0 {
		return items
	}
	return qInsertAt(rest, qPrioIndex(rest, it.Prio), it)
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

// qNext - первая запись, которую можно вызвать: ждущая или пропущенная и без кулдауна у игроков.
func qNext(items []queueItem) (queueItem, bool) {
	for _, it := range items {
		if qEligible(it.Status) && !it.Cooldown {
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
