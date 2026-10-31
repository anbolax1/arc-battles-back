package models

import "time"

// QueueSession - сессия очереди выступлений (один стрим). State: open | closed | paused | finished.
type QueueSession struct {
	ID                string     `json:"id"`
	Title             string     `json:"title"`
	Format            string     `json:"format"` // 1x1 | 2x2
	State             string     `json:"state"`
	RegistrationOpen  bool       `json:"registrationOpen"`
	Paused            bool       `json:"paused"`
	ConfirmTimeoutSec int        `json:"confirmTimeoutSec"`
	Rev               int64      `json:"rev"`
	CreatedAt         time.Time  `json:"createdAt"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	CreatedBy         string     `json:"createdBy,omitempty"` // организатор, открывший сессию
	Waiting           int        `json:"waiting"`             // сколько записей в очереди
	Played            int        `json:"played"`              // сколько матчей уже сыграно
}

// QueueMember - игрок записи; у одиночной записи он один, у команды - двое.
type QueueMember struct {
	UserID      string     `json:"userId"`
	Login       string     `json:"login"`
	DisplayName string     `json:"displayName"`
	AvatarURL   string     `json:"avatarUrl"`
	Hours       *int       `json:"hours,omitempty"`  // подтверждённые часы в игре
	Knocks      *int       `json:"knocks,omitempty"` // подтверждённые ноки за всё время
	Today       int        `json:"todayMatches"`     // матчей, сыгранных сегодня
	Ready       bool       `json:"ready"`            // подтвердил готовность на вызов
	ReadyAt     *time.Time `json:"readyAt,omitempty"`
}

// QueueEntry - место в очереди: игрок или команда 2×2. Status: waiting | invited | ready | performing | skipped.
type QueueEntry struct {
	ID               string        `json:"id"`
	Position         int           `json:"position"`
	Status           string        `json:"status"`
	IsNext           bool          `json:"isNext"` // его вызовут следующим
	Name             string        `json:"name"`   // ник игрока или название команды
	TeamKey          string        `json:"teamKey,omitempty"`
	Members          []QueueMember `json:"members"`
	Mmr              int           `json:"mmr"`
	JoinedAt         time.Time     `json:"joinedAt"`
	TodayMatches     int           `json:"todayMatches"` // у команды - у самого игравшего из двух
	PerformedCount   int           `json:"performedCount"`
	SkippedCount     int           `json:"skippedCount"`
	InvitedAt        *time.Time    `json:"invitedAt,omitempty"`
	InviteExpiresAt  *time.Time    `json:"inviteExpiresAt,omitempty"`
	AddedByOrganizer bool          `json:"addedByOrganizer,omitempty"`
	Priority         bool          `json:"priority,omitempty"` // сегодня действует жетон «Без очереди»
}

// QueueMatchSide - сторона матча очереди; Name и Members зафиксированы в момент вызова.
type QueueMatchSide struct {
	Side    int           `json:"side"`
	EntryID string        `json:"entryId"`
	Name    string        `json:"name"`
	Mmr     int           `json:"mmr"`
	Members []QueueMember `json:"members"`
	Status  string        `json:"status,omitempty"` // статус записи, пока матч не закончен
}

// QueueMatch - матч очереди. Status: preparing | live | finished | cancelled.
type QueueMatch struct {
	ID           string           `json:"id"`
	Number       int              `json:"number"`
	TournamentID string           `json:"tournamentId,omitempty"` // матч сайта, созданный для этой пары
	Status       string           `json:"status"`
	CreatedAt    time.Time        `json:"createdAt"`
	StartedAt    *time.Time       `json:"startedAt,omitempty"`
	FinishedAt   *time.Time       `json:"finishedAt,omitempty"`
	Sides        []QueueMatchSide `json:"sides"`
}

// QueueEvent - строка журнала сессии: вызов, пропуск, действие организатора.
type QueueEvent struct {
	ID    int64          `json:"id"`
	At    time.Time      `json:"at"`
	Kind  string         `json:"kind"`
	Actor string         `json:"actor,omitempty"` // кто сделал: ник организатора или игрока
	Label string         `json:"label,omitempty"` // о ком событие
	Data  map[string]any `json:"data,omitempty"`
}

// QueueSessionRef - краткая ссылка на живую сессию для переключателя форматов.
type QueueSessionRef struct {
	ID     string `json:"id"`
	Format string `json:"format"`
	State  string `json:"state"`
	Title  string `json:"title"`
}

// QueueTeamOption - команда игрока, которой он может записаться в очередь 2×2.
type QueueTeamOption struct {
	TeamKey string        `json:"teamKey"`
	Name    string        `json:"name"`
	Members []QueueMember `json:"members"`
	Blocked string        `json:"blocked,omitempty"` // почему записать нельзя
}

// QueueMe - что очередь значит для вошедшего игрока.
type QueueMe struct {
	EntryID      string            `json:"entryId,omitempty"`
	Position     int               `json:"position,omitempty"`
	Ahead        int               `json:"ahead"`                  // сколько записей перед ним
	Before       *QueueEntry       `json:"before,omitempty"`       // кто выступит прямо перед ним
	Status       string            `json:"status,omitempty"`       // статус его записи
	NeedsConfirm bool              `json:"needsConfirm,omitempty"` // его вызвали, а он ещё не подтвердил
	Confirmed    bool              `json:"confirmed,omitempty"`    // он подтвердил, ждём напарника
	NeedsStats   bool              `json:"needsStats,omitempty"`   // у него нет подтверждённых часов и ноков
	CanLeave     bool              `json:"canLeave"`
	CanJoin      bool              `json:"canJoin"`
	JoinBlocked  string            `json:"joinBlocked,omitempty"` // почему записаться нельзя
	Teams        []QueueTeamOption `json:"teams,omitempty"`       // команды игрока для записи в 2×2
}

// QueueSnapshot - всё, что нужно странице очереди за один запрос.
type QueueSnapshot struct {
	ServerTime time.Time         `json:"serverTime"`
	Session    *QueueSession     `json:"session"`
	Entries    []QueueEntry      `json:"entries"`
	Match      *QueueMatch       `json:"match,omitempty"` // матч в подготовке или в эфире
	Last       *QueueMatch       `json:"last,omitempty"`  // последний сыгранный матч сессии
	Active     []QueueSessionRef `json:"active"`          // живые очереди всех форматов
	Me         *QueueMe          `json:"me,omitempty"`    // только для вошедшего игрока
	History    []QueueMatch      `json:"history"`         // сыгранные и отменённые матчи, новые сверху
}

// QueueCall - запись игрока, которую вызвали на матч или которая уже в эфире: по ней сайт показывает плашку
// на любой странице, не только на странице очереди.
type QueueCall struct {
	SessionID       string     `json:"sessionId"`
	Format          string     `json:"format"`
	Title           string     `json:"title"`
	EntryID         string     `json:"entryId"`
	Name            string     `json:"name"`
	Status          string     `json:"status"` // invited | ready | performing
	Position        int        `json:"position"`
	InviteExpiresAt *time.Time `json:"inviteExpiresAt,omitempty"`
	NeedsConfirm    bool       `json:"needsConfirm"` // вызвали, а он ещё не подтвердил
	Confirmed       bool       `json:"confirmed"`    // он подтвердил, ждём напарника или начала
}
