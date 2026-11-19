package syncer

import "sort"

// Hashes - хеши строк: таблица -> ключ строки -> хеш. Пустого хеша нет: отсутствующая строка просто не записана.
type Hashes map[string]map[string]string

// Op - одна строка, которую надо перенести. Local и Remote - хеши на сторонах (пусто - строки нет):
// по ним принимающая сторона проверяет, что строка не изменилась с момента сравнения.
type Op struct {
	Table  string
	Key    string
	Local  string
	Remote string
}

// deleteOnPush - при отправке строка удаляется на сайте; deleteOnPull - при загрузке удаляется в приложении.
func (o Op) deleteOnPush() bool { return o.Local == "" }
func (o Op) deleteOnPull() bool { return o.Remote == "" }

// Conflict - строку изменили обе стороны; Winner - чья версия осталась.
type Conflict struct {
	Table  string
	Key    string
	Winner Side
}

// Result - что делать в этом проходе.
type Result struct {
	Pull      []Op       // сайт -> приложение
	Push      []Op       // приложение -> сайт
	Same      []Op       // строки совпали: запомнить как общую точку
	Conflicts []Conflict // для журнала и показа организатору
}

// Plan сравнивает три состояния каждой строки: у приложения (local), на сайте (remote) и при прошлой
// синхронизации (base). Строка, которую поменяла одна сторона, переезжает на другую; если поменяли обе,
// остаётся версия владельца таблицы.
func Plan(tables []Table, local, remote, base Hashes) Result {
	var res Result
	for _, t := range tables {
		if t.InitOnly {
			continue
		}
		l, r, b := local[t.Name], remote[t.Name], base[t.Name]
		keys := map[string]struct{}{}
		for k := range l {
			keys[k] = struct{}{}
		}
		for k := range r {
			keys[k] = struct{}{}
		}
		for k := range b {
			keys[k] = struct{}{}
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			op := Op{Table: t.Name, Key: k, Local: l[k], Remote: r[k]}
			bh := b[k]
			switch {
			case op.Local == op.Remote:
				// Совпало (в том числе «обе стороны удалили»): общая точка обновляется.
				if op.Local != bh {
					res.Same = append(res.Same, op)
				}
			case bh == op.Remote:
				res.Push = append(res.Push, op) // сайт не менялся - менялось приложение
			case bh == op.Local:
				res.Pull = append(res.Pull, op) // приложение не менялось - менялся сайт
			default:
				res.Conflicts = append(res.Conflicts, Conflict{Table: t.Name, Key: k, Winner: t.Winner})
				if t.Winner == SideApp {
					res.Push = append(res.Push, op)
				} else {
					res.Pull = append(res.Pull, op)
				}
			}
		}
	}
	return res
}
