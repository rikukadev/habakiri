// source_yii1.go: Yii 1.x の CActiveRecord モデルを静的に読む。
// relations() の配列(self::BELONGS_TO / HAS_MANY / HAS_ONE / MANY_MANY)が対象。
// FK 列名は宣言に明示されているのでインフレクタは不要。
//
// Yii1 の relations には必須性もカスケードも宣言できないため、NULL 許容は
// Unknown(重み 1 は「弱いと判明した」のではなく情報不足時の暫定値)。
// 実務のカスケードは beforeDelete() の手書き削除に現れるので、
// callback を持つモデルの他モデル言及は注記として出す(エッジにはしない)。
// DB スキャンとの併用を推奨。
//
// テーブル名は Yii1 と同じ規則: tableName() の {{name}} には設定ファイル
// (protected/config/main.php)の tablePrefix を前置し、直書きの名前はそのまま。
// モデルは protected 配下の models ディレクトリを全部読む(modules/*/models を含む)。
// 相手のモデルが見つからない関連は FK にしない(クラス名をテーブル名にしない)。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type yiiModel struct {
	path      string
	class     string
	tableName string // tableName() の戻り(無ければクラス名)
	fileSrc   string
	relations []yiiRelation
}

type yiiRelation struct {
	name   string // relations() の宣言名(with の解決に使う)
	kind   string // BELONGS_TO / HAS_MANY / HAS_ONE / MANY_MANY
	target string // 相手モデルのクラス名
	fkSpec string // FK 列(カンマ区切り)、MANY_MANY は 'join(col1, col2)'
	line   int    // 宣言の行番号(Evidence の Origin)
}

var (
	reYiiClass = regexp.MustCompile(`class\s+(\w+)\s+extends\s+\w*ActiveRecord\w*`)
	reYiiTable = regexp.MustCompile(`function\s+tableName\s*\(\)[^{]*\{[^}]*?return\s+['"]([^'"]+)['"]`)
	reYiiRel   = regexp.MustCompile(`['"](\w+)['"]\s*=>\s*array\s*\(\s*self::(BELONGS_TO|HAS_MANY|HAS_ONE|MANY_MANY)\s*,\s*['"](\w+)['"]\s*,\s*['"]([^'"]+)['"]`)
	reYiiMM    = regexp.MustCompile(`^\s*([\w.{}]+)\s*\(\s*([\w]+)\s*,\s*([\w]+)\s*\)\s*$`)
	reYiiCb    = regexp.MustCompile(`function\s+(beforeSave|afterSave|beforeDelete|afterDelete|beforeValidate|afterValidate|beforeFind|afterFind|afterConstruct|behaviors)\s*\(`)
)

// reYiiTablePrefix: 設定ファイルの db コンポーネントの 'tablePrefix' => 'tbl_'。
var reYiiTablePrefix = regexp.MustCompile(`['"]tablePrefix['"]\s*=>\s*['"]([^'"]*)['"]`)

// yiiTableName は {{x}} を剥がし、tablePrefix を前置する(Yii1 の CDbConnection と
// 同じく、{{}} で書かれた名前にだけ付ける。tbl_x の直書きはそのまま)。
func yiiTableName(raw, prefix string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}") {
		return prefix + strings.TrimSuffix(strings.TrimPrefix(s, "{{"), "}}")
	}
	return s
}

// yiiLayout は Yii1 アプリの読み取り範囲。protected が分かればその配下の
// models ディレクトリを全部読み、設定ファイルも探す。分からなければ dir 直下だけ。
func yiiLayout(dir string) (protected, modelsDir string) {
	for _, cand := range []string{filepath.Join(dir, "protected"), dir} {
		if fi, err := os.Stat(filepath.Join(cand, "models")); err == nil && fi.IsDir() {
			return cand, ""
		}
	}
	return "", dir
}

// yiiReadPrefix は設定ファイルの tablePrefix を読む(無ければ空)。
func yiiReadPrefix(protected string) string {
	if protected == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(protected, "config", "main.php"))
	if err != nil {
		return ""
	}
	if m := reYiiTablePrefix.FindSubmatch(raw); m != nil {
		return string(m[1])
	}
	return ""
}

