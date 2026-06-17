package models

import (
	"encoding/json"
	"math"
	"time"
)

type Role string

const (
	RoleViewer      Role = "viewer"
	RoleParticipant Role = "participant"
	RoleOrganizer   Role = "organizer"
)

type User struct {
	ID          string    `json:"id"`
	TwitchID    string    `json:"twitchId"`
	Login       string    `json:"login"`
	DisplayName string    `json:"displayName"`
	AvatarURL   string    `json:"avatarUrl"`
	Email       string    `json:"-"`
	Role        Role      `json:"role"`
	EmbarkID    string    `json:"embarkId"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Tournament struct {
	ID                  string        `json:"id"`
	Title               string        `json:"title"`
	Mode                string        `json:"mode"`
	Status              string        `json:"status"`
	TotalRounds         int           `json:"totalRounds"`
	Maps                []string      `json:"maps"`
	StartsAt            *time.Time    `json:"startsAt,omitempty"`
	WinnerParticipantID *string       `json:"winnerParticipantId,omitempty"`
	CreatedAt           time.Time     `json:"createdAt"`
	UpdatedAt           time.Time     `json:"updatedAt"`
	ParticipantCount    int           `json:"participantCount"` // в списках (без полного participants[])
	HasSpace            bool          `json:"hasSpace"`         // есть ли свободные слоты (1×1: <2 игроков; 2×2: <2 команд или неполная команда)
	Participants        []Participant `json:"participants,omitempty"`
	Rounds              []Round       `json:"rounds,omitempty"`
}

type Participant struct {
	ID           string          `json:"id"`
	TournamentID string          `json:"tournamentId"`
	Kind         string          `json:"kind"`
	UserID       *string         `json:"userId,omitempty"`
	Name         string          `json:"name"`
	Seed         int             `json:"seed"`
	TotalPoints  int             `json:"totalPoints"`
	Members      json.RawMessage `json:"members,omitempty"`
}

type Round struct {
	ID           string `json:"id"`
	TournamentID string `json:"tournamentId"`
	Number       int    `json:"number"`
	Map          string `json:"map"`
	Status       string `json:"status"`
}

// RoundEntry — результат участника в раунде (B2). points — нетто-очки раунда;
// tasks/bonus/complications — метаданные (что зачтено), на сумму не влияют.
type RoundEntry struct {
	ID            string          `json:"id"`
	RoundID       string          `json:"roundId"`
	ParticipantID string          `json:"participantId"`
	Points        int             `json:"points"`
	Tasks         json.RawMessage `json:"tasks"`
	Bonus         json.RawMessage `json:"bonus"`
	Complications json.RawMessage `json:"complications"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

type Registration struct {
	ID              string     `json:"id"`
	TournamentID    *string    `json:"tournamentId,omitempty"` // куда поставлен; nil — пока в пуле
	UserID          string     `json:"userId"`
	EmbarkID        string     `json:"embarkId"`
	Status          string     `json:"status"`
	Note            string     `json:"note"`
	CreatedAt       time.Time  `json:"createdAt"`
	DecidedAt       *time.Time `json:"decidedAt,omitempty"`
	UserLogin       string     `json:"userLogin,omitempty"`
	UserDisplayName string     `json:"userDisplayName,omitempty"`
	UserAvatarURL   string     `json:"userAvatarUrl,omitempty"`
	TournamentTitle string     `json:"tournamentTitle,omitempty"`
}

type LeaderboardRow struct {
	UserID      string `json:"userId"`
	Login       string `json:"login"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl"`
	Points      int    `json:"points"`
	Wins        int    `json:"wins"`
	Tournaments int    `json:"tournaments"`
}

// PlayerHistoryItem — одно участие игрока в турнире (для профиля, B6).
type PlayerHistoryItem struct {
	TournamentID string     `json:"tournamentId"`
	Title        string     `json:"title"`
	Mode         string     `json:"mode"`
	Status       string     `json:"status"`
	Date         *time.Time `json:"date,omitempty"`
	Name         string     `json:"name"` // имя участника/стороны игрока в том турнире
	Points       int        `json:"points"`
	Win          bool       `json:"win"`
}

// PlayerProfile — публичный профиль игрока: пользователь + сезон-статы + история (B6).
type PlayerProfile struct {
	User        User                `json:"user"`
	Points      int                 `json:"points"`
	Wins        int                 `json:"wins"`
	Tournaments int                 `json:"tournaments"`
	History     []PlayerHistoryItem `json:"history"`
}

// Highlight — пользовательский хайлайт (твич-клип, скачанный к нам, или загруженный файл).
// Публикуется после модерации (status='approved'). videoUrl/thumbUrl — готовые ссылки для
// фронта (формируются из file_path/thumb_path). Поля User* — данные автора для карточки.
type Highlight struct {
	ID              string    `json:"id"`
	UserID          string    `json:"userId"`
	UserLogin       string    `json:"userLogin"`
	UserName        string    `json:"userName"`
	UserAvatarURL   string    `json:"userAvatarUrl"`
	TournamentID    *string   `json:"tournamentId,omitempty"`
	TournamentTitle string    `json:"tournamentTitle,omitempty"`
	Title           string    `json:"title"`
	Source          string    `json:"source"` // twitch_clip | upload
	SourceURL       string    `json:"sourceUrl,omitempty"`
	VideoURL        string    `json:"videoUrl,omitempty"`
	ThumbURL        string    `json:"thumbUrl,omitempty"`
	PreviewURL      string    `json:"previewUrl,omitempty"` // лёгкое превью для автоплея в «стене»
	Duration        int       `json:"duration"`
	Status          string    `json:"status"` // processing|pending|approved|rejected|failed
	RejectReason    string    `json:"rejectReason,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

// UserOverview — пользователь + агрегаты участия для раздела «Пользователи» в кабинете.
// Email раскрываем только здесь (организатору); в публичной модели User он скрыт (json:"-").
type UserOverview struct {
	User
	Email          string `json:"email"`
	Tournaments    int    `json:"tournaments"`    // завершённых турниров
	Wins           int    `json:"wins"`           // побед в завершённых
	Points         int    `json:"points"`         // суммарные очки в завершённых
	Participations int    `json:"participations"` // всего участий (включая текущие/анонсы)
}

// Тип числового значения задания/усложнения.
const (
	ValueFixed   = "fixed"   // фиксированное число баллов
	ValuePercent = "percent" // процент от текущих баллов участника в турнире
)

type CatalogTask struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Points    int    `json:"points"`    // величина: баллы (fixed) или процент (percent)
	ValueType string `json:"valueType"` // fixed | percent
	Kind      string `json:"kind"`      // pve | pvp | mixed
	Source    string `json:"source"`    // official | boosty
	Author    string `json:"author,omitempty"`
	Title     string `json:"title,omitempty"`
}

type CatalogComplication struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Penalty   int    `json:"penalty"`   // величина: баллы (fixed) или процент (percent)
	ValueType string `json:"valueType"` // fixed | percent
	Source    string `json:"source"`    // official | boosty
	Author    string `json:"author,omitempty"`
	Title     string `json:"title,omitempty"`
}

// EffectiveValue переводит величину в фактические баллы с учётом типа значения.
// Для percent берётся процент от текущих очков участника в турнире.
func EffectiveValue(value int, valueType string, currentPoints int) int {
	if valueType == ValuePercent {
		return int(math.Round(float64(currentPoints) * float64(value) / 100))
	}
	return value
}

// Reward — сколько баллов даёт задание при текущем счёте участника.
func (t CatalogTask) Reward(currentPoints int) int {
	return EffectiveValue(t.Points, t.ValueType, currentPoints)
}

// PenaltyFor — сколько баллов снимает усложнение при текущем счёте участника.
func (c CatalogComplication) PenaltyFor(currentPoints int) int {
	return EffectiveValue(c.Penalty, c.ValueType, currentPoints)
}

// Task — задание раунда в живом состоянии оверлея.
type Task struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Points    int    `json:"points"`
	Completed bool   `json:"completed"`
}

// LiveComplication — усложнение текущего раунда в оверлее (B3). Показывается, только
// если задано (omitempty → отсутствует, когда усложнения нет).
type LiveComplication struct {
	Who       string `json:"who,omitempty"` // к кому применяется (имя стороны/участника)
	Text      string `json:"text"`
	Penalty   int    `json:"penalty"`
	ValueType string `json:"valueType"` // fixed | percent
	Times     int    `json:"times"`     // сколько раз нарушено в текущем раунде (0 — не нарушено)
}

// LiveStanding — сторона матча в оверлее с СУММАРНЫМИ очками (по всем раундам).
type LiveStanding struct {
	ParticipantID string `json:"participantId,omitempty"`
	Name          string `json:"name"`
	Points        int    `json:"points"`
}

// LiveState — состояние оверлея, которым управляет организатор и которое стримится в OBS.
type LiveState struct {
	TournamentID         *string           `json:"tournamentId,omitempty"`
	TournamentName       string            `json:"tournamentName"`
	Status               string            `json:"status"` // статус турнира; оверлей показывает табло только при "live"
	Mode                 string            `json:"mode"`
	CurrentRound         int               `json:"currentRound"`
	TotalRounds          int               `json:"totalRounds"`
	CurrentParticipantID *string           `json:"currentParticipantId,omitempty"`
	CurrentName          string            `json:"currentName"`
	CurrentPoints        int               `json:"currentPoints"`
	Tasks                []Task            `json:"tasks"`
	Complication         *LiveComplication `json:"complication,omitempty"`
	Standings            []LiveStanding    `json:"standings,omitempty"`
	ShowStandings        bool              `json:"showStandings"`
}

// StarterTask — стартовое задание из пула (НЕ бонусное, скрыто от публики/правил).
type StarterTask struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Points int    `json:"points"`
	Kind   string `json:"kind"` // pve | pvp | mixed
}

// RoundStarterTask — стартовое задание, назначенное на раунд. times — сколько раз зачтено
// (баллы = times × points), completedBy — кому. Text/Points — из starter_tasks для кабинета.
type RoundStarterTask struct {
	ID            string  `json:"id"`
	RoundID       string  `json:"roundId"`
	StarterTaskID string  `json:"starterTaskId"`
	Text          string  `json:"text"`
	Points        int     `json:"points"`
	CompletedBy   *string `json:"completedBy,omitempty"`
	Times         int     `json:"times"`
}

// RoundBonusTask — бонусное задание участника в раунде. times — сколько раз зачтено
// (0 → не выполнено, переносится; баллы = times × величина). roundNumber — в каком раунде выбрано
// (для «перенесено из раунда N»). Text/Points/ValueType — из catalog_tasks.
type RoundBonusTask struct {
	ID            string `json:"id"`
	RoundID       string `json:"roundId"`
	RoundNumber   int    `json:"roundNumber"`
	ParticipantID string `json:"participantId"`
	TaskID        string `json:"taskId"`
	Text          string `json:"text"`
	Points        int    `json:"points"`
	ValueType     string `json:"valueType"`
	Times         int    `json:"times"`
}

// RoundPenalty — применённое усложнение участнику в раунде. Штраф = times × величина
// (fixed — баллы; percent — % от суммы набранных очков участника). Text/Penalty/ValueType — из каталога.
type RoundPenalty struct {
	ID             string `json:"id"`
	RoundID        string `json:"roundId"`
	ParticipantID  string `json:"participantId"`
	ComplicationID string `json:"complicationId"`
	Text           string `json:"text"`
	Penalty        int    `json:"penalty"`
	ValueType      string `json:"valueType"`
	Times          int    `json:"times"`
}
