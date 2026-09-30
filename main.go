// FK グラフから「切れる場所」を機械的に出す CLI(v1: MySQL 専用)。
//
// 手順: information_schema を 1 パス読む → 平行 FK を束ねる → CASCADE 縮約 →
// hub 除外 → 橋検出(Tarjan) → 橋ブロック木を出力。
// 出力は 3 方向: 今日切れる(橋・孤立) / 目指す境界(ブロック) / 人間が決める(hub)。
//
// habakiri(天羽々斬): 絡み合った大蛇(スパゲッティ FK)を斬るための剣。
// 実際に斬るのは人間で、このツールは斬る場所を測って示すところまで。
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var version = "0.1.0"

func main() {
	os.Exit(run())
}

func run() int {
	prog := filepath.Base(os.Args[0])
	fs := flag.NewFlagSet(prog, flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("HABAKIRI_DSN"),
		"DSN。MySQL (user:pass@tcp(host:3306)/dbname) または Postgres (postgres://user:pass@host:5432/dbname)。環境変数 HABAKIRI_DSN でも可")
	jsonOut := fs.Bool("json", false, "JSON で出力")
	mermaid := fs.String("mermaid", "", "Mermaid 図をこのファイルへ書き出す")
	hub := fs.Int("hub", 0, "hub 判定の次数閾値(0 = 自動: max(6, ノード数の 15%))")
	showVersion := fs.Bool("version", false, "バージョン表示")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `%s: FK グラフから分割可能なポイントを出す(MySQL / Postgres)

使い方:
  %s --dsn "user:pass@tcp(127.0.0.1:3306)/mydb" [--json] [--mermaid out.mmd] [--hub N]
  %s --dsn "postgres://user:pass@127.0.0.1:5432/mydb" ...

読み取り専用。MySQL は information_schema、Postgres は pg_catalog しか見ない。
`, prog, prog, prog)
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println(prog, version)
		return 0
	}
	if *dsn == "" {
		fs.Usage()
		return 2
	}

	// DSN のスキームでドライバを判別する。postgres:// / postgresql:// 以外は
	// go-sql-driver の DSN 形式とみなす(MySQL に URL スキームは無い)。
	driver, scan := "mysql", Scan
	if strings.HasPrefix(*dsn, "postgres://") || strings.HasPrefix(*dsn, "postgresql://") {
		driver, scan = "pgx", ScanPostgres
	}

	db, err := sql.Open(driver, *dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, prog+":", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	sc, err := scan(db)
	if err != nil {
		fmt.Fprintln(os.Stderr, prog+":", err)
		return 1
	}

	a := Analyze(sc, *hub)

	if *mermaid != "" {
		f, err := os.Create(*mermaid)
		if err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return 1
		}
		WriteMermaid(f, a)
		if err := f.Close(); err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return 1
		}
	}

	if *jsonOut {
		if err := WriteJSON(os.Stdout, a); err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return 1
		}
		return 0
	}
	WriteText(os.Stdout, a)
	return 0
}
