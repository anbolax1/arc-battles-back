package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier - то, что есть и у пула, и у транзакции.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

type tableMeta struct {
	Cols []string
	PK   []string
}

// Meta - колонки и ключи таблиц, читаются из каталога БД: список колонок не дублируется в коде.
type Meta map[string]*tableMeta

func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

func loadMeta(ctx context.Context, q Querier, tables []Table) (Meta, error) {
	names := make([]string, 0, len(tables))
	for _, t := range tables {
		names = append(names, t.Name)
	}
	m := Meta{}
	rows, err := q.Query(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = ANY($1) AND is_generated = 'NEVER'
		ORDER BY table_name, ordinal_position`, names)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			rows.Close()
			return nil, err
		}
		if m[t] == nil {
			m[t] = &tableMeta{}
		}
		m[t].Cols = append(m[t].Cols, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `
		SELECT c.relname, a.attname
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
		WHERE i.indisprimary AND c.relnamespace = 'public'::regnamespace AND c.relname = ANY($1)
		ORDER BY c.relname, array_position(i.indkey::int2[], a.attnum)`, names)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			rows.Close()
			return nil, err
		}
		if m[t] != nil {
			m[t].PK = append(m[t].PK, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range tables {
		mt := m[t.Name]
		if mt == nil && t.Optional {
			continue
		}
		if mt == nil || (len(mt.PK) == 0 && !t.NoPK) {
			return nil, fmt.Errorf("таблица %s: нет в базе или без первичного ключа", t.Name)
		}
	}
	return m, nil
}

// keyExpr - ключ строки одним текстом; составной ключ склеивается через «|».
func keyExpr(mt *tableMeta) string {
	if len(mt.PK) == 0 {
		return "md5(to_jsonb(t)::text)"
	}
	parts := make([]string, len(mt.PK))
	for i, c := range mt.PK {
		parts[i] = "t." + ident(c) + "::text"
	}
	return "concat_ws('|', " + strings.Join(parts, ", ") + ")"
}

// hashExpr - хеш строки без исключённых колонок. Текст jsonb у одинаковых данных одинаков,
// а часовой пояс сессии зафиксирован в UTC (см. beginRead/beginWrite).
const hashExpr = "md5((to_jsonb(t) - $1::text[])::text)"

func excl(t Table) []string {
	if t.Exclude == nil {
		return []string{}
	}
	return t.Exclude
}

// beginRead открывает транзакцию с единым снимком данных для сравнения.
func beginRead(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func beginWrite(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// Manifest считает хеши всех строк указанных таблиц (по умолчанию - всех, кроме разовых).
func Manifest(ctx context.Context, pool *pgxpool.Pool, tables []Table) (Hashes, error) {
	tx, err := beginRead(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	meta, err := loadMeta(ctx, tx, tables)
	if err != nil {
		return nil, err
	}
	out := Hashes{}
	for _, t := range tables {
		h := map[string]string{}
		if meta[t.Name] == nil {
			out[t.Name] = h // необязательной таблицы нет в базе
			continue
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s, %s FROM %s t`, keyExpr(meta[t.Name]), hashExpr, ident(t.Name)), excl(t))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.Name, err)
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				rows.Close()
				return nil, err
			}
			h[k] = v
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", t.Name, err)
		}
		out[t.Name] = h
	}
	return out, nil
}

