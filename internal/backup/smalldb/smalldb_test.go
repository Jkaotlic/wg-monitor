package smalldb

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	_ "modernc.org/sqlite"
)

// liveDB -- база, созданная настоящими миграциями бэкенда, с данными во
// всех интересных таблицах.
func liveDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.SQL().Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for i, nick := range []string{"alpha", "beta", "gamma", "doomed"} {
		exec(`INSERT INTO users (nickname, token_hash, expected_exit_ip, awg_iface, telegram_user_id) VALUES (?, ?, ?, 'nwg0', ?)`,
			nick, fmt.Sprintf("hash-%d", i), fmt.Sprintf("198.51.100.%d", i+1), 1000+i)
	}
	// Удалённый последний роутер: счётчик AUTOINCREMENT (4) больше max(id) (3).
	exec(`DELETE FROM users WHERE nickname = 'doomed'`)
	exec(`INSERT INTO router_operators (user_id, telegram_user_id, granted_by) VALUES (1, 2001, 1000), (1, 2002, 1000), (2, 2001, 1001)`)
	exec(`INSERT INTO router_credentials (user_id, nonce, ciphertext, saved_at) VALUES (1, x'0102030405060708090a0b0c', x'deadbeef00ff', '2026-09-18 10:00:00')`)
	exec(`INSERT INTO incident_state (user_id, check_name, consecutive_fails, current_status, hard_since) VALUES (2, 'tunnel:nwg0', 5, 'hard', '2026-10-01 03:00:00')`)
	exec(`INSERT INTO router_versions (user_id, awgmgr_version, source) VALUES (1, '2.19.9', 'report'), (3, '2.17.2', 'version_audit')`)
	exec(`INSERT INTO tg_state (key, value) VALUES ('offset', '42')`)
	exec(`INSERT INTO router_repair_settings (user_id) VALUES (1)`)
	for i := 0; i < 50; i++ {
		exec(`INSERT INTO events (user_id, check_name, status, details_json, ts) VALUES (?, 'ping', 'ok', '{}', ?)`,
			1+i%3, fmt.Sprintf("2026-10-01 00:%02d:00", i))
	}
	exec(`INSERT INTO daily_soft_flaps (user_id, check_name, flap_count, date) VALUES (1, 'ping', 3, '2026-10-01')`)
	exec(`INSERT INTO awgm_ping_runs (user_id, tunnel_id, from_ts, to_ts, fails) VALUES (1, 't1', 'a', 'b', 4)`)
	exec(`INSERT INTO alert_messages (user_id, check_name, telegram_user_id, message_id) VALUES (1, 'ping', 1000, 77)`)
	return d, path
}

