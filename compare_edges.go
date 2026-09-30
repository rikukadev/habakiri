// compare_edges.go: 関係 × {Physical, Logical, Observed} の存在表(#30)。
//
// 1 行 = 関係 1 本(子テーブル・子の列・親テーブル)。宣言が無く共起だけが
// 観測された対は、対の単位で 1 行にする(共起には列も向きも無い)。
//
// 規律:
//   - 「無い」と「見ていない」を分ける。DB を読んでいない実行で「DB に FK が
//     無い」とは言えないし、モデルを解析していないテーブルについて「ORM に
//     宣言が無い」とも言えない。存在は present / absent / unobserved の三値で持ち、
//     片方でも unobserved なら分類は undetermined に倒す
//   - hub へのエッジは捨てず、通常のエッジと別枠で同じ形の表にする。hub の
//     周辺は FK の未整備が集まりやすく、外すと一番見たい所が消える
//   - 出力はソートする(map 順に依存しない)
package main

import (
	"sort"
	"strings"
)

// Presence はあるソースから見た関係の有無。
type Presence string

const (
	Present Presence = "present"
	// Absent: そのソースが関係の両端を見たうえで、関係が無かった。
	Absent Presence = "absent"
	// Unobserved: そのソースを読んでいない、または端のテーブルがそのソースの
	// 対象外。関係が無いと確認できたわけではない。
	Unobserved Presence = "unobserved"
)

// Edge Diff の分類。
const (
	EdgeBoth         = "both"          // DB の制約と ORM の宣言が一致
	EdgeLogicalOnly  = "logical_only"  // ORM にあるが DB は強制していない
	EdgePhysicalOnly = "physical_only" // DB の制約はあるが ORM に宣言が無い
	EdgeObservedOnly = "observed_only" // 実行時に共起したが、DB にも ORM にも宣言が無い
	// EdgeUndetermined: どちらかのソースが unobserved で、上のどれとも言えない。
	EdgeUndetermined = "undetermined"
)

// EdgeDiffRow は存在表の 1 行。
type EdgeDiffRow struct {
	// A, B: テーブル対(A <= B)。並べ替えと、共起との突き合わせに使う。
	A string `json:"a"`
	B string `json:"b"`
	// 関係の向きと列。共起だけの行(observed_only 等)では空。
	ChildTable  string   `json:"child_table,omitempty"`
	ChildCols   []string `json:"child_columns,omitempty"`
	ParentTable string   `json:"parent_table,omitempty"`

	Physical Presence `json:"physical"`
	Logical  Presence `json:"logical"`
	Observed Presence `json:"observed"`
	Class    string   `json:"class"`

	// 共起の実測値(Observed が present のとき)。
	CoocCount int     `json:"cooc_count,omitempty"`
	CoocNPMI  float64 `json:"cooc_npmi,omitempty"`
	// Hubs: この行が接している hub(hub 枠の行だけ)。
	Hubs []string `json:"hubs,omitempty"`
}

// EdgeDiffCounts は分類ごとの行数。
type EdgeDiffCounts struct {
	Both         int `json:"both"`
	LogicalOnly  int `json:"logical_only"`
	PhysicalOnly int `json:"physical_only"`
	ObservedOnly int `json:"observed_only"`
	Undetermined int `json:"undetermined"`
}

func (c *EdgeDiffCounts) add(class string) {
	switch class {
	case EdgeBoth:
		c.Both++
	case EdgeLogicalOnly:
		c.LogicalOnly++
	case EdgePhysicalOnly:
		c.PhysicalOnly++
	case EdgeObservedOnly:
		c.ObservedOnly++
	default:
		c.Undetermined++
	}
}

// EdgeDiff は存在表の全体。通常のエッジと、hub に接するエッジを別枠で持つ。
type EdgeDiff struct {
	Edges     []EdgeDiffRow  `json:"edges"`
	Counts    EdgeDiffCounts `json:"counts"`
	HubEdges  []EdgeDiffRow  `json:"hub_edges"`
	HubCounts EdgeDiffCounts `json:"hub_counts"`
}