// FetchRows отдаёт строки таблицы по ключам: ключ -> JSON строки без исключённых колонок.
func FetchRows(ctx context.Context, pool *pgxpool.Pool, t Table, keys []string) (map[string]json.RawMessage, error) {
	tx, err := beginRead(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	meta, err := loadMeta(ctx, tx, []Table{t})
	if err != nil {
		return nil, err
	}
	if meta[t.Name] == nil {
		return map[string]json.RawMessage{}, nil
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(
		`SELECT %[1]s, (to_jsonb(t) - $1::text[])::text FROM %[2]s t WHERE %[1]s = ANY($2)`,
		keyExpr(meta[t.Name]), ident(t.Name)), excl(t), keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, js string
		if err := rows.Scan(&k, &js); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(js)
	}
	return out, rows.Err()
}

// UpRow - строка к записи. IfHash - хеш, который строка должна иметь сейчас (пусто - строки быть не должно);
// иначе её уже кто-то поменял, и запись пропускается до следующего прохода.
type UpRow struct {
	Key    string          `json:"key"`
	IfHash string          `json:"ifHash"`
	Row    json.RawMessage `json:"row"`
}

// DelRow - строка к удалению с тем же условием.
type DelRow struct {
	Key    string `json:"key"`
	IfHash string `json:"ifHash"`
}

// TableChange - изменения одной таблицы.
type TableChange struct {
	Table   string   `json:"table"`
	Upserts []UpRow  `json:"upserts"`
	Deletes []DelRow `json:"deletes"`
}

// Failure - строка, которую не удалось записать (например, нет родителя).
type Failure struct {
	Table string `json:"table"`
	Key   string `json:"key"`
	Error string `json:"error"`
}

// KeyRef - ссылка на строку.
type KeyRef struct {
	Table string `json:"table"`
	Key   string `json:"key"`
}

// ApplyResult - итог записи.
type ApplyResult struct {
	Applied []KeyRef  `json:"applied"` // записанные и удалённые строки
	Stale   []KeyRef  `json:"stale"`   // изменились после сравнения - повторим в следующий раз
	Failed  []Failure `json:"failed"`
}

func (a *ApplyResult) merge(o ApplyResult) {
	a.Applied = append(a.Applied, o.Applied...)
	a.Stale = append(a.Stale, o.Stale...)
	a.Failed = append(a.Failed, o.Failed...)
}

// Apply записывает изменения одной транзакцией: вставки в порядке таблиц, удаления в обратном.
// check=false отключает проверку хешей (первая загрузка в пустую базу).
func Apply(ctx context.Context, pool *pgxpool.Pool, changes []TableChange, check bool) (ApplyResult, error) {
	tx, err := beginWrite(ctx, pool)
	if err != nil {
		return ApplyResult{}, err
	}
	defer tx.Rollback(ctx)
	res, err := ApplyTx(ctx, tx, changes, check)
	if err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

// ApplyTx - то же внутри чужой транзакции (слияние аккаунтов).
func ApplyTx(ctx context.Context, tx pgx.Tx, changes []TableChange, check bool) (ApplyResult, error) {
	var res ApplyResult
	tables := make([]Table, 0, len(changes))
	for _, ch := range changes {
		t, ok := tableByName(ch.Table)
		if !ok {
			return res, fmt.Errorf("неизвестная таблица %q", ch.Table)
		}
		tables = append(tables, t)
	}
	meta, err := loadMeta(ctx, tx, uniqueTables(tables))
	if err != nil {
		return res, err
	}
	for i, ch := range changes {
		if len(ch.Upserts) == 0 {
			continue
		}
		if meta[ch.Table] == nil {
			return res, fmt.Errorf("таблицы %s нет в базе", ch.Table)
		}
		r, err := upsertRows(ctx, tx, tables[i], meta[ch.Table], ch.Upserts, check)
		if err != nil {
			return res, err
		}
		res.merge(r)
	}
	for i := len(changes) - 1; i >= 0; i-- {
		if len(changes[i].Deletes) == 0 || meta[changes[i].Table] == nil {
			continue
		}
		r, err := deleteRows(ctx, tx, tables[i], meta[changes[i].Table], changes[i].Deletes, check)
		if err != nil {
			return res, err
		}
		res.merge(r)
	}
	for _, t := range uniqueTables(tables) {
		if t.Serial {
			if err := bumpSerial(ctx, tx, t, meta[t.Name]); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

func uniqueTables(in []Table) []Table {
	seen := map[string]bool{}
	var out []Table
	for _, t := range in {
		if !seen[t.Name] {
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	return out
}

// currentHashes - хеши строк по ключам прямо сейчас.
func currentHashes(ctx context.Context, tx pgx.Tx, t Table, mt *tableMeta, keys []string) (map[string]string, error) {
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %[1]s, %[2]s FROM %[3]s t WHERE %[1]s = ANY($2)`,
		keyExpr(mt), hashExpr, ident(t.Name)), excl(t), keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, h string
		if err := rows.Scan(&k, &h); err != nil {
			return nil, err
		}
		out[k] = h
	}
	return out, rows.Err()
}

func upsertSQL(t Table, mt *tableMeta) string {
	skip := map[string]bool{}
	for _, c := range t.Exclude {
		skip[c] = true
	}
	var cols, sel, set []string
	pk := map[string]bool{}
	for _, c := range mt.PK {
		pk[c] = true
	}
	for _, c := range mt.Cols {
		if skip[c] {
			if expr, ok := t.Insert[c]; ok {
				cols = append(cols, ident(c))
				sel = append(sel, expr)
			}
			continue
		}
		cols = append(cols, ident(c))
		sel = append(sel, "r."+ident(c))
		if !pk[c] {
			set = append(set, ident(c)+" = EXCLUDED."+ident(c))
		}
	}
	pks := make([]string, len(mt.PK))
	for i, c := range mt.PK {
		pks[i] = ident(c)
	}
	conflict := "ON CONFLICT (" + strings.Join(pks, ", ") + ") DO NOTHING"
	if len(set) > 0 {
		conflict = "ON CONFLICT (" + strings.Join(pks, ", ") + ") DO UPDATE SET " + strings.Join(set, ", ")
	}
	if len(mt.PK) == 0 {
		conflict = ""
	}
	return fmt.Sprintf(`INSERT INTO %[1]s (%[2]s) SELECT %[3]s FROM jsonb_populate_recordset(NULL::%[1]s, $1::jsonb) AS r %[4]s`,
		ident(t.Name), strings.Join(cols, ", "), strings.Join(sel, ", "), conflict)
}

func upsertRows(ctx context.Context, tx pgx.Tx, t Table, mt *tableMeta, rows []UpRow, check bool) (ApplyResult, error) {
	var res ApplyResult
	todo := rows
	if check {
		keys := make([]string, len(rows))
		for i, r := range rows {
			keys[i] = r.Key
		}
		cur, err := currentHashes(ctx, tx, t, mt, keys)
		if err != nil {
			return res, err
		}
		todo = todo[:0:0]
		for _, r := range rows {
			if cur[r.Key] != r.IfHash {
				res.Stale = append(res.Stale, KeyRef{t.Name, r.Key})
				continue
			}
			todo = append(todo, r)
		}
	}
	q := upsertSQL(t, mt)
	// Сначала всей пачкой; если упала - по одной строке, чтобы одна плохая не остановила остальные.
	if len(todo) > 0 {
		if err := inSavepoint(ctx, tx, func() error {
			_, err := tx.Exec(ctx, q, joinRows(todo))
			return err
		}); err == nil {
			for _, r := range todo {
				res.Applied = append(res.Applied, KeyRef{t.Name, r.Key})
			}
			return res, nil
		}
	}
	for _, r := range todo {
		r := r
		if err := inSavepoint(ctx, tx, func() error {
			_, err := tx.Exec(ctx, q, joinRows([]UpRow{r}))
			return err
		}); err != nil {
			res.Failed = append(res.Failed, Failure{t.Name, r.Key, err.Error()})
			continue
		}
		res.Applied = append(res.Applied, KeyRef{t.Name, r.Key})
	}
	return res, nil
}

func deleteRows(ctx context.Context, tx pgx.Tx, t Table, mt *tableMeta, rows []DelRow, check bool) (ApplyResult, error) {
	var res ApplyResult
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.Key
	}
	cur, err := currentHashes(ctx, tx, t, mt, keys)
	if err != nil {
		return res, err
	}
	q := fmt.Sprintf(`DELETE FROM %s t WHERE %s = $1`, ident(t.Name), keyExpr(mt))
	for _, r := range rows {
		r := r
		if check && cur[r.Key] != r.IfHash {
			res.Stale = append(res.Stale, KeyRef{t.Name, r.Key})
			continue
		}
		if err := inSavepoint(ctx, tx, func() error {
			_, err := tx.Exec(ctx, q, r.Key)
			return err
		}); err != nil {
			res.Failed = append(res.Failed, Failure{t.Name, r.Key, err.Error()})
			continue
		}
		res.Applied = append(res.Applied, KeyRef{t.Name, r.Key})
	}
	return res, nil
}

func joinRows(rows []UpRow) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, r := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(r.Row)
	}
	b.WriteByte(']')
	return b.String()
}

var savepointSeq int

func inSavepoint(ctx context.Context, tx pgx.Tx, fn func() error) error {
	savepointSeq++
	name := fmt.Sprintf("sp%d", savepointSeq)
	if _, err := tx.Exec(ctx, "SAVEPOINT "+name); err != nil {
		return err
	}
	if err := fn(); err != nil {
		if _, rerr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+name); rerr != nil {
			return rerr
		}
		return err
	}
	_, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+name)
	return err
}

// bumpSerial подтягивает счётчик ключа до максимума: строки приехали с готовыми номерами.
func bumpSerial(ctx context.Context, tx pgx.Tx, t Table, mt *tableMeta) error {
	pk := mt.PK[0]
	_, err := tx.Exec(ctx, fmt.Sprintf(
		`SELECT setval(pg_get_serial_sequence('%s', '%s'), GREATEST((SELECT COALESCE(max(%s), 1) FROM %s), 1))`,
		ident(t.Name), pk, ident(pk), ident(t.Name)))
	return err
}

// TableDDL собирает команду создания таблицы по её устройству в базе: имена и типы колонок, обязательность и ключ.
// Нужна, чтобы приложение создало таблицу, которой нет в миграциях.
func TableDDL(ctx context.Context, q Querier, table string) (string, bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+ident(table)).Scan(&exists); err != nil || !exists {
		return "", false, err
	}
	rows, err := q.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull
		FROM pg_attribute a WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, "public."+ident(table))
	if err != nil {
		return "", false, err
	}
	var cols []string
	for rows.Next() {
		var n, ty string
		var nn bool
		if err := rows.Scan(&n, &ty, &nn); err != nil {
			rows.Close()
			return "", false, err
		}
		def := ident(n) + " " + ty
		if nn {
			def += " NOT NULL"
		}
		cols = append(cols, def)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	pkRows, err := q.Query(ctx, `
		SELECT a.attname FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary ORDER BY array_position(i.indkey::int2[], a.attnum)`, "public."+ident(table))
	if err != nil {
		return "", false, err
	}
	var pk []string
	for pkRows.Next() {
		var n string
		if err := pkRows.Scan(&n); err != nil {
			pkRows.Close()
			return "", false, err
		}
		pk = append(pk, ident(n))
	}
	pkRows.Close()
	if len(pk) > 0 {
		cols = append(cols, "PRIMARY KEY ("+strings.Join(pk, ", ")+")")
	}
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", ident(table), strings.Join(cols, ", ")), true, nil
}

// Digest - контрольная сумма таблицы: число строк и хеш от всех пар «ключ + хеш строки». Совпали суммы у сайта,
// приложения и прошлой синхронизации - в таблице нечего переносить, и полный список хешей не нужен.
type Digest struct {
	N int    `json:"n"`
	D string `json:"d"`
}

// Digests считает контрольные суммы таблиц. Порядок ключей фиксирован сортировкой C: у сайта и приложения
// настройки сортировки базы могут отличаться.
func Digests(ctx context.Context, pool *pgxpool.Pool, tables []Table) (map[string]Digest, error) {
	tx, err := beginRead(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	meta, err := loadMeta(ctx, tx, tables)
	if err != nil {
		return nil, err
	}
	out := map[string]Digest{}
	for _, t := range tables {
		if meta[t.Name] == nil {
			out[t.Name] = Digest{}
			continue
		}
		var d Digest
		err := tx.QueryRow(ctx, fmt.Sprintf(
			`SELECT count(*), coalesce(md5(string_agg(k || h, '' ORDER BY k COLLATE "C")), '')
			 FROM (SELECT %s AS k, %s AS h FROM %s t) x`, keyExpr(meta[t.Name]), hashExpr, ident(t.Name)), excl(t)).Scan(&d.N, &d.D)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.Name, err)
		}
		out[t.Name] = d
	}
	return out, nil
}
