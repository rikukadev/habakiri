// scan_postgres.go: Postgres の pg_catalog から FK グラフの材料を読む。
// information_schema を使わないのは、Postgres のそれが遅く、複合 FK の表現も
// 扱いにくいため(scan.go 冒頭のメモの通り)。読み取り専用。
//
// 対象は current_schema()(通常 public)。別スキーマを見るときは DSN の
// search_path で切り替える(例: ...?search_path=myschema)。
package main

import (
	"database/sql"
	"fmt"
	"sort"
)

// pg_constraint.confdeltype の 1 文字を MySQL 側と同じ語彙へ写像する。
func pgDeleteRule(c string) string {
	switch c {
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "r":
		return "RESTRICT"
	case "d":
		return "SET DEFAULT"
	default: // "a"
		return "NO ACTION"
	}
}

// ScanPostgres は current_schema() を読む。
func ScanPostgres(db *sql.DB) (*ScanResult, error) {
	var schema sql.NullString
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	if !schema.Valid || schema.String == "" {
		return nil, fmt.Errorf("current_schema() が空です(DSN の search_path を確認)")
	}

	res := &ScanResult{Schema: schema.String}

	// 全テーブル(FK を持たない孤立テーブルも報告対象なので先に取る)。
	rows, err := db.Query(`
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind = 'r'
		ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		res.Tables = append(res.Tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// FK。複合キーは constraint 単位で 1 本に畳む(scan.go と同じ形に揃える)。
	// unnest ... WITH ORDINALITY で conkey(子側の列番号配列)を列に展開する。
	fkRows, err := db.Query(`
		SELECT con.conname,
		       child.relname,
		       pn.nspname,
		       parent.relname,
		       con.confdeltype::text,
		       a.attname,
		       a.attnotnull
		FROM pg_constraint con
		JOIN pg_class child      ON child.oid = con.conrelid
		JOIN pg_namespace cn     ON cn.oid = child.relnamespace
		JOIN pg_class parent     ON parent.oid = con.confrelid
		JOIN pg_namespace pn     ON pn.oid = parent.relnamespace
		CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a      ON a.attrelid = con.conrelid AND a.attnum = k.attnum
		WHERE con.contype = 'f' AND cn.nspname = current_schema()
		ORDER BY child.relname, con.conname, k.ord`)
	if err != nil {
		return nil, fmt.Errorf("fks: %w", err)
	}
	defer func() { _ = fkRows.Close() }()

	byConstraint := map[string]*FK{}
	var order []string
	for fkRows.Next() {
		var name, child, pschema, ptable, delType, col string
		var notNull bool
		if err := fkRows.Scan(&name, &child, &pschema, &ptable, &delType, &col, &notNull); err != nil {
			return nil, err
		}
		key := child + "\x00" + name // Postgres の constraint 名はテーブル毎に一意
		fk, ok := byConstraint[key]
		if !ok {
			fk = &FK{Constraint: name, ChildTable: child, ParentTable: ptable,
				DeleteRule: pgDeleteRule(delType), AllNotNull: true}
			if pschema != res.Schema {
				fk.ParentSchema = pschema
			}
			byConstraint[key] = fk
			order = append(order, key)
		}
		fk.ChildCols = append(fk.ChildCols, col)
		if !notNull {
			fk.AllNotNull = false
		}
	}
	if err := fkRows.Err(); err != nil {
		return nil, err
	}

	sort.Strings(order)
	for _, k := range order {
		fk := byConstraint[k]
		if fk.ParentSchema != "" {
			res.CrossFKs = append(res.CrossFKs, *fk)
			continue
		}
		res.FKs = append(res.FKs, *fk)
	}
	return res, nil
}
