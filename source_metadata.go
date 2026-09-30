// source_metadata.go: メタデータの関係一覧を読む(--relations、#54)。
//
// 関連を実行時にメタデータ(DB の行・設定)から組み立てるアプリでは、
// relations() にも DB の FK にも関係が現れない。その関係を、使う側が一覧に
// 書き出して渡す入口。ツールはアプリ固有の読み方を持たず、一覧を読むだけ
// (一覧は SQL の結果・シードデータ・手書きなど、どこから作ってもよい)。
//
// 形式(TSV、1 行 1 関係。# から行末は注釈、空行は無視):
//
//	子<TAB>子の列<TAB>親
//
// 子と親はテーブル名か、静的ソースのモデルのクラス名(大文字小文字を区別しない)。
// 列は複合ならカンマ区切り。先頭行が「child<TAB>column<TAB>parent」なら見出しとして飛ばす。
//
// 証拠の出どころは logical:metadata。ORM の宣言(logical:yii1 等)とは別に数える。
// 名前を解決できない行は FK にしない(クラス名をそのままテーブル名にすると偽の関係になる)。
package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// SourceMetadata はメタデータの関係一覧に由来する証拠。
const SourceMetadata = "logical:metadata"

// MetadataRelation は一覧の 1 行。
type MetadataRelation struct {
	Child, Parent string
	Cols          []string
	Line          int
}

// LoadMetadataRelations は一覧ファイルを読む。
func LoadMetadataRelations(path string) ([]MetadataRelation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []MetadataRelation
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimRight(line, " \t") // 行末の注釈の前の区切りも落とす
		if strings.TrimSpace(line) == "" {
			continue
		}
		cells := strings.Split(line, "\t")
		if len(cells) != 3 {
			return nil, fmt.Errorf("%s:%d: 子<TAB>列<TAB>親 の 3 列ではない", path, n)
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if len(out) == 0 && strings.EqualFold(cells[0], "child") && strings.EqualFold(cells[2], "parent") {
			continue // 見出し
		}
		var cols []string
		for _, c := range strings.Split(cells[1], ",") {
			if c = strings.TrimSpace(c); c != "" {
				cols = append(cols, c)
			}
		}
		if cells[0] == "" || cells[2] == "" || len(cols) == 0 {
			return nil, fmt.Errorf("%s:%d: 空の欄がある", path, n)
		}
		out = append(out, MetadataRelation{Child: cells[0], Parent: cells[2], Cols: cols, Line: n})
	}
	return out, sc.Err()
}

// AddMetadataRelations は一覧の関係を静的ソースの結果に足す。logic が nil なら
// 一覧だけの結果を作る(名前はテーブル名として扱う)。
func AddMetadataRelations(logic *ScanResult, path string, rels []MetadataRelation) *ScanResult {
	if logic == nil {
		logic = &ScanResult{Schema: "metadata"}
	}
	tables := map[string]bool{}
	for _, t := range logic.Tables {
		tables[t] = true
	}
	folded := map[string][]string{}
	for c, t := range logic.ModelClasses {
		folded[strings.ToLower(c)] = append(folded[strings.ToLower(c)], t)
	}
	standalone := len(logic.Tables) == 0
	resolve := func(name string) (string, bool) {
		if tables[name] || standalone {
			return name, true
		}
		if ts := folded[strings.ToLower(name)]; len(ts) == 1 {
			return ts[0], true
		}
		return "", false
	}

	index := map[string]int{}
	for i, fk := range logic.FKs {
		index[relationKey(fk)] = i
	}
	var unresolved []string
	added := 0
	for _, r := range rels {
		child, ok1 := resolve(r.Child)
		parent, ok2 := resolve(r.Parent)
		if !ok1 || !ok2 {
			unresolved = append(unresolved, fmt.Sprintf("%d 行目 %s → %s", r.Line, r.Child, r.Parent))
			continue
		}
		constraint := "metadata:" + child + "→" + parent
		ev := Evidence{Source: SourceMetadata, Origin: fmt.Sprintf("%s:%d", path, r.Line),
			Constraint: constraint, DeleteRule: "NO ACTION", Nullable: NullableUnknown}
		fk := FK{Constraint: constraint, ChildTable: child, ChildCols: r.Cols, ParentTable: parent,
			DeleteRule: "NO ACTION", Nullable: NullableUnknown, Evidences: []Evidence{ev}}
		if i, ok := index[relationKey(fk)]; ok {
			logic.FKs[i].Evidences = append(logic.FKs[i].Evidences, ev)
			continue
		}
		index[relationKey(fk)] = len(logic.FKs)
		logic.FKs = append(logic.FKs, fk)
		added++
		for _, t := range []string{child, parent} {
			if !tables[t] {
				tables[t] = true
				logic.Tables = append(logic.Tables, t)
			}
		}
	}
	sort.Strings(logic.Tables)
	logic.Notes = append(logic.Notes, fmt.Sprintf(
		"メタデータの関係一覧(%s)から %d 行を読み、%d 本を新しい関係として足した(残りは既存の関係の証拠)。出どころは logical:metadata",
		path, len(rels)-len(unresolved), added))
	if len(unresolved) > 0 {
		logic.Notes = append(logic.Notes, fmt.Sprintf(
			"[判定不能] メタデータの関係一覧のうち %d 行は、テーブルにもモデルのクラスにも解決できず FK にしていない: %s",
			len(unresolved), strings.Join(unresolved, ", ")))
	}
	return logic
}
