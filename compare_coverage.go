// compare_coverage.go: 入力の充足度(Observation Coverage、#32)。
//
// FK が少ない領域は「結合が弱い」のではなく「観測が薄い」だけかもしれない。
// 比較レポートを読む前に、各ソースがどれだけ見えていたかを数で出す。
// 分割案のグループごとにも、物理 FK の保有率が全体より大きく低いものに旗を立てる。
//
// 規律:
//   - 「0 件」と「読んでいない」を分ける。DB を読んでいない実行の
//     「物理 FK 0 本」は観測結果ではない。ソースごとに読んだかどうかを持つ
//   - 旗は「このグループの分割は入力が薄い」という読み方の注意であって、
//     結合が弱いという判定ではない
package main

import (
	"sort"
)

// thinCoverageRatio: グループの物理 FK 保有率が、全体の保有率のこの割合を
// 下回ったら「入力情報が薄い」とする。
const thinCoverageRatio = 0.5

// Coverage は入力の充足度。
type Coverage struct {
	// 各ソースを読んだか。false のとき、そのソースに由来する件数は
	// 「0 件だった」ではなく「数えていない」。
	PhysicalRead bool `json:"physical_read"`
	LogicalRead  bool `json:"logical_read"`
	CoocRead     bool `json:"cooc_read"`

	// DBTables: DB が見たテーブル数。
	DBTables int `json:"db_tables"`
	// TablesWithPhysicalFK: 物理 FK に子または親として関わるテーブル数。
	// どちらの端でも、DB がそのテーブルの関係を 1 本は強制している。
	TablesWithPhysicalFK int `json:"tables_with_physical_fk"`
	// PhysicalFKRate: TablesWithPhysicalFK / DBTables。DB のテーブルが 0 なら nil。
	PhysicalFKRate *float64 `json:"physical_fk_rate"`

	// LogicalTables: 静的解析が見たテーブル(モデル)数。
	LogicalTables int `json:"logical_tables"`
	// RelationsDeclared: 宣言の件数(belongs_to と has_many が同じ関係を
	// 両側から言っていれば 2 件)。Relations: 同じ関係を 1 本に畳んだ後の本数。
	// Duplicates: その差。
	RelationsDeclared int `json:"relations_declared"`
	Relations         int `json:"relations"`
	Duplicates        int `json:"duplicates"`

	// Edge Diff の分類ごとの本数(通常枠と hub 枠の合計)。
	Both         int `json:"both"`
	LogicalOnly  int `json:"logical_only"`
	PhysicalOnly int `json:"physical_only"`
	ObservedOnly int `json:"observed_only"`
	Undetermined int `json:"undetermined"`

	// CoocTx: 共起の集計に使ったトランザクション数。
	CoocTx int `json:"cooc_tx"`
}

// BuildCoverage は統合済みのスキャン結果と Edge Diff から充足度を数える。
func BuildCoverage(sc *ScanResult, ed EdgeDiff) Coverage {
	physTables, logicTables, physRead, logicRead := observedTables(sc)
	c := Coverage{PhysicalRead: physRead, LogicalRead: logicRead, CoocRead: sc.Cooc != nil}

	c.DBTables = len(physTables)
	c.TablesWithPhysicalFK = len(withPhysicalFK(sc, physTables))
	if c.DBTables > 0 {
		r := float64(c.TablesWithPhysicalFK) / float64(c.DBTables)
		c.PhysicalFKRate = &r
	}

	c.LogicalTables = len(logicTables)
	for _, fk := range sc.FKs {
		n := 0
		for _, ev := range fk.Evidences {
			if isLogicalSource(ev.Source) {
				n++
			}
		}
		if n > 0 {
			c.Relations++
			c.RelationsDeclared += n
		}
	}
	c.Duplicates = c.RelationsDeclared - c.Relations

	c.Both = ed.Counts.Both + ed.HubCounts.Both
	c.LogicalOnly = ed.Counts.LogicalOnly + ed.HubCounts.LogicalOnly
	c.PhysicalOnly = ed.Counts.PhysicalOnly + ed.HubCounts.PhysicalOnly
	c.ObservedOnly = ed.Counts.ObservedOnly + ed.HubCounts.ObservedOnly
	c.Undetermined = ed.Counts.Undetermined + ed.HubCounts.Undetermined

	if sc.Cooc != nil {
		c.CoocTx = sc.Cooc.TotalTx
	}
	return c
}

