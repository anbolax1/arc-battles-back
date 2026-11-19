// Package syncer - двусторонняя синхронизация локального приложения организатора с сайтом.
//
// Каждая строка сравнивается по хешу трёх состояний: у приложения, на сайте и при прошлой
// синхронизации. Так видно, кто изменил строку; если изменили обе стороны, побеждает владелец таблицы.
// MMR, нашивки и пояс не переносятся: каждая сторона пересчитывает их по матчам сама.
package syncer

// Side - кто владеет таблицей и выигрывает при одновременной правке строки.
type Side int

const (
	SideApp  Side = iota // организатор правит в приложении
	SideSite             // игроки правят на сайте
)

// ProtoVersion меняется, когда сайт и приложение перестают понимать друг друга.
const ProtoVersion = 2

// Table - таблица, которая ездит между приложением и сайтом.
type Table struct {
	Name string
	// Exclude - колонки, которые не передаются и не участвуют в хеше; Insert - чем их заполнить при создании
	// строки на другой стороне. Сейчас переносятся все колонки: у этих полей нет ни одной таблицы.
	Exclude []string
	Insert  map[string]string
	// Optional - таблицы может не быть в базе (её завели вручную, миграции её не создают): тогда она пропускается,
	// а приложение создаёт недостающую по схеме сайта при первой загрузке.
	Optional bool
	// NoPK - у таблицы нет первичного ключа (старые копии): строки опознаются по хешу, ездят только при первой загрузке.
	NoPK   bool
	Winner Side
	// InitOnly - таблица копируется только при первой загрузке, дальше у каждой стороны своя.
	InitOnly bool
	// Serial - у таблицы последовательный числовой ключ: после записей счётчик подтягивается.
	Serial bool
}

// Tables - порядок важен: родители раньше детей (вставки идут сверху вниз, удаления снизу вверх).
var Tables = []Table{
	{Name: "users", Winner: SideSite},
	{Name: "account_claim_tokens", Winner: SideSite},
	{Name: "seasons", Winner: SideApp},
	{Name: "maps", Winner: SideApp},
	{Name: "tags", Winner: SideApp},
	{Name: "teams", Winner: SideApp},
	{Name: "news", Winner: SideApp},
	{Name: "belt_news", Winner: SideApp},
	{Name: "catalog_complications", Winner: SideApp},
	{Name: "catalog_tasks", Winner: SideApp},
	{Name: "starter_tasks", Winner: SideApp},
	{Name: "legendary_contracts", Winner: SideApp},
	{Name: "overlay_presets", Winner: SideApp},
	{Name: "site_settings", Winner: SideApp},
	{Name: "tournaments", Winner: SideApp},
	{Name: "participants", Winner: SideApp},
	{Name: "rounds", Winner: SideApp},
	{Name: "round_entries", Winner: SideApp},
	{Name: "round_member_entries", Winner: SideApp},
	{Name: "round_penalties", Winner: SideApp},
	{Name: "round_starter_tasks", Winner: SideApp},
	{Name: "round_starter_task_done", Winner: SideApp},
	{Name: "round_bonus_tasks", Winner: SideApp},
	{Name: "legendary_contract_completions", Winner: SideApp},
	{Name: "match_log", Winner: SideApp},
	{Name: "match_veto", Winner: SideApp},
	{Name: "raid_starts", Winner: SideApp, Serial: true},
	{Name: "mmr_corrections", Winner: SideApp},
	{Name: "token_ledger", Winner: SideApp},
	{Name: "user_tags", Winner: SideApp},
	{Name: "user_hidden_tags", Winner: SideSite},
	{Name: "player_favorites", Winner: SideSite},
	{Name: "registrations", Winner: SideSite},
	{Name: "stat_claims", Winner: SideSite},
	{Name: "highlights", Winner: SideSite},
	{Name: "queue_sessions", Winner: SideSite},
	{Name: "queue_entries", Winner: SideSite},
	{Name: "queue_entry_members", Winner: SideSite},
	{Name: "queue_matches", Winner: SideSite},
	{Name: "queue_match_entries", Winner: SideSite},
	{Name: "queue_events", Winner: SideSite, Serial: true},
	{Name: "live_state", Winner: SideApp},
	{Name: "catalog_task_protocol_backup", Winner: SideApp, InitOnly: true, Optional: true},
	{Name: "catalog_complications_bak_20260624", Winner: SideApp, InitOnly: true, NoPK: true, Optional: true},
	{Name: "catalog_tasks_bak_20260624", Winner: SideApp, InitOnly: true, NoPK: true, Optional: true},
}

// derivedTables - всё, что считается по матчам и поэтому не переносится.
var derivedTables = []string{"mmr_history", "team_mmr_history", "user_mmr", "team_mmr", "player_patches"}

// recomputeTables - изменение этих таблиц меняет рейтинг и нашивки.
var recomputeTables = map[string]bool{
	"tournaments": true, "participants": true, "rounds": true, "round_entries": true,
	"round_member_entries": true, "round_bonus_tasks": true, "round_starter_task_done": true,
	"legendary_contract_completions": true, "round_penalties": true, "mmr_corrections": true,
	"token_ledger": true, "seasons": true,
}

func tableByName(name string) (Table, bool) {
	for _, t := range Tables {
		if t.Name == name {
			return t, true
		}
	}
	return Table{}, false
}