// ScanYii1 は Yii1 アプリ(protected/models など)を読む。
func ScanYii1(dir string) (*ScanResult, error) {
	protected, modelsDir := yiiLayout(dir)
	prefix := yiiReadPrefix(protected)
	walkRoot := modelsDir
	if protected != "" {
		walkRoot = protected
		modelsDir = filepath.Join(protected, "models")
	}

	var files []string
	err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".php") {
			return nil
		}
		// protected 配下では models ディレクトリの中だけ(modules/*/models を含む)
		if protected != "" && !strings.Contains(filepath.ToSlash(path), "/models/") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("models: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s に .php が見つかりません(Yii1 アプリのルートか protected/models を指定)", modelsDir)
	}
	sort.Strings(files)

	models := map[string]*yiiModel{}
	var dupes []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		src := string(raw)
		cm := reYiiClass.FindStringSubmatch(src)
		if cm == nil {
			continue
		}
		if prev, dup := models[cm[1]]; dup {
			// Yii1 のクラス名はグローバル。同名が 2 つあると実行時にどちらかしか
			// 読まれない。先に見つけた方(パス順)を採り、注記する。
			dupes = append(dupes, fmt.Sprintf("%s(%s と %s)", cm[1], sourceOrigin(dir, prev.path, 0), sourceOrigin(dir, f, 0)))
			continue
		}
		m := &yiiModel{class: cm[1], tableName: cm[1], fileSrc: src, path: f}
		if tm := reYiiTable.FindStringSubmatch(src); tm != nil {
			m.tableName = yiiTableName(tm[1], prefix)
		}
		for _, loc := range reYiiRel.FindAllStringSubmatchIndex(src, -1) {
			m.relations = append(m.relations, yiiRelation{
				name: src[loc[2]:loc[3]], kind: src[loc[4]:loc[5]],
				target: src[loc[6]:loc[7]], fkSpec: src[loc[8]:loc[9]],
				line: strings.Count(src[:loc[0]], "\n") + 1})
		}
		models[m.class] = m
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("%s に CActiveRecord 系のモデルが見つかりません", modelsDir)
	}

	res := yiiToScan(dir, models, prefix)
	if prefix != "" {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"yii1 設定の tablePrefix %q を {{...}} で書かれたテーブル名に前置した(protected/config/main.php)", prefix))
	}
	if len(dupes) > 0 {
		res.Notes = append(res.Notes, "yii1: 同名のモデルクラスが複数ある — パス順で先のものを採った: "+strings.Join(dupes, ", "))
	}
	res.FileTables = map[string]string{}
	for _, m := range models {
		if m.path != "" {
			res.FileTables[m.path] = m.tableName
		}
	}
	return res, nil
}

