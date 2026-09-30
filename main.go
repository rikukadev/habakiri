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
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var version = "0.6.1"

func main() {
	os.Exit(run(filepath.Base(os.Args[0]), os.Args[1:], os.Stdout, os.Stderr))
}

// run は CLI 本体。引数と出力先を受け取る形にしてあるのは、ゴールデン回帰テスト
// (golden_test.go)が実バイナリと同じ経路を通れるようにするため。
func run(prog string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(prog, flag.ContinueOnError)
	fs.SetOutput(stderr)
	errln := func(a ...any) { _, _ = fmt.Fprintln(stderr, a...) }
	dsn := fs.String("dsn", os.Getenv("HABAKIRI_DSN"),
		"DSN。MySQL (user:pass@tcp(host:3306)/dbname) または Postgres (postgres://user:pass@host:5432/dbname)。環境変数 HABAKIRI_DSN でも可")
	railsDir := fs.String("rails", "", "Rails アプリのルート(または app/models)を静的に読む。DB 接続不要")
	yii1Dir := fs.String("yii1", "", "Yii 1.x アプリのルート(または protected/models)を静的に読む。DB 接続不要")
	schemaJSON := fs.String("schema-json", "", "--dump-schema で書き出したスキャン結果を DSN の代わりに読む(DB に繋げない環境へスキーマだけ持ち出して解析する)")
	graph := fs.String("graph", "", "グラフの見方: physical(DB が強制する FK のみ)/ logical(ORM の宣言のみ)/ combined(統合)。既定は入力に応じる(DB だけ → physical、静的ソースだけ → logical、併用 → combined)")
	compareGraphs := fs.Bool("compare-graphs", false, "Physical / Logical / Combined を同じ条件(hub・CASCADE 縮約・頂点集合を combined から固定)で解析して比べる。DB と静的ソースの併用が前提")
	showEvidence := fs.Bool("show-evidence", false, "各 FK の出自(証拠の位置・DB が強制しているか・NULL 許容・重みの理由)を text / JSON / HTML に出す")
	dumpSchema := fs.String("dump-schema", "", "スキャン結果(テーブルと FK)を JSON でこのファイルへ書き出す")
	jsonOut := fs.Bool("json", false, "JSON で出力")
	mermaid := fs.String("mermaid", "", "Mermaid 図をこのファイルへ書き出す")
	svgOut := fs.String("svg", "", "切る前の E-R 図(SVG)をこのファイルへ機械生成する")
	svgCutOut := fs.String("svg-cut", "", "切った後の E-R 図をこのファイルへ書き出す(--cut-level のレベルまで切る)")
	cutLevel := fs.Int("cut-level", 1, "切断レベル: 1=結果整合のみ(既定) / 2=存在保証込み / 3=最大分解")
	svgPart := fs.String("svg-partition", "", "分割案の図(グループ = コンテナ)をこのファイルへ書き出す")
	services := fs.Int("services", 0, "分割案のグループ数の希望(0 = モジュラリティ最大に任せる)")
	coocFile := fs.String("cooc", "", "同一 tx 書き込み共起のログ(MySQL general log / Postgres log / 中立形式)")
	coocWeight := fs.Bool("cooc-weight", true, "共起を分割グラフに算入する(false = レポートのみ。baseline 用の静的モード)")
	emitContract := fs.String("emit-contract", "", "橋の FK DROP マイグレーションのスケルトンをこのファイルへ生成")
	patternsFile := fs.String("patterns", "", "受け皿パターン語彙の差し替え(JSON: {\"1\": \"...\", \"2\": \"...\", \"3\": \"...\"})")
	churnDir := fs.String("churn", "", "git リポジトリからモデル変更頻度を採り、切り出し候補ランキングを出す(静的ソースと併用)")
	critFile := fs.String("criticality", "", "テーブル → 事故コストの JSON(候補ランキングの第 3 キー)")
	baseline := fs.String("baseline", "", "過去の --json 出力と比較し、結合の逆行(新規 FK ペア・hub 契約増・跨ぎ FK 増)があれば exit 3")
	d2Out := fs.String("d2", "", "D2 スクリプトをこのファイルへ書き出す(d2 out.d2 out.svg で描画)")
	htmlOut := fs.String("html", "", "図と切断計画をまとめた自己完結 HTML をこのファイルへ書き出す")
	hub := fs.Int("hub", 0, "hub 判定の次数閾値(0 = 自動: max(6, ノード数の 15%))")
	showVersion := fs.Bool("version", false, "バージョン表示")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, `%s: FK グラフから分割可能なポイントを出す(MySQL / Postgres)

使い方:
  %s --dsn "user:pass@tcp(127.0.0.1:3306)/mydb" [--json] [--mermaid out.mmd] [--hub N]
  %s --dsn "postgres://user:pass@127.0.0.1:5432/mydb" ...
  %s --rails /path/to/railsapp    # ActiveRecord の宣言を静的に読む(DB 不要)
  %s --yii1  /path/to/yii1app     # Yii1 の relations() を静的に読む(DB 不要)
  %s --dsn ... --yii1 /path/to/app  # 併用: DB の FK と宣言を合流(同じ関係は 1 本、証拠が 2 件)

分析は読み取り専用。MySQL は information_schema、Postgres は pg_catalog しか見ない。
`, prog, prog, prog, prog, prog, prog)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, prog, version)
		return 0
	}
	// ソースは 2 系統。DB(Physical)と静的ソース(Logical)は 1 つずつ併用でき、
	// その場合は証拠を合流させる(merge.go)。同じ系統の中では 1 つだけ。
	if *dsn != "" && *schemaJSON != "" {
		errln(prog + ": --dsn と --schema-json はどちらか 1 つ")
		return 2
	}
	if *railsDir != "" && *yii1Dir != "" {
		errln(prog + ": --rails と --yii1 はどちらか 1 つ")
		return 2
	}
	hasPhysical := *dsn != "" || *schemaJSON != ""
	hasLogical := *railsDir != "" || *yii1Dir != ""
	if !hasPhysical && !hasLogical {
		fs.Usage()
		return 2
	}

	var phys, logic *ScanResult
	var err error
	switch {
	case *schemaJSON != "":
		phys, err = LoadSchemaJSON(*schemaJSON)
	case *dsn != "":
		// DSN のスキームでドライバを判別する。postgres:// / postgresql:// 以外は
		// go-sql-driver の DSN 形式とみなす(MySQL に URL スキームは無い)。
		driver, scan := "mysql", Scan
		if strings.HasPrefix(*dsn, "postgres://") || strings.HasPrefix(*dsn, "postgresql://") {
			driver, scan = "pgx", ScanPostgres
		}
		var db *sql.DB
		db, err = sql.Open(driver, *dsn)
		if err != nil {
			errln(prog+":", err)
			return 1
		}
		defer func() { _ = db.Close() }()
		phys, err = scan(db)
	}
	if err != nil {
		errln(prog+":", err)
		return 1
	}
	switch {
	case *railsDir != "":
		logic, err = ScanRails(*railsDir)
	case *yii1Dir != "":
		logic, err = ScanYii1(*yii1Dir)
	}
	if err != nil {
		errln(prog+":", err)
		return 1
	}
	if *dumpSchema != "" {
		if phys == nil {
			errln(prog + ": --dump-schema は DB スキャン(--dsn)の結果を書き出す。静的ソースだけでは使えない")
			return 2
		}
		if err := DumpSchemaJSON(*dumpSchema, phys); err != nil {
			errln(prog+":", err)
			return 1
		}
	}
	if *compareGraphs && (phys == nil || logic == nil) {
		errln(prog + ": --compare-graphs は DB(--dsn / --schema-json)と静的ソース(--rails / --yii1)の併用が前提。片方だけでは比べる相手が無い")
		return 2
	}
	var sc *ScanResult
	switch {
	case phys != nil && logic != nil:
		sc = MergeScans(phys, logic)
	case phys != nil:
		sc = phys
	default:
		sc = logic
	}
	merged := sc // 射影前(比較はここから 3 つの見方を作る)
	// --graph を明示したときだけ射影する。既定は入力に応じた見方で、
	// それは射影なしの sc そのもの(単独ソースの出力を 1 バイトも変えない)。
	if *graph != "" {
		kind, err := ParseGraphKind(*graph)
		if err != nil {
			errln(prog+":", err)
			return 2
		}
		sc = Project(sc, kind, *coocWeight)
	}
	sc.ShowEvidence = *showEvidence
	if *coocFile != "" {
		cooc, err := LoadCooc(*coocFile)
		if err != nil {
			errln(prog+":", err)
			return 1
		}
		sc.Cooc = cooc
		sc.CoocNoWeight = !*coocWeight
	}

	if *patternsFile != "" {
		raw, err := os.ReadFile(*patternsFile)
		if err != nil {
			errln(prog+":", err)
			return 1
		}
		var pm map[string]string
		if err := json.Unmarshal(raw, &pm); err != nil {
			errln(prog+":", err)
			return 1
		}
		for k, v := range pm {
			if lv, err := strconv.Atoi(k); err == nil {
				cutPatterns[lv] = v
			}
		}
	}

	a := Analyze(sc, *hub)
	if *compareGraphs {
		a.Comparison = BuildComparison(PrepareComparison(merged, *hub, *services, *coocWeight))
	}
	if *services > 0 && a.Partition != nil {
		a.Partition.SelectLevel(*services)
	}
	if *churnDir != "" || *critFile != "" {
		var tChurn, tFiles map[string]int // nil = --churn 無し(全ユニット未計測)
		if *churnDir != "" {
			if len(sc.FileTables) == 0 {
				errln(prog + ": --churn は静的ソース(--rails/--yii1)と併用してください(ファイル→テーブル対応が要る)")
				return 2
			}
			fc, err := LoadChurn(*churnDir)
			if err != nil {
				errln(prog+":", err)
				return 1
			}
			tChurn, tFiles = tableChurn(sc.FileTables, fc)
		}
		tCrit := map[string]float64{}
		if *critFile != "" {
			var err error
			tCrit, err = LoadCriticality(*critFile)
			if err != nil {
				errln(prog+":", err)
				return 1
			}
		}
		a.Candidates = BuildCandidates(a, tChurn, tFiles, tCrit)
	}

	writeFile := func(path string, write func(f *os.File)) bool {
		if path == "" {
			return true
		}
		f, err := os.Create(path)
		if err != nil {
			errln(prog+":", err)
			return false
		}
		write(f)
		if err := f.Close(); err != nil {
			errln(prog+":", err)
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
			errln(prog+":", err)
			return false
		}
		if err := render(f, a); err != nil {
			errln(prog+":", err)
			return false
		}
		if err := f.Close(); err != nil {
			errln(prog+":", err)
			return false
		}
		return true
	}
	writeSVGCutAtLevel := func(w io.Writer, a *Analysis) error { return WriteSVGLevel(w, a, *cutLevel) }
	if !writeSVGFile(*svgOut, WriteSVG) || !writeSVGFile(*svgCutOut, writeSVGCutAtLevel) ||
		!writeSVGFile(*svgPart, WriteSVGPartition) {
		return 1
	}
	if *emitContract != "" {
		dialect := "static"
		if sc.Dialect != "" {
			dialect = sc.Dialect
		}
		if !writeFile(*emitContract, func(f *os.File) { WriteContract(f, a, dialect) }) {
			return 1
		}
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
			errln(prog+":", err)
			return 1
		}
		if CompareBaseline(stdout, prev, a) {
			return 3
		}
		return 0
	}

	if *jsonOut {
		if err := WriteJSON(stdout, a); err != nil {
			errln(prog+":", err)
			return 1
		}
		return 0
	}
	WriteText(stdout, a)
	return 0
}
