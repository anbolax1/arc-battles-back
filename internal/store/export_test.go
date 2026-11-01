package store

// SetMapTasksEnabled включает раздачу заданий на карту на время теста; возвращает функцию отката.
func SetMapTasksEnabled(on bool) (restore func()) {
	prev := mapTasksEnabled
	mapTasksEnabled = on
	return func() { mapTasksEnabled = prev }
}
