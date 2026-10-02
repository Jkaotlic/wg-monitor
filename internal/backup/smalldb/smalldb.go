// Package smalldb собирает «малую» базу для ночного архива: схема живой базы
// целиком и данные всех таблиц, кроме тяжёлой истории. Малый архив обязан
// пролезать в Telegram (47 МБ), а боевая база -- 2,2 ГБ, почти всё в events.
package smalldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	_ "modernc.org/sqlite" // драйвер database/sql
)

// SkipTables -- тяжёлые исторические таблицы: схема в малую базу едет,
// строки -- нет. После восстановления экран пуст до первого отчёта агентов.
var SkipTables = []string{"events", "daily_soft_flaps", "awgm_ping_runs", "alert_messages"}

// CopyTables -- таблицы, чьи строки переносятся целиком. Сборка переносит
// любую таблицу не из SkipTables; этот список нужен сторожу
// TestEveryTableIsClassified: новая таблица в схеме обязана попасть в один
// из двух списков, иначе тест красный.
var CopyTables = []string{
	"users",
	"incident_state",
	"tg_state",
	"router_operators",
	"tunnel_config_origin",
	"router_repair_settings",
	"router_notify_mutes",
	"telegram_unreachable",
	"router_versions",
	"web_links",
	"router_update_reminders",
	"revive_intents",
	"revive_secrets",
	"router_credentials",
	"router_facts",
}

type schemaObject struct{ kind, name, sql string }

// Counts -- сколько роутеров, владельцев и операторов было в ИСТОЧНИКЕ в
// момент снимка. Known=false -- таблиц для счёта нет (урезанная база).
type Counts struct {
	Routers, Owners, Operators int
	Known                      bool
}

// Build собирает малую базу dst из живой src. dst не должен существовать.
// То же, что BuildCounted, без счётчиков.
func Build(ctx context.Context, src, dst string) error {
	_, err := BuildCounted(ctx, src, dst)
	return err
}

