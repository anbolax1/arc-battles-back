package models

import (
	"encoding/json"
	"math"
	"time"
)

type Role string

const (
	RoleUser       Role = "user"       // обычный зарегистрированный пользователь
	RoleSuperadmin Role = "superadmin" // организатор: полный доступ ко всему
)

// DefaultRole — роль нового аккаунта по умолчанию.
const DefaultRole = RoleUser

// roleLevels — иерархия ролей: чем больше число, тем больше прав. Роль с бóльшим
// уровнем автоматически имеет все доступы ролей ниже. Значения разрежены (10, 100),
// чтобы новые роли (например moderator=50) можно было вставлять между существующими.
var roleLevels = map[Role]int{
	RoleUser:       10,
	RoleSuperadmin: 100,
}

// Level — числовой уровень роли (неизвестная роль → 0, ниже любой валидной).
func (r Role) Level() int { return roleLevels[r] }

// AtLeast сообщает, что роль не ниже требуемой — основа иерархической проверки доступа.
func (r Role) AtLeast(min Role) bool { return r.Level() >= min.Level() }

// Valid сообщает, известна ли роль.
func (r Role) Valid() bool { _, ok := roleLevels[r]; return ok }

type User struct {
	ID          string    `json:"id"`
	Login       string    `json:"login"`
	DisplayName string    `json:"displayName"`
	AvatarURL   string    `json:"avatarUrl"`
	Email       string    `json:"-"`
	Role        Role      `json:"role"`
	EmbarkID    string    `json:"embarkId"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Season — период рейтинга. Ровно один active одновременно; завершённый имеет ended_at.
type Season struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"` // active | finished
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

type Tournament struct {
	ID                  string        `json:"id"`
	Title               string        `json:"title"`
	Mode                string        `json:"mode"`
	PlayerType          string        `json:"playerType"` // pve | pvp | pvpve — тип игроков (пул основных заданий/контрактов)
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
	Mmr         int    `json:"mmr"`    // рейтинг по исходам (старт 1000, сквозной по сезонам)
	Points      int    `json:"points"` // сумма набранных баллов за сезон (вторично, для контекста)
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

// PlayerStats — расширенная статистика игрока по завершённым турнирам:
// винрейт по режимам, источники очков, любимая карта и текущая серия.
type PlayerStats struct {
	SoloWins   int `json:"soloWins"`
	SoloPlayed int `json:"soloPlayed"`
	DuoWins    int `json:"duoWins"`
	DuoPlayed  int `json:"duoPlayed"`

	// Серия по последним завершённым турнирам: kind = "win" | "loss" | "" (нет игр).
	StreakKind string `json:"streakKind"`
	StreakLen  int    `json:"streakLen"`

	// Источники очков (суммарно по завершённым турнирам).
	BasePoints      int `json:"basePoints"`      // ручная корректировка раунда (round_entries.points)
	MainPoints      int `json:"mainPoints"`      // основные задания раунда
	ContractPoints  int `json:"contractPoints"`  // контракты (свои 2 + чужие 1)
	LegendaryPoints int `json:"legendaryPoints"` // легендарные контракты (10 каждый)

	FavoriteMap       string `json:"favoriteMap"`
	FavoriteMapRounds int    `json:"favoriteMapRounds"`
}

// PlayerProfile — публичный профиль игрока: пользователь + статы + история (B6).
type PlayerProfile struct {
	User        User                `json:"user"`
	MmrSolo     int                 `json:"mmrSolo"` // MMR в режиме 1x1 (старт 1000)
	MmrDuo      int                 `json:"mmrDuo"`  // MMR в режиме 2x2 (старт 1000)
	Points      int                 `json:"points"`
	Wins        int                 `json:"wins"`
	Tournaments int                 `json:"tournaments"`
	Stats       PlayerStats         `json:"stats"`
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
	Points        int    `json:"points"`      // всего по турниру
	RoundPoints   int    `json:"roundPoints"` // очки за текущий раунд (для опции в табло)
}

// LiveBonus — контракт стороны в оверлее (виджет «Контракты»).
type LiveBonus struct {
	Text      string `json:"text"`
	Points    int    `json:"points"`
	ValueType string `json:"valueType"`          // fixed | percent
	Times     int    `json:"times"`              // 0 — не зачтён, >0 — зачтён (для подсветки)
	Who       string `json:"who,omitempty"`      // имя стороны-владельца контракта
	Opponent  bool   `json:"opponent,omitempty"` // контракт противника фокусной стороны (для опции «показывать контракты противника»)
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

	// Богатые данные для модульных виджетов (Фаза 3). Заполняются «Эфиром»;
	// пустые при дефолтном табло. Singular complication выше оставлен для совместимости.
	RoundTasks    []Task             `json:"roundTasks,omitempty"`    // стартовые задания текущего раунда
	BonusTasks    []LiveBonus        `json:"bonusTasks,omitempty"`    // бонусные фокусной стороны
	Complications []LiveComplication `json:"complications,omitempty"` // усложнения обеих сторон

	// Кастомизируемая раскладка оверлея (модульные виджеты). Если nil — оверлей
	// рендерит дефолтную раскладку (источник дефолта — фронт). Чтобы пуш раскладки
	// организатором переживал round-trip через json.Marshal(LiveState), поле должно
	// присутствовать в структуре.
	Layout *OverlayLayout `json:"layout,omitempty"`
}

// OverlayLayout — документ раскладки оверлея: набор независимых виджетов с
// позициями/прозрачностью + глобальные настройки сцены. Хранится внутри того же
// jsonb live_state, отдельной таблицы/эндпоинта не требует.
type OverlayLayout struct {
	Version        int              `json:"version"`
	Accent         string           `json:"accent,omitempty"`         // глобальный акцент (переопределяет --primary)
	StageBg        OverlayBg        `json:"stageBg"`                  // глобальный фон сцены (затемнение)
	Pad            float64          `json:"pad,omitempty"`            // отступ от края (px сцены) при выравнивании по краю; 0 = дефолт
	ActivePresetID string           `json:"activePresetId,omitempty"` // выбранный общий пресет (server-side, одинаков на всех устройствах)
	Widgets        []WidgetInstance `json:"widgets"`
}

// OverlayBg — настройка фона (вкл/выкл + прозрачность 0..1). Используется и для
// сцены целиком, и для каждого виджета.
type OverlayBg struct {
	On      bool    `json:"on"`
	Opacity float64 `json:"opacity"`
}

// OverlayPreset — общий (глобальный) сохранённый шаблон раскладки. layout хранится
// «как есть» (json.RawMessage = полный OverlayLayout), чтобы не зависеть от полей модели.
type OverlayPreset struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Layout    json.RawMessage `json:"layout"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// WidgetInstance — один экземпляр виджета на сцене. Позиция/размер — ДОЛЯМИ от
// 1920×1080 (resolution-independent), чтобы раскладка не зависела от разрешения.
type WidgetInstance struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`        // scoreboard|round|complications|standings|roundTasks|bonusTasks|text|logo
	X              float64         `json:"x"`           // 0..1 — левый край
	Y              float64         `json:"y"`           // 0..1 — верхний край
	W              float64         `json:"w,omitempty"` // 0..1 — ширина (пусто = по контенту)
	H              float64         `json:"h,omitempty"` // 0..1 — высота (пусто = по контенту)
	Scale          float64         `json:"scale"`
	Z              int             `json:"z"`
	Visible        bool            `json:"visible"`
	Locked         bool            `json:"locked,omitempty"`
	HideTitle      bool            `json:"hideTitle,omitempty"`             // скрыть заголовок/подпись виджета
	HidePenalty    bool            `json:"hidePenalty,omitempty"`           // усложнения: не показывать плашку «ШТРАФ» при нарушении
	ShowRoundScore bool            `json:"showRoundScore,omitempty"`        // табло: очки за раунд в скобках у счёта
	ShowOpponentCs bool            `json:"showOpponentContracts,omitempty"` // контракты: показывать и контракты противника
	Anchor         string          `json:"anchor,omitempty"`                // привязка к краю (tl|tc|tr|ml|c|mr|bl|bc|br); "" — свободно
	Bg             OverlayBg       `json:"bg"`
	Accent         string          `json:"accent,omitempty"`
	Props          json.RawMessage `json:"props,omitempty"` // пер-типовые доп.поля (текст, url логотипа и т.п.)
}

// StarterTask — стартовое задание из пула (НЕ бонусное, скрыто от публики/правил).
type StarterTask struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Points int    `json:"points"`
	Kind   string `json:"kind"` // pve | pvp | mixed
}

