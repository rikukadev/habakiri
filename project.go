// project.go: 合流済みの FK 集合を、証拠の種類で絞った「見方」に射影する。
//
//	Physical — DB が強制している FK だけ
//	Logical  — ORM の宣言に現れる関係だけ(DB にもある関係を含む。宣言の全集合)
//	Combined — 全部(同じ関係は 1 本に統合済み)
//
// 射影は束ねの手前で FK を選ぶだけで、以降のパイプライン(束ね → hub 除外 →
// 縮約 → 橋 → ブロック → hub 契約 → 分割)には触れない。
package main

import "fmt"

// GraphKind は射影の種類。
type GraphKind int

const (
	GraphPhysical GraphKind = iota + 1
	GraphLogical
	GraphCombined
)

func (k GraphKind) String() string {
	switch k {
	case GraphPhysical:
		return "physical"
	case GraphLogical:
		return "logical"
	case GraphCombined:
		return "combined"
	}
	return ""
}

// ParseGraphKind は --graph の値を読む。
func ParseGraphKind(s string) (GraphKind, error) {
	for _, k := range []GraphKind{GraphPhysical, GraphLogical, GraphCombined} {
		if s == k.String() {
			return k, nil
		}
	}
	return 0, fmt.Errorf("--graph は physical / logical / combined のいずれか(%q)", s)
}

// logicalView は FK を「宣言が言っていること」だけに戻す。
// 合流済みの FK は属性が physical 優先になっているので、そのまま Logical に
// 入れると DB の整備状況(どこに CASCADE が張ってあるか)が宣言のグラフに漏れる。
// 宣言側の証拠が持っている主張から属性を組み直し、physical の証拠は外す。
func logicalView(fk FK) FK {
	var evs []Evidence
	for _, ev := range fk.Evidences {
		if ev.Source != SourcePhysical {
			evs = append(evs, ev)
		}
	}
	out := fk
	out.Evidences = evs
	out.Constraint = evs[0].Constraint
	out.Nullable = evs[0].Nullable
	out.AllNotNull = evs[0].Nullable == NullableFalse
	out.DeleteRule = evs[0].DeleteRule
	for _, ev := range evs {
		if ev.DeleteRule == "CASCADE" { // 親側の dependent: は 2 件目以降の証拠にある
			out.DeleteRule = "CASCADE"
		}
	}
	return out
}

// ProjectFKs は FK 集合を射影する。
func ProjectFKs(fks []FK, kind GraphKind) []FK {
	var out []FK
	for _, fk := range fks {
		switch kind {
		case GraphPhysical:
			if fk.Enforced() {
				out = append(out, fk)
			}
		case GraphLogical:
			if fk.Logical() {
				out = append(out, logicalView(fk))
			}
		default:
			out = append(out, fk)
		}
	}
	return out
}

// Project は ScanResult を射影した写しを返す。頂点(テーブル)集合は変えない —
// ある見方で FK を持たないテーブルは、消えるのではなく孤立として残る。
// includeObserved は実測共起(--cooc)を分割グラフに算入するかどうか。
func Project(sc *ScanResult, kind GraphKind, includeObserved bool) *ScanResult {
	out := *sc
	out.FKs = ProjectFKs(sc.FKs, kind)
	out.CoocNoWeight = !includeObserved
	out.Projected = true
	out.Notes = append([]string(nil), sc.Notes...)
	out.Schema = fmt.Sprintf("%s [%s]", sc.Schema, kind)

	hasSource := func(match func(FK) bool) bool {
		for _, fk := range sc.FKs {
			if match(fk) {
				return true
			}
		}
		return false
	}
	switch kind {
	case GraphPhysical:
		// 宣言外の疑い(callback・生SQL)は静的ソース由来。DB だけの見方には入れない。
		out.Suspects = nil
		if len(out.FKs) == 0 && !hasSource(FK.Enforced) {
			out.Notes = append(out.Notes,
				"physical: DB が強制している FK が 1 本も見つかっていない。DB を読んでいない(--dsn 未指定)か、DB に FK が張られていない。どちらも「関係が無い」の確認ではない")
		}
	case GraphLogical:
		out.CrossFKs = nil // スキーマ跨ぎ FK は DB スキャンでしか見えない
		if len(out.FKs) == 0 && !hasSource(FK.Logical) {
			out.Notes = append(out.Notes,
				"logical: ORM の宣言が 1 件も見つかっていない。静的ソースを読んでいない(--rails / --yii1 未指定)可能性がある。「関係が無い」の確認ではない")
		}
	}
	return &out
}
