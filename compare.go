// compare.go: --compare-graphs の本体。Physical / Logical / Combined を共通条件
// (compare_prep.go)で解析し、結果を 1 つの Comparison にまとめる。
//
// 差分は「乖離の候補」として出す。FK が少ないグラフで辺や所属が無いのは、
// 関係が無いと確認できたからではなく、その入力に現れなかっただけのことがある。
package main

import (
	"fmt"
	"html"
	"io"
	"sort"
	"strings"
)

// GraphSummary は 1 つの見方の要約。
type GraphSummary struct {
	FKCount     int                `json:"fk_count"`
	Modularity  float64            `json:"modularity"`
	Communities []CommunitySummary `json:"communities"`
	// Isolated: この見方で辺を持たない非 hub テーブル(入力はそのテーブルを見ている)。
	Isolated []string `json:"isolated"`
	// Unobserved: この見方の入力がそもそも見ていないテーブル(DB に無いテーブルを
	// physical で、など)。孤立と違い、辺が無いことを確かめていない。
	Unobserved []string `json:"unobserved"`
}

// CommunitySummary はコミュニティ 1 つ(テーブル単位に展開済み)。
type CommunitySummary struct {
	Name   string   `json:"name"`
	Tables []string `json:"tables"`
}

// CommunityComparison は見方の対ごとの分割比較。
type CommunityComparison struct {
	Pairs []CommunityDiff `json:"pairs"`
}

// CoverageReport は入力の充足度(全体 + combined の分割のグループごと)。
type CoverageReport struct {
	Coverage
	Groups []GroupCoverage `json:"groups"`
}

// Comparison は比較モードの全結果(JSON の comparison)。
type Comparison struct {
	Common        CommonConditions        `json:"common"`
	Coverage      CoverageReport          `json:"coverage"`
	Graphs        map[string]GraphSummary `json:"graphs"`
	EdgeDiff      EdgeDiff                `json:"edge_diff"`
	CommunityDiff CommunityComparison     `json:"community_diff"`
}

// comparePairs は比べる見方の対(順序固定)。
var comparePairs = [][2]GraphKind{
	{GraphPhysical, GraphLogical},
	{GraphPhysical, GraphCombined},
	{GraphLogical, GraphCombined},
}

// BuildComparison は前処理の結果から比較をまとめる。
func BuildComparison(ci *ComparisonInput) *Comparison {
	c := &Comparison{Common: ci.Common, Graphs: map[string]GraphSummary{}}
	views := map[GraphKind]CommunityView{}
	// 見方ごとに「入力が見たテーブル」。Edge Diff と同じ判定(compare_edges.go)。
	physSeen, logicSeen := observedScopes(ci.Combined)
	seen := map[GraphKind]func(string) bool{
		GraphPhysical: physSeen,
		GraphLogical:  logicSeen,
		GraphCombined: func(t string) bool { return physSeen(t) || logicSeen(t) },
	}
	for _, v := range ci.Views {
		cv := CommunityViewOf(v.Kind.String(), v.Analysis)
		cv.Unobserved = map[string]bool{}
		for _, t := range cv.Tables {
			if _, ok := cv.Assign[t]; !ok && !seen[v.Kind](t) {
				cv.Unobserved[t] = true
			}
		}
		views[v.Kind] = cv

		byName := map[string][]string{}
		isolated, unobserved := []string{}, []string{}
		for _, t := range cv.Tables {
			switch name, ok := cv.Assign[t]; {
			case ok:
				byName[name] = append(byName[name], t)
			case cv.Unobserved[t]:
				unobserved = append(unobserved, t)
			default:
				isolated = append(isolated, t)
			}
		}
		gs := GraphSummary{FKCount: len(v.Scan.FKs), Modularity: cv.Modularity,
			Communities: []CommunitySummary{}, Isolated: isolated, Unobserved: unobserved}
		for name, ts := range byName {
			gs.Communities = append(gs.Communities, CommunitySummary{Name: name, Tables: ts})
		}
		sort.Slice(gs.Communities, func(i, j int) bool {
			x, y := gs.Communities[i], gs.Communities[j]
			if len(x.Tables) != len(y.Tables) {
				return len(x.Tables) > len(y.Tables)
			}
			return x.Name < y.Name
		})
		c.Graphs[v.Kind.String()] = gs
	}
	var hubNames []string
	for _, h := range ci.Common.Hubs {
		hubNames = append(hubNames, h.Node)
	}
	c.EdgeDiff = DiffEdges(ci.Combined, hubNames)
	// 旗は combined の分割に立てる。DB と宣言を合わせた形のうち、どの塊が
	// 実は宣言(や共起)だけで支えられているかを見るため。
	c.Coverage.Coverage = BuildCoverage(ci.Combined, c.EdgeDiff)
	c.Coverage.Groups = GroupCoverages(ci.Combined, views[GraphCombined], c.Coverage.Coverage)
	if c.Coverage.Groups == nil {
		c.Coverage.Groups = []GroupCoverage{}
	}
	for _, p := range comparePairs {
		c.CommunityDiff.Pairs = append(c.CommunityDiff.Pairs, DiffCommunities(views[p[0]], views[p[1]]))
	}
	return c
}