// DiffEdges は統合済みのスキャン結果から存在表を作る。
// hubs は hub 扱いのテーブル(比較の前処理で固定したもの)。
//
// 共起は NPMI が coocNPMIThreshold 以上の対だけを「観測された」とみなす。
// 分割グラフに共起を参加させる基準と同じで、高頻度テーブルの偶発的な
// 共起を関係として数えないため。
func DiffEdges(sc *ScanResult, hubs []string) EdgeDiff {
	isHub := map[string]bool{}
	for _, h := range hubs {
		isHub[h] = true
	}
	physSeen, logicSeen := observedScopes(sc)

	type coocVal struct {
		count int
		npmi  float64
	}
	cooc := map[Pair]coocVal{}
	if sc.Cooc != nil {
		for _, p := range sc.Cooc.Pairs {
			if p.A == p.B {
				continue
			}
			if v := sc.Cooc.NPMI(p.A, p.B, p.Count); v >= coocNPMIThreshold {
				cooc[mkPair(p.A, p.B)] = coocVal{count: p.Count, npmi: v}
			}
		}
	}
	observedOf := func(p Pair) (Presence, coocVal) {
		if sc.Cooc == nil {
			return Unobserved, coocVal{}
		}
		if v, ok := cooc[p]; ok {
			return Present, v
		}
		return Absent, coocVal{}
	}

	var rows []EdgeDiffRow
	declared := map[Pair]bool{}
	for _, fk := range sc.FKs {
		p := mkPair(fk.ChildTable, fk.ParentTable)
		declared[p] = true
		row := EdgeDiffRow{A: p.A, B: p.B,
			ChildTable: fk.ChildTable, ChildCols: append([]string(nil), fk.ChildCols...), ParentTable: fk.ParentTable}
		// 物理 FK は子テーブルに付く。DB が子テーブルを見ていれば、無いものは無い。
		row.Physical = presence(fk.Enforced(), physSeen(fk.ChildTable))
		// 宣言は子のモデル(belongs_to)にも親のモデル(has_many)にも書ける。
		// 両方のモデルを解析していて初めて「宣言が無い」と言える。
		row.Logical = presence(fk.Logical(), logicSeen(fk.ChildTable) && logicSeen(fk.ParentTable))
		var cv coocVal
		row.Observed, cv = observedOf(p)
		row.CoocCount, row.CoocNPMI = cv.count, cv.npmi
		row.Class = classifyEdge(row.Physical, row.Logical, row.Observed)
		rows = append(rows, row)
	}

	// 宣言がどちらにも無く、共起だけが観測された対。
	var coocPairs []Pair
	for p := range cooc {
		if !declared[p] {
			coocPairs = append(coocPairs, p)
		}
	}
	sort.Slice(coocPairs, func(i, j int) bool { return pairLess(coocPairs[i], coocPairs[j]) })
	for _, p := range coocPairs {
		v := cooc[p]
		row := EdgeDiffRow{A: p.A, B: p.B, Observed: Present, CoocCount: v.count, CoocNPMI: v.npmi}
		// 向きが分からないので、両端とも見ていて初めて「無い」と言える。
		row.Physical = presence(false, physSeen(p.A) && physSeen(p.B))
		row.Logical = presence(false, logicSeen(p.A) && logicSeen(p.B))
		row.Class = classifyEdge(row.Physical, row.Logical, row.Observed)
		rows = append(rows, row)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		x, y := rows[i], rows[j]
		if x.A != y.A {
			return x.A < y.A
		}
		if x.B != y.B {
			return x.B < y.B
		}
		if x.ChildTable != y.ChildTable {
			return x.ChildTable < y.ChildTable
		}
		return strings.Join(x.ChildCols, ",") < strings.Join(y.ChildCols, ",")
	})

	var d EdgeDiff
	for _, row := range rows {
		if isHub[row.A] {
			row.Hubs = append(row.Hubs, row.A)
		}
		if isHub[row.B] && row.B != row.A {
			row.Hubs = append(row.Hubs, row.B)
		}
		if len(row.Hubs) > 0 {
			d.HubEdges = append(d.HubEdges, row)
			d.HubCounts.add(row.Class)
		} else {
			d.Edges = append(d.Edges, row)
			d.Counts.add(row.Class)
		}
	}
	return d
}

func presence(found, seen bool) Presence {
	switch {
	case found:
		return Present
	case seen:
		return Absent
	}
	return Unobserved
}

// classifyEdge は三値の存在から分類を決める。迷ったら undetermined。
func classifyEdge(physical, logical, observed Presence) string {
	switch {
	case physical == Present && logical == Present:
		return EdgeBoth
	case physical == Present && logical == Absent:
		return EdgePhysicalOnly
	case physical == Absent && logical == Present:
		return EdgeLogicalOnly
	case physical == Absent && logical == Absent && observed == Present:
		return EdgeObservedOnly
	}
	return EdgeUndetermined
}

// observedScopes は「DB がこのテーブルを見たか」「静的解析がこのテーブルの
// モデルを見たか」を返す。
func observedScopes(sc *ScanResult) (phys, logic func(string) bool) {
	set := func(ts []string) func(string) bool {
		m := make(map[string]bool, len(ts))
		for _, t := range ts {
			m[t] = true
		}
		return func(t string) bool { return m[t] }
	}
	pt, lt, _, _ := observedTables(sc)
	return set(pt), set(lt)
}

// observedTables は各ソースが見たテーブルの一覧と、そのソースを読んだかどうか。
//
// 合流した結果なら、各ソースが見たテーブルの集合が残っている。単独ソースの
// 結果は、そのソースのテーブルだけを見ており、もう片方は何も見ていない。
func observedTables(sc *ScanResult) (phys, logic []string, physRead, logicRead bool) {
	if sc.Merged {
		return sc.PhysicalTables, sc.LogicalTables, true, true
	}
	// 単独ソース。どちらのソースかは FK の証拠で決める(証拠が無い古い構築
	// 経路の結果は、どちらとも言えないので両方「読んでいない」)。
	var anyPhys, anyLogic bool
	for _, fk := range sc.FKs {
		anyPhys = anyPhys || fk.Enforced()
		anyLogic = anyLogic || fk.Logical()
	}
	switch {
	case sc.Dialect != "" || (anyPhys && !anyLogic):
		return sc.Tables, nil, true, false
	case anyLogic && !anyPhys:
		return nil, sc.Tables, false, true
	}
	return nil, nil, false, false
}
