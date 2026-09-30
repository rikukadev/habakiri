// scan.go: MySQL の information_schema から FK グラフの材料を 1 パスで読む。
// 読み取り専用。対象は DSN が指すスキーマ(DATABASE())のみ。
// Postgres 版は scan_postgres.go(pg_catalog を読む)。
package main

import (
	"database/sql"
	"fmt"
	"sort"
)

// FK は 1 本の外部キー制約(複合キーは 1 本に畳む)。
type FK struct {
	Constraint   string   `json:"constraint"`
	ChildTable   string   `json:"child_table"`
	ChildCols    []string `json:"child_columns"`
	ParentSchema string   `json:"parent_schema,omitempty"` // 空 = 同一スキーマ
	ParentTable  string   `json:"parent_table"`
	DeleteRule   string   `json:"delete_rule"` // CASCADE / SET NULL / RESTRICT / NO ACTION
	// AllNotNull: 子側の列が**すべて** NOT NULL なら true。1 列でも NULL 許容なら
	// 「参照が無い状態が存在できる」= 存在依存ではない、として弱い結合に分類する。
	AllNotNull bool `json:"all_not_null"`
}

// ScanResult はスキャンの生データ。
type ScanResult struct {
	Schema   string   `json:"schema"`
	Tables   []string `json:"tables"`
	FKs      []FK     `json:"fks"`
	CrossFKs []FK     `json:"cross_schema_fks"` // 別スキーマの親を指す FK(DDL ロックが跨ぐ)
}

// Scan は DSN のスキーマを読む。
func Scan(db *sql.DB) (*ScanResult, error) {
	var schema string
	if err := db.QueryRow(`SELECT DATABASE()`).Scan(&schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	if schema == "" {
		return nil, fmt.Errorf("DSN にデータベース名が入っていません(例: user:pass@tcp(host:3306)/dbname)")
	}

	res := &ScanResult{Schema: schema}

	// 全テーブル(FK を持たない孤立テーブルも報告対象なので先に取る)。
	rows, err := db.Query(`
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = ? AND table_type = 'BASE TABLE'
		ORDER BY table_name`, schema)
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

	// FK。複合キーは constraint 単位で 1 本に畳む。子列の NULL 許容も同時に取る。
	// referenced_table_schema を落とさない — 別スキーマへの FK は DDL ロックが
	// スキーマを跨ぐ(このツールを書く動機になった現象)ので別枠で出す。
	fkRows, err := db.Query(`
		SELECT kcu.constraint_name, kcu.table_name, kcu.column_name,
		       kcu.referenced_table_schema, kcu.referenced_table_name,
		       rc.delete_rule, c.is_nullable
		FROM information_schema.key_column_usage kcu
		JOIN information_schema.referential_constraints rc
		  ON rc.constraint_schema = kcu.constraint_schema
		 AND rc.constraint_name  = kcu.constraint_name
		 AND rc.table_name       = kcu.table_name
		JOIN information_schema.columns c
		  ON c.table_schema = kcu.table_schema
		 AND c.table_name   = kcu.table_name
		 AND c.column_name  = kcu.column_name
		WHERE kcu.constraint_schema = ?
		  AND kcu.referenced_table_name IS NOT NULL
		ORDER BY kcu.constraint_name, kcu.ordinal_position`, schema)
	if err != nil {
		return nil, fmt.Errorf("fks: %w", err)
	}
	defer func() { _ = fkRows.Close() }()

	byConstraint := map[string]*FK{}
	var order []string
	for fkRows.Next() {
		var name, child, col, pschema, ptable, rule, nullable string
		if err := fkRows.Scan(&name, &child, &col, &pschema, &ptable, &rule, &nullable); err != nil {
			return nil, err
		}
		key := child + "\x00" + name // constraint 名はテーブル毎に一意(MySQL はスキーマ内一意だが安全側)
		fk, ok := byConstraint[key]
		if !ok {
			fk = &FK{Constraint: name, ChildTable: child, ParentTable: ptable,
				DeleteRule: rule, AllNotNull: true}
			if pschema != schema {
				fk.ParentSchema = pschema
			}
			byConstraint[key] = fk
			order = append(order, key)
		}
		fk.ChildCols = append(fk.ChildCols, col)
		if nullable == "YES" {
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
