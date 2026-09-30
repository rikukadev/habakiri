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
	file   string // 宣言のあるファイル(祖先から継承した relations() なら祖先のファイル)
}

var (
	// クラス宣言(abstract / final 付き、名前空間付きの親も)。モデルかどうかは
	// 継承を辿って決める(yiiDiscover)— extends の右側だけ見ると X2Model 経由の
	// 2 段継承を取りこぼす。
	reYiiClassDecl = regexp.MustCompile(`(?m)^\s*(abstract\s+|final\s+)?class\s+(\w+)\s+extends\s+\\?(?:\w+\\)*(\w+)`)
	reYiiARRoot    = regexp.MustCompile(`ActiveRecord$`)
	// tableName() の本体が文字列リテラル 1 つを返すだけなら静的
	reYiiStaticReturn = regexp.MustCompile(`^\s*return\s+['"]([^'"]*)['"]\s*;\s*$`)
	// 連結で組む名前の先頭('{{responses_' . $this->dynamicId . '}}')
	reYiiConcatHead = regexp.MustCompile(`^\s*return\s+['"]([^'"]+)['"]\s*\.`)
	reYiiReturnVar  = regexp.MustCompile(`\breturn\s+\$\w+\s*;`)
	rePHPComment    = regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*|#[^\n]*`)
	reYiiMM         = regexp.MustCompile(`^\s*([\w.{}]+)\s*\(\s*([\w]+)\s*,\s*([\w]+)\s*\)\s*$`)
	reYiiCb         = regexp.MustCompile(`function\s+(beforeSave|afterSave|beforeDelete|afterDelete|beforeValidate|afterValidate|beforeFind|afterFind|afterConstruct|behaviors)\s*\(`)
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

	d, err := yiiDiscover(dir, files, prefix)
	if err != nil {
		return nil, err
	}
	models, dupes := d.models, d.dupes
	if len(models) == 0 {
		return nil, fmt.Errorf("%s に CActiveRecord 系のモデルが見つかりません", modelsDir)
	}

	res := yiiToScan(dir, models, prefix, d.dynamic)
	if prefix != "" {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"yii1 設定の tablePrefix %q を {{...}} で書かれたテーブル名に前置した(protected/config/main.php)", prefix))
	}
	if len(dupes) > 0 {
		res.Notes = append(res.Notes, "yii1: 同名のモデルクラスが複数ある — パス順で先のものを採った: "+strings.Join(dupes, ", "))
	}
	res.Notes = append(res.Notes, d.notes...)
	// どこを読んだかを出す(読まなかった場所が出力から分からないのが一番困る)。
	// モジュールを持つアプリでだけ出す — models/ だけのアプリでは自明なので。
	if protected != "" {
		if fi, err := os.Stat(filepath.Join(protected, "modules")); err == nil && fi.IsDir() {
			perDir := map[string]int{}
			for _, m := range models {
				rel := sourceOrigin(dir, m.path, 0)
				if i := strings.LastIndex(rel, "/models/"); i >= 0 {
					rel = rel[:i+len("/models")]
				}
				perDir[rel]++
			}
			var locs []string
			for d := range perDir {
				locs = append(locs, d)
			}
			sort.Strings(locs)
			var parts []string
			for _, d := range locs {
				parts = append(parts, fmt.Sprintf("%s %d", d, perDir[d]))
			}
			res.Notes = append(res.Notes, fmt.Sprintf("yii1: 読んだモデル %d 個 — %s", len(models), strings.Join(parts, " / ")))
		}
	}
	res.FileTables = map[string]string{}
	for _, m := range models {
		if m.path != "" {
			res.FileTables[m.path] = m.tableName
		}
	}
	return res, nil
}