// WriteComparisonText は比較レポート(text)。
func WriteComparisonText(w io.Writer, c *Comparison) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }

	var hubs []string
	for _, h := range c.Common.Hubs {
		hubs = append(hubs, h.Node)
	}
	hubText := "なし"
	if len(hubs) > 0 {
		hubText = strings.Join(hubs, ", ")
	}
	p("■ グラフ比較(--compare-graphs)— 同じ条件で Physical / Logical / Combined を解析")
	p("  共通条件(combined から決定): hub = %s(次数閾値 %d)/ CASCADE 縮約 %d 群",
		hubText, c.Common.HubThreshold, len(c.Common.CascadeGroups))
	p("  ここに出る差分は乖離の候補。FK が少ない見方で辺や所属が無いのは「関係が無い」の確認ではない")
	p("")

	writeCoverageText(p, c.Coverage)
	p("")

	p("■ 見方ごとの分割")
	for _, kind := range []GraphKind{GraphPhysical, GraphLogical, GraphCombined} {
		g := c.Graphs[kind.String()]
		p("  %-8s FK %d 本 / コミュニティ %d / 孤立 %d / 未観測 %d / Q=%.2f",
			kind, g.FKCount, len(g.Communities), len(g.Isolated), len(g.Unobserved), g.Modularity)
		for _, cm := range g.Communities {
			p("      %s(%d): %s", cm.Name, len(cm.Tables), strings.Join(cm.Tables, ", "))
		}
		if len(g.Isolated) > 0 {
			p("      孤立(%d): %s", len(g.Isolated), strings.Join(g.Isolated, ", "))
		}
		if len(g.Unobserved) > 0 {
			p("      未観測(%d — この見方の入力がテーブル自体を見ていない): %s", len(g.Unobserved), strings.Join(g.Unobserved, ", "))
		}
	}
	p("")

	p("■ Edge Diff — 関係ごとに、DB の制約(Physical)・ORM の宣言(Logical)・実行時の共起(Observed)のどれにあるか")
	writeEdgeDiffText(p, "通常の関係", c.EdgeDiff.Edges, c.EdgeDiff.Counts)
	writeEdgeDiffText(p, "hub に接する関係", c.EdgeDiff.HubEdges, c.EdgeDiff.HubCounts)
	p("")

	p("■ Community Diff — 分割はどれだけ一致するか(ARI: 1 = 同じ分割 / 0 = 偶然と同程度)")
	for _, d := range c.CommunityDiff.Pairs {
		p("  %s × %s: ARI %s(共通頂点 %d)/ 両方で非孤立の頂点だけなら %s(%d)",
			d.A.Kind, d.B.Kind, ariText(d.ARI), d.Vertices, ariText(d.ARIConnected), d.ConnectedVertices)
		for _, m := range d.Moved {
			switch m.Kind {
			case MoveReassigned:
				p("      %s: %s → %s", m.Table, m.From, m.To)
			case MoveIsolatedInA:
				p("      %s: (%s では孤立) → %s", m.Table, d.A.Kind, m.To)
			case MoveIsolatedInB:
				p("      %s: %s → (%s では孤立)", m.Table, m.From, d.B.Kind)
			case MoveUnobservedInA:
				p("      %s: (%s では未観測) → %s", m.Table, d.A.Kind, m.To)
			case MoveUnobservedInB:
				p("      %s: %s → (%s では未観測)", m.Table, m.From, d.B.Kind)
			}
		}
		if len(d.CutEdges) > 0 {
			p("      片方でだけコミュニティを跨ぐ辺(両方で跨ぐ辺は %d 本):", d.CutBoth)
			for _, e := range d.CutEdges {
				p("        %s × %s: %s=%s / %s=%s", e.A, e.B, d.A.Kind, edgeStateText(e.StateA), d.B.Kind, edgeStateText(e.StateB))
			}
		}
	}
	p("")
}