func openRO(t *testing.T, path string) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// dump -- все строки таблицы в устойчивом порядке, значение как %#v.
func dump(t *testing.T, d *sql.DB, table string) []string {
	t.Helper()
	rows, err := d.Query(`SELECT * FROM "` + table + `"`)
	if err != nil {
		t.Fatalf("select %s: %v", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(cols))
		for i, v := range vals {
			parts[i] = fmt.Sprintf("%s=%#v", cols[i], v)
		}
		out = append(out, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func tableNames(t *testing.T, d *sql.DB) []string {
	t.Helper()
	rows, err := d.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// Сторож: новая таблица в схеме обязана быть явно отнесена к «переносим»
// или «пропускаем». Иначе большая таблица молча попадёт в малый архив и он
// перестанет пролезать в Telegram -- или нужная молча не попадёт.
func TestEveryTableIsClassified(t *testing.T) {
	d, _ := liveDB(t)
	tables := tableNames(t, d.SQL())
	if len(tables) < 10 {
		t.Fatalf("подозрительно мало таблиц в схеме: %v", tables)
	}
	for _, name := range tables {
		inCopy, inSkip := slices.Contains(CopyTables, name), slices.Contains(SkipTables, name)
		switch {
		case inCopy && inSkip:
			t.Errorf("таблица %q сразу в обоих списках", name)
		case !inCopy && !inSkip:
			t.Errorf("таблица %q не отнесена ни к CopyTables, ни к SkipTables в internal/backup/smalldb: "+
				"решите, нужна ли она для восстановления (CopyTables) или это тяжёлая история (SkipTables)", name)
		}
	}
	for _, name := range append(append([]string{}, CopyTables...), SkipTables...) {
		if !slices.Contains(tables, name) {
			t.Errorf("в списках малого архива таблица %q, которой нет в схеме", name)
		}
	}
	if want := []string{"events", "daily_soft_flaps", "awgm_ping_runs", "alert_messages"}; !slices.Equal(SkipTables, want) {
		t.Errorf("SkipTables = %v, по спеке %v", SkipTables, want)
	}
}

func TestBuildCopiesEverythingButHistory(t *testing.T) {
	d, src := liveDB(t)
	dst := filepath.Join(t.TempDir(), "small.db")
	if err := Build(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	small := openRO(t, dst)

	var check string
	if err := small.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q, %v", check, err)
	}
	if got, want := tableNames(t, small), tableNames(t, d.SQL()); !slices.Equal(got, want) {
		t.Fatalf("таблицы: %v, ждали %v", got, want)
	}
	for _, table := range CopyTables {
		got, want := dump(t, small, table), dump(t, d.SQL(), table)
		if !slices.Equal(got, want) {
			t.Errorf("таблица %s разошлась:\n got %v\nwant %v", table, got, want)
		}
	}
	for _, table := range []string{"users", "router_operators", "router_credentials", "incident_state", "router_versions"} {
		if n := len(dump(t, small, table)); n == 0 {
			t.Errorf("таблица %s пуста -- тест ничего не проверил", table)
		}
	}
	for _, table := range SkipTables {
		if n := len(dump(t, small, table)); n != 0 {
			t.Errorf("таблица %s должна быть пустой, строк: %d", table, n)
		}
		if n := len(dump(t, d.SQL(), table)); n == 0 {
			t.Errorf("в исходной базе таблица %s пуста -- тест ничего не проверил", table)
		}
	}

	// Схема целиком: индексы и прочее -- те же, что в живой базе.
	schema := func(d *sql.DB) []string {
		rows, err := d.Query(`SELECT type || ' ' || name || ' ' || COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	if got, want := schema(small), schema(d.SQL()); !slices.Equal(got, want) {
		t.Errorf("схема разошлась:\n got %v\nwant %v", got, want)
	}
}

func TestBuildPreservesAutoincrementSequences(t *testing.T) {
	d, src := liveDB(t)
	dst := filepath.Join(t.TempDir(), "small.db")
	if err := Build(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	small := openRO(t, dst)
	if got, want := dump(t, small, "sqlite_sequence"), dump(t, d.SQL(), "sqlite_sequence"); !slices.Equal(got, want) {
		t.Fatalf("sqlite_sequence: %v, ждали %v", got, want)
	}
	// Новый роутер после восстановления не получает id удалённого.
	res, err := small.Exec(`INSERT INTO users (nickname, token_hash, expected_exit_ip, awg_iface) VALUES ('new', 'h', '203.0.113.9', 'nwg0')`)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := res.LastInsertId(); id != 5 {
		t.Fatalf("id нового роутера %d, ждали 5 (счётчик сохранён)", id)
	}
	// Счётчик пустой таблицы events тоже на месте: новые события не
	// переиспользуют номера старых.
	res, err = small.Exec(`INSERT INTO events (user_id, check_name, status, ts) VALUES (1, 'ping', 'ok', '2026-10-02 00:00:00')`)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := res.LastInsertId(); id != 51 {
		t.Fatalf("id нового события %d, ждали 51", id)
	}
}

func TestBuildLeavesLiveDatabaseWritableAndUntouched(t *testing.T) {
	d, src := liveDB(t)
	before := dump(t, d.SQL(), "users")
	dst := filepath.Join(t.TempDir(), "small.db")
	if err := Build(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if after := dump(t, d.SQL(), "users"); !slices.Equal(before, after) {
		t.Fatal("сборка изменила живую базу")
	}
	if _, err := d.SQL().Exec(`INSERT INTO tg_state (key, value) VALUES ('after', '1')`); err != nil {
		t.Fatalf("живая база не пишется после сборки: %v", err)
	}
}

func TestBuildRefusesExistingDestination(t *testing.T) {
	_, src := liveDB(t)
	dst := filepath.Join(t.TempDir(), "small.db")
	if err := Build(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if err := Build(context.Background(), src, dst); err == nil {
		t.Fatal("сборка поверх существующего файла должна отказывать")
	}
	if err := Build(context.Background(), filepath.Join(t.TempDir(), "missing.db"), filepath.Join(t.TempDir(), "x.db")); err == nil {
		t.Fatal("сборка из несуществующей базы должна отказывать")
	}
}

// Триггеры и представления едут в схеме, но триггер не должен срабатывать
// на самом переносе; имена с кавычками и пробелами не ломают сборку.
func TestBuildCopiesTriggersAndViewsWithoutFiringThem(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.db")
	s, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Exec(`
CREATE TABLE "odd name" (id INTEGER PRIMARY KEY, v TEXT);
CREATE TABLE audit (n INTEGER);
CREATE TABLE plain (a TEXT, b BLOB) ;
CREATE TABLE norowid (k TEXT PRIMARY KEY, v INTEGER) WITHOUT ROWID;
CREATE TRIGGER trg AFTER INSERT ON "odd name" BEGIN INSERT INTO audit VALUES (NEW.id); END;
CREATE VIEW vw AS SELECT v FROM "odd name";
INSERT INTO "odd name" (v) VALUES ('x'), ('y');
INSERT INTO plain VALUES ('t', x'00ff'), (NULL, NULL);
INSERT INTO norowid VALUES ('k', 1);
PRAGMA user_version = 7;`); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "small.db")
	if err := Build(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	small := openRO(t, dst)
	for _, table := range []string{"odd name", "audit", "plain", "norowid"} {
		if got, want := dump(t, small, table), dump(t, s, table); !slices.Equal(got, want) {
			t.Errorf("%s: %v, ждали %v", table, got, want)
		}
	}
	if n := len(dump(t, small, "audit")); n != 2 {
		t.Fatalf("audit: %d строк, ждали 2 (триггер сработал на переносе?)", n)
	}
	var n int
	if err := small.QueryRow(`SELECT count(*) FROM sqlite_master WHERE (type = 'trigger' AND name = 'trg') OR (type = 'view' AND name = 'vw')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("триггер и представление не перенесены: %d, %v", n, err)
	}
	var uv int
	if err := small.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil || uv != 7 {
		t.Fatalf("user_version = %d, %v", uv, err)
	}
}