func yiiToScan(dir string, models map[string]*yiiModel, prefix string, dynamic map[string]string) *ScanResult {
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
	// PHP のクラス名は大文字小文字を区別しない('defaultvalue' でも DefaultValue に
	// 解決される)。完全一致が無く、大文字小文字を無視して一意に決まるときだけ引く。
	folded := map[string][]string{}
	for c := range models {
		folded[strings.ToLower(c)] = append(folded[strings.ToLower(c)], c)
	}
	tableOf := func(class string) (string, bool) {
		if m, ok := models[class]; ok {
			return m.tableName, true
		}
		if cs := folded[strings.ToLower(class)]; len(cs) == 1 {
			return models[cs[0]].tableName, true
		}
		return "", false
	}
	var unresolved []string
	unknown := func(cc string, r yiiRelation) {
		if fam, ok := dynamic[r.target]; ok {
			// 相手のテーブル名が実行時に決まる(テーブル族)。どのテーブルかは言えない
			unresolved = append(unresolved, fmt.Sprintf("%s.%s → %s(%s)", cc, r.name, r.target, fam))
			return
		}
		unresolved = append(unresolved, fmt.Sprintf("%s.%s → %s", cc, r.name, r.target))
	}

	// BELONGS_TO と HAS_MANY/HAS_ONE は同じエッジの両側宣言なので、child+parent+cols で重複排除。
	// 両側の宣言はどちらも同じ関係の証拠なので、FK は 1 本のまま Evidence を足す。
	seen := map[string]int{} // key → res.FKs の位置
	// 自己参照(parent_id の木構造など)も作る。DB スキャンは自己参照 FK を持つので、
	// 捨てると Edge Diff で「DB にだけある関係」に見える。分割に効かないのは
	// グラフ層(BuildEdges)が落とすため。
	addFK := func(child, parent string, cols []string, constraint string, m *yiiModel, r yiiRelation) {
		file := r.file
		if file == "" {
			file = m.path
		}
		ev := Evidence{Source: SourceYii1, Origin: sourceOrigin(dir, file, r.line),
			Constraint: constraint, DeleteRule: "NO ACTION", Nullable: NullableUnknown}
		key := child + "\x00" + parent + "\x00" + strings.Join(cols, ",")
		if i, ok := seen[key]; ok {
			// 同じ宣言を 2 回数えない(relations() を継承した子クラスは親の宣言を
			// そのまま持つので、同じテーブル・同じ位置の証拠が重なる)
			for _, e := range res.FKs[i].Evidences {
				if e.Origin == ev.Origin {
					return
				}
			}
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
	cascaded := map[string]map[string]bool{} // モデル → 手書きカスケードで出した相手
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
			if cascaded[cc] == nil {
				cascaded[cc] = map[string]bool{}
			}
			for t := range targets {
				names = append(names, t)
				cascaded[cc][t] = true
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

	// モデル経由の一括書き込み(X::model()->updateAll 等)。callback の外でも
	// 宣言に現れない書き込み結合なので [強]。手書きカスケードで出した相手は重ねない。
	reModelWrite := regexp.MustCompile(`(\w+)::model\(\)->(?:updateAll|updateByPk|updateCounters|deleteAll|deleteByPk|deleteAllByAttributes)\(`)
	for _, cc := range classes {
		m := models[cc]
		targets := map[string]bool{}
		for _, w := range reModelWrite.FindAllStringSubmatch(maskPHPComments(m.fileSrc), -1) {
			if w[1] != cc && !cascaded[cc][w[1]] {
				targets[w[1]] = true
			}
		}
		var hits []string
		for t := range targets {
			if tt, ok := tableOf(t); ok {
				hits = append(hits, t)
				res.Suspects = append(res.Suspects, Suspect{FromTable: m.tableName, ToTable: tt, Strong: true})
			}
		}
		if len(hits) > 0 {
			sort.Strings(hits)
			res.Notes = append(res.Notes, fmt.Sprintf(
				"[強] %s: %s を ::model()->updateAll 等で一括書き込み — 宣言に現れない書き込み結合", cc, strings.Join(hits, ", ")))
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
		// 読み取り(FROM / JOIN)は [弱]。書き込み先として出したテーブルは重ねない
		written := map[string]bool{}
		for _, h := range hits {
			written[h] = true
		}
		var reads []string
		for _, t := range extractRawReadTables(m.fileSrc) {
			if !tableSet[t] && prefix != "" && tableSet[prefix+t] {
				t = prefix + t
			}
			if t == m.tableName || !tableSet[t] || written[t] {
				continue
			}
			reads = append(reads, t)
			res.Suspects = append(res.Suspects, Suspect{FromTable: m.tableName, ToTable: t, Strong: false})
		}
		if len(reads) > 0 {
			res.Notes = append(res.Notes,
				fmt.Sprintf("[弱] %s: 生SQL/コマンドビルダで %s を読む(read 側の暗黙結合)", cc, strings.Join(reads, ", ")))
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

// yiiDecl はクラス宣言 1 つ。
type yiiDecl struct {
	class, parent, path, src string
	abstract                 bool
}

// yiiDiscovery はモデル判定の結果。
type yiiDiscovery struct {
	models  map[string]*yiiModel // テーブルを持つ具体的なモデル
	dynamic map[string]string    // テーブル名を実行時に組み立てるモデル → 族の表記
	// relTotal / relSkipped: relations() の宣言の総数と、関係として読めなかった
	// 宣言の理由別の件数(読めなかったものを 0 件に見せない)
	relTotal   int
	relSkipped map[string]int
	dupes      []string
	notes      []string
}

// yiiMethodBody は function name(...) { ... } の本体を返す。
func yiiMethodBody(src, name string) (string, bool) {
	re := regexp.MustCompile(`function\s+` + name + `\s*\([^)]*\)[^{]*\{`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		return "", false
	}
	return braceBody(src, loc[1]-1), true
}

// yiiDiscover はクラス宣言を全部集め、継承を辿ってモデルを決める。
//
//   - CActiveRecord(名前が ActiveRecord で終わる、読んだ範囲に無いクラス)まで
//     辿れるクラスがモデル候補。X2Model → X2ActiveRecord → CActiveRecord の
//     2 段継承も拾う
//   - 基底クラスはテーブルにしない: abstract、または他のモデルに継承されていて
//     自分では tableName() も relations() も持たないクラス(LSActiveRecord 等)
//   - tableName() / relations() を持たないクラスは祖先のものを継承する(Yii と同じ)
//   - tableName() が文字列リテラルでなければ動的。具体的なテーブルにしない
//     (連結の先頭が読めれば「テーブル族」として注記)
//   - relations() が静的に読めない(実行時に組み立てる)なら、黙って 0 本にせず注記
func yiiDiscover(dir string, files []string, prefix string) (*yiiDiscovery, error) {
	d := &yiiDiscovery{models: map[string]*yiiModel{}, dynamic: map[string]string{}, relSkipped: map[string]int{}}
	decls := map[string]*yiiDecl{}
	var order []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		src := string(raw)
		cm := reYiiClassDecl.FindStringSubmatch(src)
		if cm == nil {
			continue
		}
		if prev, dup := decls[cm[2]]; dup {
			// Yii1 のクラス名はグローバル。同名が 2 つあると実行時にどちらかしか
			// 読まれない。先に見つけた方(パス順)を採り、注記する。
			d.dupes = append(d.dupes, fmt.Sprintf("%s(%s と %s)", cm[2], sourceOrigin(dir, prev.path, 0), sourceOrigin(dir, f, 0)))
			continue
		}
		decls[cm[2]] = &yiiDecl{class: cm[2], parent: cm[3], path: f, src: src,
			abstract: strings.HasPrefix(strings.TrimSpace(cm[1]), "abstract")}
		order = append(order, cm[2])
	}

	// CActiveRecord まで辿れるか。辿れなければ、どこで切れたか(読んでいない親)を返す
	isAR := func(c string) (bool, string) {
		seen := map[string]bool{}
		for cur := c; !seen[cur]; {
			seen[cur] = true
			decl, ok := decls[cur]
			if !ok {
				return reYiiARRoot.MatchString(cur), cur
			}
			cur = decl.parent
		}
		return false, ""
	}

	type info struct {
		hasTable, staticTable bool
		table, family         string
		hasRels, readableRels bool
		rels                  []yiiRelation
	}
	infos := map[string]*info{}
	extended := map[string]bool{}
	var broken []string
	for _, c := range order {
		decl := decls[c]
		ok, root := isAR(c)
		if !ok {
			_, hasT := yiiMethodBody(decl.src, "tableName")
			_, hasR := yiiMethodBody(decl.src, "relations")
			if (hasT || hasR) && root != "" && !strings.HasSuffix(root, "Model") && !strings.HasSuffix(root, "Form") {
				broken = append(broken, fmt.Sprintf("%s(%s で切れる)", c, root))
			}
			continue
		}
		extended[decl.parent] = true
		in := &info{}
		if body, has := yiiMethodBody(decl.src, "tableName"); has {
			in.hasTable = true
			if m := reYiiStaticReturn.FindStringSubmatch(body); m != nil {
				in.staticTable, in.table = true, yiiTableName(m[1], prefix)
			} else if m := reYiiConcatHead.FindStringSubmatch(body); m != nil {
				head := strings.TrimPrefix(m[1], "{{")
				if strings.HasPrefix(m[1], "{{") {
					head = prefix + head
				}
				in.family = "テーブル族 " + head + "*"
			} else {
				in.family = "名前を静的に読めない"
			}
		}
		if body, has := yiiMethodBody(decl.src, "relations"); has {
			in.hasRels = true
			rels, skipped := parseYiiRelations(decl.src, decl.path)
			in.rels = rels
			for why, n := range skipped {
				d.relSkipped[why] += n
			}
			d.relTotal += len(rels)
			for _, n := range skipped {
				d.relTotal += n
			}
			// 読めないのは return $relations; のように変数を返す(実行時に組み立てる)
			// ときだけ。空の配列・on で結合する関連・列の対応表は静的に書かれている
			in.readableRels = !reYiiReturnVar.MatchString(stripPHPComments(body))
		}
		infos[c] = in
	}

	// 祖先を辿って tableName() / relations() を引く(自分が持っていればそれ)
	ancestor := func(c string, has func(*info) bool) *info {
		seen := map[string]bool{}
		for cur := c; !seen[cur]; {
			seen[cur] = true
			in, ok := infos[cur]
			if !ok {
				return nil
			}
			if has(in) {
				return in
			}
			cur = decls[cur].parent
		}
		return nil
	}

	var bases, dynamics []string
	unreadable := map[string]int{} // relations() を静的に読めないクラス → それを使うモデル数
	for _, c := range order {
		in, ok := infos[c]
		if !ok {
			continue
		}
		decl := decls[c]
		tin := ancestor(c, func(i *info) bool { return i.hasTable })
		dynamicTable := tin != nil && !tin.staticTable
		if decl.abstract || (extended[c] && !in.hasTable && !in.hasRels) {
			bases = append(bases, c)
			if dynamicTable {
				// abstract のまま実行時に具象クラスを作る設計(LimeSurvey の Response)。
				// 関連の相手として現れるので、族として覚えておく
				d.dynamic[c] = tin.family
			}
			continue
		}
		if dynamicTable {
			d.dynamic[c] = tin.family
			dynamics = append(dynamics, fmt.Sprintf("%s(%s)", c, tin.family))
			continue
		}
		m := &yiiModel{class: c, tableName: c, fileSrc: decl.src, path: decl.path}
		if tin != nil {
			m.tableName = tin.table
		}
		if rin := ancestor(c, func(i *info) bool { return i.hasRels }); rin != nil {
			m.relations = rin.rels
			if !rin.readableRels {
				owner := c
				for cur := c; ; cur = decls[cur].parent {
					if infos[cur] == rin {
						owner = cur
						break
					}
				}
				unreadable[owner]++
			}
		}
		d.models[c] = m
	}
	for c, fam := range d.dynamic {
		if decls[c].abstract {
			dynamics = append(dynamics, fmt.Sprintf("%s(%s)", c, fam))
		}
	}
	sort.Strings(dynamics)

	if len(bases) > 0 {
		sort.Strings(bases)
		d.notes = append(d.notes, fmt.Sprintf(
			"yii1: 基底クラス %d 個はテーブルにしていない(abstract、または継承されるだけで tableName()/relations() を持たない): %s",
			len(bases), strings.Join(bases, ", ")))
	}
	if len(dynamics) > 0 {
		d.notes = append(d.notes, fmt.Sprintf(
			"[判定不能] テーブル名を実行時に組み立てるモデル %d 個は、具体的なテーブルにせず関連も FK にしていない: %s",
			len(dynamics), strings.Join(dynamics, ", ")))
	}
	if len(unreadable) > 0 {
		var parts []string
		for c, n := range unreadable {
			parts = append(parts, fmt.Sprintf("%s(使うモデル %d)", c, n))
		}
		sort.Strings(parts)
		d.notes = append(d.notes, fmt.Sprintf(
			"[判定不能] relations() を静的に読めない(実行時に組み立てる)— このモデルの関連は 0 本に見えるが、無いことの確認ではない: %s",
			strings.Join(parts, ", ")))
	}
	if skippedN := func() int {
		n := 0
		for _, v := range d.relSkipped {
			n += v
		}
		return n
	}(); skippedN > 0 {
		var whys []string
		for why := range d.relSkipped {
			whys = append(whys, why)
		}
		sort.Strings(whys)
		var parts []string
		for _, why := range whys {
			parts = append(parts, fmt.Sprintf("%s %d", why, d.relSkipped[why]))
		}
		d.notes = append(d.notes, fmt.Sprintf(
			"yii1: relations() の宣言 %d 件中 %d 件を関係として読んだ。読めなかった %d 件(%s)は FK にしていない",
			d.relTotal, d.relTotal-skippedN, skippedN, strings.Join(parts, " / ")))
	}
	if len(broken) > 0 {
		sort.Strings(broken)
		d.notes = append(d.notes, fmt.Sprintf(
			"[判定不能] tableName()/relations() を持つが、継承を ActiveRecord まで辿れないクラス(親を読んでいない): %s",
			strings.Join(broken, ", ")))
	}
	return d, nil
}

// stripPHPComments はコメントを除く(判定の邪魔になる NOTE 行など)。
func stripPHPComments(src string) string { return rePHPComment.ReplaceAllString(src, "") }

var (
	// 宣言の先頭: 'name' => array(self::KIND,  /  'name' => [self::KIND,
	reYiiRelHead = regexp.MustCompile(`['"](\w+)['"]\s*=>\s*(array\s*\(|\[)\s*self::(BELONGS_TO|HAS_MANY|HAS_ONE|MANY_MANY)\s*,\s*`)
	// 相手: 'Class' / Class::class / \Ns\Class::class
	reYiiRelTarget = regexp.MustCompile(`^(?:['"](\w+)['"]|\\?(?:\w+\\)*(\w+)::class)\s*,\s*`)
	reYiiQuoted    = regexp.MustCompile(`^(?:'([^']*)'|"([^"]*)")`)
	reYiiPair      = regexp.MustCompile(`['"](\w+)['"]\s*=>\s*['"](\w+)['"]`)
	reYiiItem      = regexp.MustCompile(`['"](\w+)['"]`)
	reYiiOnOpt     = regexp.MustCompile(`['"]on['"]\s*=>\s*(?:"([^"]*)"|'([^']*)')`)
	reYiiOnEq      = regexp.MustCompile(`(\$?[\w>-]+)\.(\w+)\s*=\s*(\$?[\w>-]+)\.(\w+)`)
)

// 関係として読めなかった宣言の理由(注記に件数で出す)。
const (
	relSkipTarget = "相手を読めない"
	relSkipOn     = "列を特定できない on 句"
	relSkipFK     = "外部キーを読めない"
)

// maskPHPComments はコメントを空白に置き換える(改行は残すので行番号がずれない)。
// コメントアウトした宣言を関係として読まないため。
func maskPHPComments(src string) string {
	return rePHPComment.ReplaceAllStringFunc(src, func(c string) string {
		b := []byte(c)
		for i := range b {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
		return string(b)
	})
}

// balanced は src[open] の ( または [ に対応する閉じ括弧までの中身を返す。
// 文字列リテラルの中の括弧は数えない。
func balanced(src string, open int) (string, bool) {
	depth := 0
	var quote byte
	for i := open; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			switch c {
			case '\\':
				i++ // エスケープの次の 1 文字を飛ばす
			case quote:
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
			if depth == 0 {
				return src[open+1 : i], true
			}
		}
	}
	return "", false
}

// parseYiiRelations は relations() の宣言を読む。FK の書き方は 4 通り:
//
//	'post_id'                           列(カンマ区切りで複合)
//	array('fk' => 'pk', ...) / [...]    列の対応。左側が子の列
//	array('col1', 'col2')               列の並び
//	''  + 'on' => "$alias.x = rel.y"    on 句から列を読む(読めなければ関係にしない)
//
// 子の列の向きは Yii1 の定義どおり: BELONGS_TO は自分のテーブルの列、
// HAS_ONE / HAS_MANY は相手のテーブルの列。MANY_MANY は 'join(col1, col2)'。
func parseYiiRelations(rawSrc, path string) ([]yiiRelation, map[string]int) {
	src := maskPHPComments(rawSrc)
	skipped := map[string]int{}
	var rels []yiiRelation
	for _, loc := range reYiiRelHead.FindAllStringSubmatchIndex(src, -1) {
		name, kind := src[loc[2]:loc[3]], src[loc[6]:loc[7]]
		body, ok := balanced(src, loc[4])
		if !ok {
			skipped[relSkipFK]++
			continue
		}
		// body は "self::KIND, target, fk, ..."。先頭の self::KIND, を飛ばす
		rest := src[loc[1]:]
		if end := loc[4] + 1 + len(body); end > loc[1] {
			rest = src[loc[1]:end]
		}
		tm := reYiiRelTarget.FindStringSubmatch(rest)
		if tm == nil {
			skipped[relSkipTarget]++
			continue
		}
		target := tm[1]
		if target == "" {
			target = tm[2]
		}
		fkPart := strings.TrimSpace(rest[len(tm[0]):])
		rel := yiiRelation{name: name, kind: kind, target: target,
			line: strings.Count(src[:loc[0]], "\n") + 1, file: path}

		var cols []string
		switch {
		case reYiiQuoted.MatchString(fkPart):
			q := reYiiQuoted.FindStringSubmatch(fkPart)
			spec := q[1] + q[2]
			if strings.TrimSpace(spec) != "" {
				rel.fkSpec = spec
				rels = append(rels, rel)
				continue
			}
			// 空の FK: on 句で結んでいる
			on := reYiiOnOpt.FindStringSubmatch(fkPart)
			if on == nil {
				skipped[relSkipOn]++
				continue
			}
			eq := reYiiOnEq.FindStringSubmatch(on[1] + on[2])
			if eq == nil {
				skipped[relSkipOn]++
				continue
			}
			// 相手側の別名は関連名。もう片方が自分側($alias / t / $this->...)
			ownCol, relCol := "", ""
			switch {
			case eq[3] == name:
				ownCol, relCol = eq[2], eq[4]
			case eq[1] == name:
				ownCol, relCol = eq[4], eq[2]
			}
			if ownCol == "" {
				skipped[relSkipOn]++
				continue
			}
			if kind == "BELONGS_TO" {
				cols = []string{ownCol}
			} else {
				cols = []string{relCol}
			}
		case strings.HasPrefix(fkPart, "array") || strings.HasPrefix(fkPart, "["):
			open := strings.IndexAny(fkPart, "([")
			inner, ok := balanced(fkPart, open)
			if !ok {
				skipped[relSkipFK]++
				continue
			}
			if pairs := reYiiPair.FindAllStringSubmatch(inner, -1); len(pairs) > 0 {
				for _, p := range pairs {
					cols = append(cols, p[1]) // 'fk' => 'pk' の左側が子の列
				}
			} else {
				for _, it := range reYiiItem.FindAllStringSubmatch(inner, -1) {
					cols = append(cols, it[1])
				}
			}
		}
		if len(cols) == 0 {
			skipped[relSkipFK]++
			continue
		}
		rel.fkSpec = strings.Join(cols, ",")
		rels = append(rels, rel)
	}
	return rels, skipped
}