// BuildCounted собирает малую базу и возвращает счётчики источника,
// снятые в той же читающей транзакции, что и перенос данных.
//
// Живая база только читается, одной читающей транзакцией: в WAL она не
// мешает писателю и даёт согласованный срез всех таблиц. Строки таблиц из
// SkipTables не читаются вовсе. Триггеры создаются после переноса данных и
// на переносе не срабатывают.
func BuildCounted(ctx context.Context, src, dst string) (counts Counts, err error) {
	if info, serr := os.Stat(src); serr != nil {
		return counts, fmt.Errorf("source database: %w", serr)
	} else if !info.Mode().IsRegular() {
		return counts, errors.New("source database is not a regular file")
	}
	if _, serr := os.Lstat(dst); serr == nil {
		return counts, errors.New("small database destination already exists")
	} else if !errors.Is(serr, os.ErrNotExist) {
		return counts, serr
	}
	db, err := sql.Open("sqlite", dst)
	if err != nil {
		return counts, err
	}
	defer func() {
		if cerr := db.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dst)
			_ = os.Remove(dst + "-journal")
		}
	}()
	// ATTACH и транзакция живут на соединении -- держим ровно одно.
	conn, err := db.Conn(ctx)
	if err != nil {
		return counts, err
	}
	defer conn.Close()

	for _, q := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA foreign_keys = OFF`, // порядок переноса таблиц не важен
		`PRAGMA synchronous = OFF`,  // файл временный: упали -- соберём заново
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return counts, fmt.Errorf("%s: %w", q, err)
		}
	}
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS src`, src); err != nil {
		return counts, fmt.Errorf("attach source database: %w", err)
	}
	var userVersion int64
	if err := conn.QueryRowContext(ctx, `PRAGMA src.user_version`).Scan(&userVersion); err != nil {
		return counts, fmt.Errorf("read user_version: %w", err)
	}

	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return counts, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
		}
	}()

	objects, hasSequence, err := readSchema(ctx, conn)
	if err != nil {
		return counts, err
	}
	for _, o := range objects {
		if o.kind != "table" {
			continue
		}
		if _, err := conn.ExecContext(ctx, o.sql); err != nil {
			return counts, fmt.Errorf("create table %s: %w", o.name, err)
		}
	}
	for _, o := range objects {
		if o.kind != "table" || slices.Contains(SkipTables, o.name) {
			continue
		}
		q := quoteIdent(o.name)
		if _, err := conn.ExecContext(ctx, `INSERT INTO main.`+q+` SELECT * FROM src.`+q); err != nil { // #nosec G202 -- имя таблицы из sqlite_master, экранировано quoteIdent
			return counts, fmt.Errorf("copy table %s: %w", o.name, err)
		}
	}
	counts = countSource(ctx, conn)
	// Счётчики AUTOINCREMENT -- как в живой базе, включая пропущенные
	// таблицы: новые строки после восстановления не переиспользуют номера.
	if hasSequence {
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM main.sqlite_master WHERE name = 'sqlite_sequence'`).Scan(&n); err != nil {
			return counts, err
		}
		if n > 0 {
			if _, err := conn.ExecContext(ctx, `DELETE FROM main.sqlite_sequence`); err != nil {
				return counts, fmt.Errorf("reset sqlite_sequence: %w", err)
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO main.sqlite_sequence (name, seq) SELECT name, seq FROM src.sqlite_sequence`); err != nil {
				return counts, fmt.Errorf("copy sqlite_sequence: %w", err)
			}
		}
	}
	// Индексы после данных -- быстрее; представления и триггеры последними.
	for _, kind := range []string{"index", "view", "trigger"} {
		for _, o := range objects {
			if o.kind != kind {
				continue
			}
			if _, err := conn.ExecContext(ctx, o.sql); err != nil {
				return counts, fmt.Errorf("create %s %s: %w", o.kind, o.name, err)
			}
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return counts, fmt.Errorf("commit small database: %w", err)
	}
	committed = true
	if _, err := conn.ExecContext(ctx, `DETACH DATABASE src`); err != nil {
		return counts, fmt.Errorf("detach source database: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, userVersion)); err != nil {
		return counts, fmt.Errorf("set user_version: %w", err)
	}
	var check string
	if err := conn.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil {
		return counts, fmt.Errorf("integrity_check: %w", err)
	}
	if check != "ok" {
		return counts, fmt.Errorf("small database failed integrity_check: %s", check)
	}
	return counts, nil
}

func readSchema(ctx context.Context, conn *sql.Conn) (objects []schemaObject, hasSequence bool, err error) {
	rows, err := conn.QueryContext(ctx, `SELECT type, name, COALESCE(sql, '') FROM src.sqlite_master ORDER BY rowid`)
	if err != nil {
		return nil, false, fmt.Errorf("read source schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			return nil, false, err
		}
		if o.name == "sqlite_sequence" {
			hasSequence = true
		}
		// sqlite_% -- служебное (sqlite_sequence, автоиндексы): создаётся само.
		if strings.HasPrefix(o.name, "sqlite_") || o.sql == "" {
			continue
		}
		objects = append(objects, o)
	}
	return objects, hasSequence, rows.Err()
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// countSource считает роутеры, владельцев и операторов в src внутри уже
// открытой транзакции переноса. Ошибка (нет таблицы) -- Known=false.
func countSource(ctx context.Context, conn *sql.Conn) Counts {
	var c Counts
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM src.users`).Scan(&c.Routers); err != nil {
		return Counts{}
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(DISTINCT telegram_user_id) FROM src.users WHERE telegram_user_id IS NOT NULL AND telegram_user_id != 0`).Scan(&c.Owners); err != nil {
		return Counts{}
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM src.router_operators`).Scan(&c.Operators); err != nil {
		return Counts{}
	}
	c.Known = true
	return c
}