func rateText(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", *v*100)
}

// thinText は旗の文言。結合が弱いとは言わない — 読み方の注意だけ。
func thinText(g GroupCoverage) string {
	switch {
	case !g.Thin:
		return ""
	case g.ThinReason == ThinNotInDB:
		return "⚠ 入力が薄い: このグループのテーブルは DB に無い(宣言だけで出来た塊)"
	}
	return "⚠ 入力が薄い: 物理 FK が全体より大幅に少ない — 結合が弱いのではなく、FK の整備状況を写している可能性"
}

func writeCoverageText(p func(string, ...any), cr CoverageReport) {
	c := cr.Coverage
	count := func(read bool, n int) string {
		if !read {
			return "読んでいない"
		}
		return fmt.Sprintf("%d", n)
	}
	p("■ Observation Coverage — 入力はどれだけ見えていたか(差分を読む前に)")
	p("  DB のテーブル              %s", count(c.PhysicalRead, c.DBTables))
	p("  物理 FK に関わるテーブル   %s(保有率 %s)", count(c.PhysicalRead, c.TablesWithPhysicalFK), rateText(c.PhysicalFKRate))
	p("  ORM が解析したテーブル     %s", count(c.LogicalRead, c.LogicalTables))
	p("  宣言された関係             %s 本(宣言 %d 件、両側からの重複 %d)", count(c.LogicalRead, c.Relations), c.RelationsDeclared, c.Duplicates)
	p("  DB と宣言が一致            %d 本", c.Both)
	p("  Logical Only / Physical Only / 判定不能   %d / %d / %d 本", c.LogicalOnly, c.PhysicalOnly, c.Undetermined)
	if c.CoocRead {
		p("  共起(Observed)           %d tx(観測期間はログに依る — 無いことの証明には使えない)/ Observed Only %d 対", c.CoocTx, c.ObservedOnly)
	} else {
		p("  共起(Observed)           読んでいない(--cooc 未指定)")
	}
	if len(cr.Groups) > 0 {
		p("  分割案(combined)のグループごとの物理 FK 保有率:")
		for _, g := range cr.Groups {
			line := fmt.Sprintf("      %s: %d テーブル中 DB にあるもの %d、うち物理 FK あり %d(%s)",
				g.Name, g.Tables, g.DBTables, g.WithPhysicalFK, rateText(g.PhysicalFKRate))
			if t := thinText(g); t != "" {
				line += "  " + t
			}
			p("%s", line)
		}
	}
}

// edgeClassNotes は分類の読み方。断定はしない(候補を並べるだけ)。
var edgeClassNotes = []struct{ class, label, note string }{
	{EdgeLogicalOnly, "Logical Only", "ORM にあるが DB が強制していない — FK 未整備か、意図的なアプリ側整合"},
	{EdgePhysicalOnly, "Physical Only", "DB の制約はあるが ORM に宣言が無い — ORM を通らない処理か、乖離の候補"},
	{EdgeObservedOnly, "Observed Only", "実行時に共起したが宣言が無い — 生 SQL / 動的クエリの調査対象"},
	{EdgeUndetermined, "判定不能", "片方のソースが端のテーブルを見ていない — 「無い」とは言えない"},
}

func edgeRowText(r EdgeDiffRow) string {
	s := r.A + " × " + r.B
	if r.ChildTable != "" {
		s = fmt.Sprintf("%s.%s → %s", r.ChildTable, strings.Join(r.ChildCols, ","), r.ParentTable)
	}
	if r.Observed == Present {
		s += fmt.Sprintf("  [共起 ×%d npmi=%.2f]", r.CoocCount, r.CoocNPMI)
	}
	return s
}

func writeEdgeDiffText(p func(string, ...any), title string, rows []EdgeDiffRow, n EdgeDiffCounts) {
	p("  %s(%d): 一致 %d / Logical Only %d / Physical Only %d / Observed Only %d / 判定不能 %d",
		title, len(rows), n.Both, n.LogicalOnly, n.PhysicalOnly, n.ObservedOnly, n.Undetermined)
	for _, cn := range edgeClassNotes {
		first := true
		for _, r := range rows {
			if r.Class != cn.class {
				continue
			}
			if first {
				p("    %s(%s):", cn.label, cn.note)
				first = false
			}
			p("        %s", edgeRowText(r))
		}
	}
}

