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
	"io"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var version = "0.4.0"

func main() {
	os.Exit(run())
}

func run() int {
	prog := filepath.Base(os.Args[0])
	fs := flag.NewFlagSet(prog, flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("HABAKIRI_DSN"),
		"DSN。MySQL (user:pass@tcp(host:3306)/dbname) または Postgres (postgres://user:pass@host:5432/dbname)。環境変数 HABAKIRI_DSN でも可")
	railsDir := fs.String("rails", "", "Rails アプリのルート(または app/models)を静的に読む。DB 接続不要")
	yii1Dir := fs.String("yii1", "", "Yii 1.x アプリのルート(または protected/models)を静的に読む。DB 接続不要")
	jsonOut := fs.Bool("json", false, "JSON で出力")
	mermaid := fs.String("mermaid", "", "Mermaid 図をこのファイルへ書き出す")
	svgOut := fs.String("svg", "", "切る前の E-R 図(SVG)をこのファイルへ機械生成する")
	svgCutOut := fs.String("svg-cut", "", "切った後の E-R 図をこのファイルへ書き出す(--cut-level のレベルまで切る)")
	cutLevel := fs.Int("cut-level", 1, "切断レベル: 1=結果整合のみ(既定) / 2=存在保証込み / 3=最大分解")
	svgPart := fs.String("svg-partition", "", "分割案の図(グループ = コンテナ)をこのファイルへ書き出す")
	services := fs.Int("services", 0, "分割案のグループ数の希望(0 = モジュラリティ最大に任せる)")
	coocFile := fs.String("cooc", "", "同一 tx 書き込み共起のログ(MySQL general log / Postgres log / 中立形式)")
	coocWeight := fs.Bool("cooc-weight", true, "共起を分割グラフに算入する(false = レポートのみ。baseline 用の静的モード)")
	baseline := fs.String("baseline", "", "過去の --json 出力と比較し、結合の逆行(新規 FK ペア・hub 契約増・跨ぎ FK 増)があれば exit 3")
	d2Out := fs.String("d2", "", "D2 スクリプトをこのファイルへ書き出す(d2 out.d2 out.svg で描画)")
	htmlOut := fs.String("html", "", "図と切断計画をまとめた自己完結 HTML をこのファイルへ書き出す")
	hub := fs.Int("hub", 0, "hub 判定の次数閾値(0 = 自動: max(6, ノード数の 15%))")
	showVersion := fs.Bool("version", false, "バージョン表示")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `%s: FK グラフから分割可能なポイントを出す(MySQL / Postgres)

使い方:
  %s --dsn "user:pass@tcp(127.0.0.1:3306)/mydb" [--json] [--mermaid out.mmd] [--hub N]
  %s --dsn "postgres://user:pass@127.0.0.1:5432/mydb" ...
  %s --rails /path/to/railsapp    # ActiveRecord の宣言を静的に読む(DB 不要)
  %s --yii1  /path/to/yii1app     # Yii1 の relations() を静的に読む(DB 不要)

読み取り専用。MySQL は information_schema、Postgres は pg_catalog しか見ない。
`, prog, prog, prog, prog, prog)
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println(prog, version)
		return 0
	}
	sources := 0
	for _, s := range []string{*dsn, *railsDir, *yii1Dir} {
		if s != "" {
			sources++
		}
	}
	if sources != 1 {
		if sources > 1 {
			fmt.Fprintln(os.Stderr, prog+": --dsn / --rails / --yii1 はどれか 1 つだけ")
		} else {
			fs.Usage()
		}
		return 2
	}

	var sc *ScanResult
	var err error
	switch {
	case *railsDir != "":
		sc, err = ScanRails(*railsDir)
	case *yii1Dir != "":
		sc, err = ScanYii1(*yii1Dir)
	default:
		// DSN のスキームでドライバを判別する。postgres:// / postgresql:// 以外は
		// go-sql-driver の DSN 形式とみなす(MySQL に URL スキームは無い)。
		driver, scan := "mysql", Scan
		if strings.HasPrefix(*dsn, "postgres://") || strings.HasPrefix(*dsn, "postgresql://") {
			driver, scan = "pgx", ScanPostgres
		}
		var db *sql.DB
		db, err = sql.Open(driver, *dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return 1
		}
		defer func() { _ = db.Close() }()
		sc, err = scan(db)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, prog+":", err)
		return 1
	}
	if *coocFile != "" {
		cooc, err := LoadCooc(*coocFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return 1
		}
		sc.Cooc = cooc
		sc.CoocNoWeight = !*coocWeight
	}

	a := Analyze(sc, *hub)
	if *services > 0 && a.Partition != nil {
		a.Partition.SelectLevel(*services)
	}

	writeFile := func(path string, write func(f *os.File)) bool {
		if path == "" {
			return true
		}
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return false
		}
		write(f)
		if err := f.Close(); err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return false
		}
		return true
	}
	if !writeFile(*mermaid, func(f *os.File) { WriteMermaid(f, a) }) {
		return 1
	}
	writeSVGFile := func(path string, render func(io.Writer, *Analysis) error) bool {
		if path == "" {
			return true
		}
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return false
		}
		if err := render(f, a); err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return false
		}
		if err := f.Close(); err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return false
		}
		return true
	}
	writeSVGCutAtLevel := func(w io.Writer, a *Analysis) error { return WriteSVGLevel(w, a, *cutLevel) }
	if !writeSVGFile(*svgOut, WriteSVG) || !writeSVGFile(*svgCutOut, writeSVGCutAtLevel) ||
		!writeSVGFile(*svgPart, WriteSVGPartition) {
		return 1
	}
	if !writeFile(*d2Out, func(f *os.File) { WriteD2(f, a) }) {
		return 1
	}
	if !writeFile(*htmlOut, func(f *os.File) { WriteHTML(f, a) }) {
		return 1
	}

	if *baseline != "" {
		prev, err := LoadBaseline(*baseline)
		if err != nil {
			fmt.Fprintln(os.Stderr, prog+":", err)
			return 1
		}
		if CompareBaseline(os.Stdout, prev, a) {
			return 3
		}
		return 0
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