func yiiToScan(dir string, models map[string]*yiiModel, prefix string) *ScanResult {
	res := &ScanResult{Schema: "yii1:" + filepath.Base(strings.TrimRight(dir, "/"))}
	tables := map[string]bool{}
	classes := make([]string, 0, len(models))
	for c := range models {
		classes = append(classes, c)
		tables[models[c].tableName] = true
	}
	sort.Strings(classes)

	// tableOf は相手モデルのテーブル名。見つからなければ ok=false —
	// クラス名をテーブル名にすると、読めていないモジュールや拡張のモデルが
	// 実在しないテーブル(User 等)への偽の関係になる。
	tableOf := func(class string) (string, bool) {
		if m, ok := models[class]; ok {
			return m.tableName, true
		}
		return "", false
	}
	var unresolved []string
	unknown := func(cc string, r yiiRelation) {
		unresolved = append(unresolved, fmt.Sprintf("%s.%s → %s", cc, r.name, r.target))
	}

	// BELONGS_TO と HAS_MANY/HAS_ONE は同じエッジの両側宣言なので、child+parent+cols で重複排除。
	// 両側の宣言はどちらも同じ関係の証拠なので、FK は 1 本のまま Evidence を足す。
	seen := map[string]int{} // key → res.FKs の位置
	// 自己参照(parent_id の木構造など)も作る。DB スキャンは自己参照 FK を持つので、
	// 捨てると Edge Diff で「DB にだけある関係」に見える。分割に効かないのは
	// グラフ層(BuildEdges)が落とすため。
	addFK := func(child, parent string, cols []string, constraint string, m *yiiModel, r yiiRelation) {
		ev := Evidence{Source: SourceYii1, Origin: sourceOrigin(dir, m.path, r.line),
			Constraint: constraint, DeleteRule: "NO ACTION", Nullable: NullableUnknown}
		key := child + "\x00" + parent + "\x00" + strings.Join(cols, ",")
		if i, ok := seen[key]; ok {
			res.FKs[i].Evidences = append(res.FKs[i].Evidences, ev)
			return
		}
		seen[key] = len(res.FKs)
		res.FKs = append(res.FKs, FK{
			Constraint: constraint, ChildTable: child, ChildCols: cols,
			ParentTable: parent, DeleteRule: "NO ACTION", AllNotNull: false,
			Nullable: NullableUnknown, Evidences: []Evidence{ev},
		})
		tables[child], tables[parent] = true, true
	}

	splitCols := func(s string) []string {
		var out []string
		for _, c := range strings.Split(s, ",") {
			if c = strings.TrimSpace(c); c != "" {
				out = append(out, c)
			}
		}
		return out
	}

	for _, cc := range classes {
		m := models[cc]
		for _, r := range m.relations {
			switch r.kind {
			case "BELONGS_TO":
				target, ok := tableOf(r.target)
				if !ok {
					unknown(cc, r)
					continue
				}
				addFK(m.tableName, target, splitCols(r.fkSpec),
					fmt.Sprintf("yii1:%s→%s", cc, r.target), m, r)
			case "HAS_MANY", "HAS_ONE":
				target, ok := tableOf(r.target)
				if !ok {
					unknown(cc, r)
					continue
				}
				addFK(target, m.tableName, splitCols(r.fkSpec),
					fmt.Sprintf("yii1:%s→%s", r.target, cc), m, r)
			case "MANY_MANY":
				mm := reYiiMM.FindStringSubmatch(r.fkSpec)
				if mm == nil {
					res.Notes = append(res.Notes,
						fmt.Sprintf("%s の MANY_MANY '%s' が 'join(col1, col2)' 形式でない — 読めなかった", cc, r.fkSpec))
					continue
				}
				join := yiiTableName(mm[1], prefix)
				// 中間テーブルから両側への FK を合成する。相手が見つからなければ
				// 自分の側だけ(中間テーブルとこのモデルの関係は宣言から確か)
				addFK(join, m.tableName, []string{mm[2]}, fmt.Sprintf("yii1:%s(mm)", join), m, r)
				target, ok := tableOf(r.target)
				if !ok {
					unknown(cc, r)
					continue
				}
				addFK(join, target, []string{mm[3]}, fmt.Sprintf("yii1:%s(mm)", join), m, r)
			}
		}
	}

	tableSet := map[string]bool{}
	for t := range tables {
		tableSet[t] = true
	}
	if len(unresolved) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"[判定不能] 相手のモデルが見つからない関連 %d 件は FK にしていない(読めていないモジュール・拡張のモデル等。クラス名をテーブル名にすると偽の関係になる): %s",
			len(unresolved), strings.Join(unresolved, ", ")))
	}

	// 手書きカスケード: beforeDelete / afterDelete の本文内の削除呼び出し。
	// Yii1 は relations にカスケードを宣言できないので、ここが唯一の証拠源。
	reCbDelete := regexp.MustCompile(`function\s+(beforeDelete|afterDelete)\s*\([^)]*\)\s*\{`)
	reDelModel := regexp.MustCompile(`(\w+)::model\(\)->delete(?:All|AllByAttributes|ByPk)?\(`)
	reDelRel := regexp.MustCompile(`\$this->(\w+)->delete\(`)
	for _, cc := range classes {
		m := models[cc]
		for _, loc := range reCbDelete.FindAllStringSubmatchIndex(m.fileSrc, -1) {
			body := braceBody(m.fileSrc, loc[1]-1)
			targets := map[string]bool{}
			for _, d := range reDelModel.FindAllStringSubmatch(body, -1) {
				if d[1] != cc {
					targets[d[1]] = true
				}
			}
			for _, d := range reDelRel.FindAllStringSubmatch(body, -1) {
				for _, r := range m.relations {
					if r.name == d[1] && r.target != cc {
						targets[r.target] = true
					}
				}
			}
			if len(targets) == 0 {
				continue
			}
			var names []string
			for t := range targets {
				names = append(names, t)
				if tt, ok := tableOf(t); ok {
					res.Suspects = append(res.Suspects, Suspect{
						FromTable: m.tableName, ToTable: tt, Strong: true})
				}
			}
			sort.Strings(names)
			res.Notes = append(res.Notes, fmt.Sprintf(
				"[強] %s: beforeDelete/afterDelete 内で %s を削除 — 手書きカスケード(実質ライフサイクル共有。切らない候補)",
				cc, strings.Join(names, ", ")))
		}
	}

	// with 参照(read 側の暗黙結合)は [弱]。
	reWithArr := regexp.MustCompile(`['"]with['"]\s*=>\s*(?:array\(|\[)([^)\]]*)`)
	reWithCall := regexp.MustCompile(`->with\(\s*['"]([\w.]+)`)
	for _, cc := range classes {
		m := models[cc]
		names := map[string]bool{}
		for _, w := range reWithArr.FindAllStringSubmatch(m.fileSrc, -1) {
			for _, q := range regexp.MustCompile(`['"](\w+)`).FindAllStringSubmatch(w[1], -1) {
				names[q[1]] = true
			}
		}
		for _, w := range reWithCall.FindAllStringSubmatch(m.fileSrc, -1) {
			names[strings.SplitN(w[1], ".", 2)[0]] = true
		}
		var hits []string
		for n := range names {
			for _, r := range m.relations {
				if r.name == n && r.target != cc {
					hits = append(hits, r.target)
					if tt, ok := tableOf(r.target); ok {
						res.Suspects = append(res.Suspects, Suspect{
							FromTable: m.tableName, ToTable: tt, Strong: false})
					}
				}
			}
		}
		if len(hits) > 0 {
			sort.Strings(hits)
			res.Notes = append(res.Notes, fmt.Sprintf(
				"[弱] %s: with で %s を読む(read 側の暗黙結合)", cc, strings.Join(hits, ", ")))
		}
	}

	// 宣言外の他モデル言及は全モデルで注記する(エッジにはしない)。
	// Yii1 は relations にカスケードを書けず beforeDelete に手書きされがちなので、
	// callback / behaviors 持ちは「強」、それ以外(メソッドからの参照)は「弱」。
	reConstPhp := regexp.MustCompile(`\b([A-Z]\w+)::model\s*\(|new\s+([A-Z]\w+)\s*\(`)
	var strongNotes, weakNotes []string
	for _, cc := range classes {
		m := models[cc]
		declared := map[string]bool{cc: true}
		for _, r := range m.relations {
			declared[r.target] = true
		}
		var mentions []string
		mseen := map[string]bool{}
		for _, g := range reConstPhp.FindAllStringSubmatch(m.fileSrc, -1) {
			c := g[1]
			if c == "" {
				c = g[2]
			}
			if _, isModel := models[c]; isModel && !declared[c] && !mseen[c] {
				mentions = append(mentions, c)
				mseen[c] = true
			}
		}
		if len(mentions) == 0 {
			continue
		}
		sort.Strings(mentions)
		for _, mc := range mentions {
			tt, _ := tableOf(mc) // mentions は models にあるクラスだけ
			res.Suspects = append(res.Suspects, Suspect{
				FromTable: m.tableName, ToTable: tt, Strong: reYiiCb.MatchString(m.fileSrc)})
		}
		if reYiiCb.MatchString(m.fileSrc) {
			strongNotes = append(strongNotes,
				fmt.Sprintf("[強] %s: beforeDelete/afterSave/behaviors 等 + 宣言外の %s への言及 — Yii1 はカスケードが callback に書かれがち",
					cc, strings.Join(mentions, ", ")))
		} else {
			weakNotes = append(weakNotes,
				fmt.Sprintf("[弱] %s: メソッドから宣言外の %s への言及", cc, strings.Join(mentions, ", ")))
		}
	}
	res.Notes = append(res.Notes, strongNotes...)
	res.Notes = append(res.Notes, weakNotes...)

	// 生 SQL / コマンドビルダの書き込み先([強]— 宣言に一切現れない実結合)
	for _, cc := range classes {
		m := models[cc]
		var hits []string
		for _, t := range extractRawWriteTables(m.fileSrc) {
			// {{user}} は抽出時に剥がれているので、接頭辞付きの名前でも引く
			if !tableSet[t] && prefix != "" && tableSet[prefix+t] {
				t = prefix + t
			}
			if t == m.tableName || !tableSet[t] {
				continue // 自分自身と、モデルに対応しないテーブル名(誤爆)は除く
			}
			hits = append(hits, t)
			res.Suspects = append(res.Suspects, Suspect{
				FromTable: m.tableName, ToTable: t, Strong: true})
		}
		if len(hits) > 0 {
			res.Notes = append(res.Notes,
				fmt.Sprintf("[強] %s: 生SQL/コマンドビルダで %s へ書き込み — 宣言に現れない実結合",
					cc, strings.Join(hits, ", ")))
		}
	}

	res.Notes = append(res.Notes, yii1WeightNote)

	for t := range tables {
		res.Tables = append(res.Tables, t)
	}
	sort.Strings(res.Tables)
	// Yii1 では関連の相手は必ず読んだモデル(見つからない相手は FK にしない)で、
	// MANY_MANY の中間テーブルも宣言が構造を言っている。Tables がそのまま
	// 「定義を読んだ範囲」になる。
	res.ModelTables = append([]string(nil), res.Tables...)
	return res
}