// RoundStarterTask — основное задание раунда (одинаково у обеих сторон). Зачёт раздельный по
// сторонам: Done = participantId → сколько раз зачтено (баллы стороне = times × points).
type RoundStarterTask struct {
	ID            string          `json:"id"`
	RoundID       string          `json:"roundId"`
	StarterTaskID string          `json:"starterTaskId"`
	Text          string          `json:"text"`
	Points        int             `json:"points"`
	Done          []RoundTaskDone `json:"done"`
}

// RoundTaskDone — зачёт основного задания конкретной стороной.
type RoundTaskDone struct {
	ParticipantID string `json:"participantId"`
	Times         int    `json:"times"`
}

// RoundBonusTask — бонусное задание участника в раунде. times — сколько раз зачтено
// (0 → не выполнено, переносится; баллы = times × величина). roundNumber — в каком раунде выбрано
// (для «перенесено из раунда N»). Text/Points/ValueType — из catalog_tasks.
type RoundBonusTask struct {
	ID            string  `json:"id"`
	RoundID       string  `json:"roundId"`
	RoundNumber   int     `json:"roundNumber"`
	ParticipantID string  `json:"participantId"` // владелец контракта (кому выдан)
	TaskID        string  `json:"taskId"`
	Text          string  `json:"text"`
	Points        int     `json:"points"`
	ValueType     string  `json:"valueType"`
	Kind          string  `json:"kind"`                  // pve | pvp | pvpve
	Times         int     `json:"times"`                 // legacy-счётчик (не используется в скоринге контрактов)
	CompletedBy   *string `json:"completedBy,omitempty"` // кто выполнил: владелец → +2, противник → +1, nil → не выполнен
}

// CatalogLegendary — легендарный контракт (глобальный пул, 10 баллов, выполним один раз навсегда).
type CatalogLegendary struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Points    int    `json:"points"`
	Kind      string `json:"kind"`   // pve | pvp | pvpve
	Source    string `json:"source"` // official | boosty
	Author    string `json:"author,omitempty"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status"` // available | done
	SortOrder int    `json:"-"`

	// Данные о выполнении (если status=done) — для журнала/публички.
	Completion *LegendaryCompletion `json:"completion,omitempty"`
}

// LegendaryCompletion — запись о выполнении легендарного контракта (ник/дата/карта).
type LegendaryCompletion struct {
	ID                  string    `json:"id"`
	LegendaryContractID string    `json:"legendaryContractId"`
	UserID              *string   `json:"userId,omitempty"`
	ParticipantID       *string   `json:"participantId,omitempty"`
	Nickname            string    `json:"nickname"`
	TournamentID        *string   `json:"tournamentId,omitempty"`
	Map                 string    `json:"map,omitempty"`
	CompletedAt         time.Time `json:"completedAt"`
	TournamentTitle     string    `json:"tournamentTitle,omitempty"`
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