func presenceMark(v Presence) string {
	switch v {
	case Present:
		return "●"
	case Absent:
		return "—"
	}
	return "?"
}

func edgeClassLabel(class string) string {
	if class == EdgeBoth {
		return "一致"
	}
	for _, cn := range edgeClassNotes {
		if cn.class == class {
			return cn.label
		}
	}
	return class
}

func ariText(v *float64) string {
	if v == nil {
		return "判定不能"
	}
	return fmt.Sprintf("%.2f", *v)
}

func edgeStateText(s string) string {
	switch s {
	case EdgeCut:
		return "跨ぐ"
	case EdgeInternal:
		return "内側"
	}
	return "辺なし"
}

// WriteComparisonHTML は比較レポート(--html の比較節)。表だけで組む(JS なし)。
func WriteComparisonHTML(w io.Writer, c *Comparison) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }
	esc := html.EscapeString

	var hubs []string
	for _, h := range c.Common.Hubs {
		hubs = append(hubs, h.Node)
	}
	hubText := "なし"
	if len(hubs) > 0 {
		hubText = strings.Join(hubs, ", ")
	}
	p(`<h2>グラフ比較 — 同じ条件で Physical / Logical / Combined を解析</h2>
<p class="sub">共通条件(combined から決定): hub = <code>%s</code>(次数閾値 %d)/ CASCADE 縮約 %d 群。
ここに出る差分は乖離の候補。FK が少ない見方で辺や所属が無いのは「関係が無い」の確認ではない。</p>`,
		esc(hubText), c.Common.HubThreshold, len(c.Common.CascadeGroups))

	cv := c.Coverage.Coverage
	count := func(read bool, n int) string {
		if !read {
			return "読んでいない"
		}
		return fmt.Sprintf("%d", n)
	}
	cooc := "読んでいない(--cooc 未指定)"
	if cv.CoocRead {
		cooc = fmt.Sprintf("%d tx / Observed Only %d 対", cv.CoocTx, cv.ObservedOnly)
	}
	p(`<h2>Observation Coverage — 入力はどれだけ見えていたか</h2>
<p class="sub">差分を読む前に。物理 FK が少ない領域は「結合が弱い」のではなく「観測が薄い」だけかもしれない。</p>
<div class="tw"><table>
<tr><th>DB のテーブル</th><td>%s</td></tr>
<tr><th>物理 FK に関わるテーブル</th><td>%s(保有率 %s)</td></tr>
<tr><th>ORM が解析したテーブル</th><td>%s</td></tr>
<tr><th>宣言された関係</th><td>%s 本(宣言 %d 件、両側からの重複 %d)</td></tr>
<tr><th>DB と宣言が一致</th><td>%d 本</td></tr>
<tr><th>Logical Only / Physical Only / 判定不能</th><td>%d / %d / %d 本</td></tr>
<tr><th>共起(Observed)</th><td>%s</td></tr>
</table></div>`,
		count(cv.PhysicalRead, cv.DBTables), count(cv.PhysicalRead, cv.TablesWithPhysicalFK), rateText(cv.PhysicalFKRate),
		count(cv.LogicalRead, cv.LogicalTables), count(cv.LogicalRead, cv.Relations), cv.RelationsDeclared, cv.Duplicates,
		cv.Both, cv.LogicalOnly, cv.PhysicalOnly, cv.Undetermined, esc(cooc))
	if len(c.Coverage.Groups) > 0 {
		p(`<div class="tw"><table><tr><th>グループ(combined の分割)</th><th>テーブル</th><th>DB にある</th><th>物理 FK あり</th><th>保有率</th><th></th></tr>`)
		for _, g := range c.Coverage.Groups {
			p(`<tr><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%s</td><td class="w3">%s</td></tr>`,
				esc(g.Name), g.Tables, g.DBTables, g.WithPhysicalFK, rateText(g.PhysicalFKRate), esc(thinText(g)))
		}
		p(`</table></div>`)
	}

	p(`<div class="tw"><table><tr><th>見方</th><th>FK</th><th>Q</th><th>コミュニティ</th><th>孤立</th></tr>`)
	for _, kind := range []GraphKind{GraphPhysical, GraphLogical, GraphCombined} {
		g := c.Graphs[kind.String()]
		var comms []string
		for _, cm := range g.Communities {
			comms = append(comms, fmt.Sprintf("<b>%s</b>(%d): <code>%s</code>",
				esc(cm.Name), len(cm.Tables), esc(strings.Join(cm.Tables, ", "))))
		}
		iso := "—"
		if len(g.Isolated) > 0 {
			iso = fmt.Sprintf("%d: <code>%s</code>", len(g.Isolated), esc(strings.Join(g.Isolated, ", ")))
		}
		if len(g.Unobserved) > 0 {
			iso += fmt.Sprintf("<br>未観測 %d: <code>%s</code>", len(g.Unobserved), esc(strings.Join(g.Unobserved, ", ")))
		}
		p(`<tr><td>%s</td><td>%d</td><td>%.2f</td><td>%s</td><td>%s</td></tr>`,
			kind, g.FKCount, g.Modularity, strings.Join(comms, "<br>"), iso)
	}
	p(`</table></div>`)

	edgeTable := func(title string, rows []EdgeDiffRow, n EdgeDiffCounts) {
		if len(rows) == 0 {
			return
		}
		p(`<p class="sub"><b>%s</b>(%d): 一致 %d / Logical Only %d / Physical Only %d / Observed Only %d / 判定不能 %d</p>
<div class="tw"><table><tr><th>関係</th><th>Physical</th><th>Logical</th><th>Observed</th><th>分類</th></tr>`,
			esc(title), len(rows), n.Both, n.LogicalOnly, n.PhysicalOnly, n.ObservedOnly, n.Undetermined)
		for _, r := range rows {
			cls := ""
			if r.Class != EdgeBoth {
				cls = ` class="w3"`
			}
			p(`<tr><td><code>%s</code></td><td>%s</td><td>%s</td><td>%s</td><td%s>%s</td></tr>`,
				esc(edgeRowText(r)), presenceMark(r.Physical), presenceMark(r.Logical), presenceMark(r.Observed),
				cls, esc(edgeClassLabel(r.Class)))
		}
		p(`</table></div>`)
	}
	p(`<h2>Edge Diff — 関係 × {Physical, Logical, Observed}</h2>
<p class="sub">● = ある / — = 見たうえで無い / ? = そのソースは見ていない(無いとは言えない)。
Logical Only は FK 未整備か意図的なアプリ側整合、Physical Only は ORM を通らない処理か乖離の候補、
Observed Only は生 SQL / 動的クエリの調査対象。</p>`)
	edgeTable("通常の関係", c.EdgeDiff.Edges, c.EdgeDiff.Counts)
	edgeTable("hub に接する関係", c.EdgeDiff.HubEdges, c.EdgeDiff.HubCounts)

	p(`<h2>Community Diff — 分割はどれだけ一致するか</h2>
<p class="sub">ARI: 1 = 同じ分割 / 0 = 偶然と同程度。「孤立」はその見方の入力がテーブルを見たうえで関係が現れなかったこと、「未観測」は入力がテーブル自体を見ていないこと。どちらも移動ではない。</p>
<div class="tw"><table><tr><th>対</th><th>ARI</th><th>非孤立のみ</th><th>所属が変わったテーブル</th><th>片方でだけ跨ぐ辺</th></tr>`)
	for _, d := range c.CommunityDiff.Pairs {
		var moved, cuts []string
		for _, m := range d.Moved {
			from, to := esc(m.From), esc(m.To)
			if m.Kind == MoveIsolatedInA {
				from = "(" + d.A.Kind + " では孤立)"
			}
			if m.Kind == MoveIsolatedInB {
				to = "(" + d.B.Kind + " では孤立)"
			}
			if m.Kind == MoveUnobservedInA {
				from = "(" + d.A.Kind + " では未観測)"
			}
			if m.Kind == MoveUnobservedInB {
				to = "(" + d.B.Kind + " では未観測)"
			}
			moved = append(moved, fmt.Sprintf("<code>%s</code>: %s → %s", esc(m.Table), from, to))
		}
		for _, e := range d.CutEdges {
			cuts = append(cuts, fmt.Sprintf("<code>%s × %s</code>: %s / %s",
				esc(e.A), esc(e.B), edgeStateText(e.StateA), edgeStateText(e.StateB)))
		}
		dash := func(xs []string) string {
			if len(xs) == 0 {
				return "—"
			}
			return strings.Join(xs, "<br>")
		}
		p(`<tr><td>%s × %s</td><td class="w3">%s</td><td>%s(%d 頂点)</td><td>%s</td><td>%s</td></tr>`,
			d.A.Kind, d.B.Kind, ariText(d.ARI), ariText(d.ARIConnected), d.ConnectedVertices, dash(moved), dash(cuts))
	}
	p(`</table></div>`)
}