func isLogicalSource(src string) bool {
	return len(src) > 8 && src[:8] == "logical:"
}

// withPhysicalFK は、DB が見たテーブルのうち物理 FK に関わるものの集合。
func withPhysicalFK(sc *ScanResult, physTables []string) map[string]bool {
	inDB := make(map[string]bool, len(physTables))
	for _, t := range physTables {
		inDB[t] = true
	}
	out := map[string]bool{}
	mark := func(t string) {
		if inDB[t] {
			out[t] = true
		}
	}
	for _, fk := range sc.FKs {
		if fk.Enforced() {
			mark(fk.ChildTable)
			mark(fk.ParentTable)
		}
	}
	// スキーマ跨ぎの FK も、子テーブルにとっては DB が強制している関係。
	for _, fk := range sc.CrossFKs {
		if fk.Enforced() {
			mark(fk.ChildTable)
		}
	}
	return out
}

// 「入力情報が薄い」の理由。
const (
	// ThinBelowAverage: 物理 FK の保有率が全体の保有率を大きく下回る。
	ThinBelowAverage = "below_average"
	// ThinNotInDB: グループのテーブルが 1 つも DB に無い(宣言だけで出来たグループ)。
	ThinNotInDB = "not_in_db"
)

// GroupCoverage は分割案のグループ 1 つの充足度。
type GroupCoverage struct {
	Name   string `json:"name"`
	Tables int    `json:"tables"` // グループの非 hub テーブル数
	// DBTables: そのうち DB が見たテーブル数。保有率の分母。
	DBTables       int      `json:"db_tables"`
	WithPhysicalFK int      `json:"with_physical_fk"`
	PhysicalFKRate *float64 `json:"physical_fk_rate"` // DBTables が 0 なら nil
	// Thin: 入力情報が薄い。このグループの形は、業務上の結合より
	// FK の整備状況を写している可能性がある。結合が弱いという意味ではない。
	Thin       bool   `json:"thin,omitempty"`
	ThinReason string `json:"thin_reason,omitempty"`
}

// GroupCoverages は分割案のグループごとの充足度。名前順。
//
// DB を読んでいない実行では旗を立てない。全グループが同じ理由で薄く、
// 旗が情報にならないため(Coverage.PhysicalRead = false がそれを言う)。
func GroupCoverages(sc *ScanResult, view CommunityView, cov Coverage) []GroupCoverage {
	physTables, _, physRead, _ := observedTables(sc)
	inDB := make(map[string]bool, len(physTables))
	for _, t := range physTables {
		inDB[t] = true
	}
	covered := withPhysicalFK(sc, physTables)

	byName := map[string]*GroupCoverage{}
	var tables []string
	for t := range view.Assign {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		name := view.Assign[t]
		g, ok := byName[name]
		if !ok {
			g = &GroupCoverage{Name: name}
			byName[name] = g
		}
		g.Tables++
		if inDB[t] {
			g.DBTables++
			if covered[t] {
				g.WithPhysicalFK++
			}
		}
	}

	out := make([]GroupCoverage, 0, len(byName))
	for _, g := range byName {
		if g.DBTables > 0 {
			r := float64(g.WithPhysicalFK) / float64(g.DBTables)
			g.PhysicalFKRate = &r
		}
		if physRead {
			switch {
			case g.DBTables == 0:
				g.Thin, g.ThinReason = true, ThinNotInDB
			case cov.PhysicalFKRate != nil && *g.PhysicalFKRate < *cov.PhysicalFKRate*thinCoverageRatio:
				g.Thin, g.ThinReason = true, ThinBelowAverage
			}
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
