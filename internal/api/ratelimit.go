package api

import (
	"sync"
	"time"
)

// limiter — счётчик попыток с фиксированным окном (in-memory, потокобезопасный).
// Используется для троттлинга входа/регистрации: защита от перебора паролей
// и массового создания аккаунтов. Память самоочищается при росте.
type limiter struct {
	mu     sync.Mutex
	hits   map[string]*hitWindow
	max    int
	window time.Duration
}

type hitWindow struct {
	count int
	reset time.Time
}

// limiterMaxKeys — жёсткий потолок числа ключей в карте: даже при ротации ключей
// (разные логины/IP) память ограничена, а горячий путь не деградирует в O(n).
const limiterMaxKeys = 8192

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{hits: make(map[string]*hitWindow), max: max, window: window}
}

// blocked сообщает, исчерпан ли лимит по ключу в текущем окне (без инкремента).
func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.hits[key]
	if w == nil || time.Now().After(w.reset) {
		return false
	}
	return w.count >= l.max
}

// inc увеличивает счётчик по ключу, открывая новое окно при необходимости.
func (l *limiter) inc(key string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.hits[key]; w != nil && !now.After(w.reset) {
		w.count++
	} else {
		l.hits[key] = &hitWindow{count: 1, reset: now.Add(l.window)}
	}
	// Держим карту ограниченной: сначала чистим протухшие записи; если ключей всё ещё
	// слишком много (ротация живых ключей), эвиктим произвольные, пока не вернёмся под потолок.
	if len(l.hits) > limiterMaxKeys {
		for k, v := range l.hits {
			if now.After(v.reset) {
				delete(l.hits, k)
			}
		}
		for k := range l.hits {
			if len(l.hits) <= limiterMaxKeys {
				break
			}
			delete(l.hits, k)
		}
	}
}

// reset очищает счётчик по ключу (например, после успешного входа).
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
