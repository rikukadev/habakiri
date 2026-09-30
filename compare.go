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
	// Isolated: この見方で辺を持たない非 hub テーブル。
	Isolated []string `json:"isolated"`
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

// Comparison は比較モードの全結果(JSON の comparison)。
type Comparison struct {
	Common        CommonConditions        `json:"common"`
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
	for _, v := range ci.Views {
		cv := CommunityViewOf(v.Kind.String(), v.Analysis)
		views[v.Kind] = cv

		byName := map[string][]string{}
		var isolated []string
		for _, t := range cv.Tables {
			if name, ok := cv.Assign[t]; ok {
				byName[name] = append(byName[name], t)
			} else {
				isolated = append(isolated, t)
			}
		}
		gs := GraphSummary{FKCount: len(v.Scan.FKs), Modularity: cv.Modularity,
			Communities: []CommunitySummary{}, Isolated: isolated}
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
		if gs.Isolated == nil {
			gs.Isolated = []string{}
		}
		c.Graphs[v.Kind.String()] = gs
	}
	var hubNames []string
	for _, h := range ci.Common.Hubs {
		hubNames = append(hubNames, h.Node)
	}
	c.EdgeDiff = DiffEdges(ci.Combined, hubNames)
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

	p("■ 見方ごとの分割")
	for _, kind := range []GraphKind{GraphPhysical, GraphLogical, GraphCombined} {
		g := c.Graphs[kind.String()]
		p("  %-8s FK %d 本 / コミュニティ %d / 孤立 %d / Q=%.2f",
			kind, g.FKCount, len(g.Communities), len(g.Isolated), g.Modularity)
		for _, cm := range g.Communities {
			p("      %s(%d): %s", cm.Name, len(cm.Tables), strings.Join(cm.Tables, ", "))
		}
		if len(g.Isolated) > 0 {
			p("      孤立(%d): %s", len(g.Isolated), strings.Join(g.Isolated, ", "))
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
<p class="sub">ARI: 1 = 同じ分割 / 0 = 偶然と同程度。「孤立」は移動ではなく、その見方の入力に関係が現れなかったことを指す。</p>
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
