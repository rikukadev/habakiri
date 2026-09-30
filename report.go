// report.go: 解析結果の出力(テキスト / JSON / Mermaid)。
//
// 出力は 3 種類の行き先に分かれる:
//
//	今日切れる(橋) / 目指す境界(ブロック) / 人間が決める(hub・注記)
//
// クラスタの絵で終わらせず「切る FK の作業リスト」まで落とすのがこのツールの主張。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Analysis は解析の全結果(JSON 出力の形そのもの)。
type Analysis struct {
	Schema        string              `json:"schema"`
	TableCount    int                 `json:"table_count"`
	FKCount       int                 `json:"fk_count"`
	Isolated      []string            `json:"isolated_tables"` // FK が 1 本も無い = 既に自由
	Hubs          []Hub               `json:"shared_kernel"`   // 除外した高次数ノード
	HubThreshold  int                 `json:"hub_threshold"`
	CascadeGroups map[string][]string `json:"cascade_groups"`   // ライフサイクル一体(縮約済み)
	Blocks        [][]string          `json:"blocks"`           // 2-辺連結成分(縮約後ノード名)
	Edges         []EdgeReport        `json:"edges"`            // 縮約後の全エッジ(図の機械生成と外部消費用)
	// Islands: hub 経由でしか外と繋がらない島(hub 除去後にエッジ 0 本)。
	// 「もう hub との契約だけ整理すれば独立できる」塊で、孤立の次に自由度が高い。
	Islands []IslandReport `json:"islands"`
	Bridges       []BridgeReport      `json:"bridges"`          // 切断点
	ThinSeams     []SeamReport        `json:"thinnest_seams"`   // 橋が無いときの候補
	CrossFKs      []FK                `json:"cross_schema_fks"` // スキーマ跨ぎ(最優先で殲滅)
	Suspects      []Suspect           `json:"suspects,omitempty"` // FK ではない結合の疑い(静的ソース由来)
	Notes         []string            `json:"notes"`
}

// BridgeReport は橋 1 本の切断計画。
type BridgeReport struct {
	A          string  `json:"a"`
	B          string  `json:"b"`
	Weight     float64 `json:"weight"`
	SideASize  int     `json:"side_a_size"` // 切ったとき A 側に残るノード数
	SideBSize  int     `json:"side_b_size"`
	FKs        []FK    `json:"fks"`
	Difficulty string  `json:"difficulty"` // 易 / 中
}

// EdgeReport は縮約後グラフの 1 エッジ(橋かどうかの印付き)。
// MaxWeight は束ねた FK の最大重み(1=NULL可 / 2=NOT NULL / 3=CASCADE)。
// Weight が合計なのに対し、こちらは「この結合の最も強い性質」— 図の色分けに使う。
type EdgeReport struct {
	A         string  `json:"a"`
	B         string  `json:"b"`
	Weight    float64 `json:"weight"`
	MaxWeight float64 `json:"max_weight"`
	FKCount   int     `json:"fk_count"`
	Bridge    bool    `json:"bridge"`
	FKs       []FK    `json:"fks"` // 図のラベル(列名・向き)と外部消費用
}

// IslandReport は hub 経由のみで繋がる島 1 つ(CASCADE 集約なら Tables > 1)。
type IslandReport struct {
	Name   string `json:"name"`
	Tables int    `json:"tables"`
}

// SeamReport は橋ではないが最も細い継ぎ目。
type SeamReport struct {
	A      string  `json:"a"`
	B      string  `json:"b"`
	Weight float64 `json:"weight"`
	FKs    int     `json:"fk_count"`
}

// Analyze がパイプライン本体。
func Analyze(sc *ScanResult, hubThreshold int) *Analysis {
	a := &Analysis{
		Schema:     sc.Schema,
		TableCount: len(sc.Tables),
		FKCount:    len(sc.FKs) + len(sc.CrossFKs),
		CrossFKs:   sc.CrossFKs,
		Suspects:   sc.Suspects,
		Notes:      append([]string(nil), sc.Notes...), // ソース固有の注意を合流
	}

	// 孤立テーブル(どの FK にも現れない)
	inGraph := map[string]bool{}
	for _, fk := range sc.FKs {
		inGraph[fk.ChildTable] = true
		inGraph[fk.ParentTable] = true
	}
	// スキーマ跨ぎ FK の子は「孤立」ではない(既に Cross 枠で報告される)。
	for _, fk := range sc.CrossFKs {
		inGraph[fk.ChildTable] = true
	}
	for _, t := range sc.Tables {
		if !inGraph[t] {
			a.Isolated = append(a.Isolated, t)
		}
	}

	// 1. 束ね → 2. hub 除外 → 3. CASCADE 縮約。
	//
	// hub が先、縮約が後。逆にすると壊れることが Magento 2(295 テーブル)で
	// 実証された: store / eav_attribute のような hub へも掃除目的の CASCADE が
	// 大量に張られており、先に縮約すると hub 経由で 190 テーブルが
	// 「ライフサイクル一体」に融合した。CASCADE が意味するのは親子の従属で
	// あって、hub への CASCADE は集約の証拠ではない。hub を先に外せば、
	// 縮約は局所的な親子(注文ファミリー等)に限定される。
	edges := BuildEdges(sc.FKs)
	nodeSet := map[string]bool{}
	for p := range edges {
		nodeSet[p.A] = true
		nodeSet[p.B] = true
	}
	if hubThreshold <= 0 {
		hubThreshold = AutoHubThreshold(len(nodeSet))
	}
	a.HubThreshold = hubThreshold
	edges, a.Hubs = RemoveHubs(edges, hubThreshold)
	edges, a.CascadeGroups = Contract(edges)

	// 縮約が異常肥大したら警告(全体の 25% 超)。CASCADE の使われ方が
	// 「従属の掃除」寄りのスキーマで、集約推定として信用できない印。
	for root, ms := range a.CascadeGroups {
		if len(ms)*4 > len(nodeSet) {
			a.Notes = append(a.Notes, fmt.Sprintf(
				"CASCADE 集約 [%s] が %d テーブル(全体の 25%%超)に達した — この規模は集約ではなく「hub への掃除 CASCADE」の融合を疑う。--hub を下げて hub を増やすと分解されることが多い", root, len(ms)))
		}
	}

	// 4. 橋 → 5. ブロック
	bridges := Bridges(edges)
	a.Blocks = Blocks(edges, bridges)
	blockOf := BlockOf(a.Blocks)

	// 縮約後の全エッジ(図とJSON消費者向け)。決定的な順序で。
	bridgeSet := map[Pair]bool{}
	for _, bp := range bridges {
		bridgeSet[bp] = true
	}
	// Contract の再束ねで FK の並びが map 順に揺れるので、ここで固定する
	// (--d2 / --svg のバイト決定性はこの順序に依存する)。
	sortFKs := func(fks []FK) []FK {
		out := append([]FK(nil), fks...)
		sort.Slice(out, func(i, j int) bool {
			if out[i].ChildTable != out[j].ChildTable {
				return out[i].ChildTable < out[j].ChildTable
			}
			if ci, cj := strings.Join(out[i].ChildCols, ","), strings.Join(out[j].ChildCols, ","); ci != cj {
				return ci < cj
			}
			return out[i].Constraint < out[j].Constraint
		})
		return out
	}
	for p, e := range edges {
		maxW := 0.0
		for _, fk := range e.FKs {
			if w := fkWeight(fk); w > maxW {
				maxW = w
			}
		}
		a.Edges = append(a.Edges, EdgeReport{
			A: p.A, B: p.B, Weight: e.Weight, MaxWeight: maxW,
			FKCount: len(e.FKs), Bridge: bridgeSet[p], FKs: sortFKs(e.FKs)})
	}
	sort.Slice(a.Edges, func(i, j int) bool {
		if a.Edges[i].A != a.Edges[j].A {
			return a.Edges[i].A < a.Edges[j].A
		}
		return a.Edges[i].B < a.Edges[j].B
	})

	// 縮約ノード 1 個が実テーブル N 個を含むことがあるので、人に見せる
	// サイズは常にテーブル数で数える(ノード数だと過小に見える)。
	tableCount := func(node string) int {
		if ms, ok := a.CascadeGroups[node]; ok {
			return len(ms)
		}
		return 1
	}
	blockTables := func(bi int) int {
		n := 0
		for _, m := range a.Blocks[bi] {
			n += tableCount(m)
		}
		return n
	}

	for _, bp := range bridges {
		e := edges[bp]
		br := BridgeReport{A: bp.A, B: bp.B, Weight: e.Weight, FKs: sortFKs(e.FKs)}
		// 橋を切った後、両端は別ブロックに落ちる…のではなく、橋除去後の
		// ブロック表で両端のブロックサイズを引く(橋はブロック間の辺)。
		br.SideASize = blockTables(blockOf[bp.A])
		br.SideBSize = blockTables(blockOf[bp.B])
		br.Difficulty = "易(NULL 許容のみ — 値参照化だけで切れる)"
		for _, fk := range e.FKs {
			if fk.AllNotNull {
				br.Difficulty = "中(NOT NULL あり — 既定値かバックフィルの設計が要る)"
				break
			}
		}
		a.Bridges = append(a.Bridges, br)
	}
	sort.Slice(a.Bridges, func(i, j int) bool {
		return a.Bridges[i].Weight < a.Bridges[j].Weight // 軽いものから着手
	})

	// hub 経由のみの島: どのブロックにも孤立にも hub にも入らないテーブル。
	// CASCADE 集約の代表で畳んで数える。DB スキャンでは大物(sales 系など)が
	// ここに落ちるので、図に出さないと「ほぼ空のグラフ」に見えてしまう。
	inBlocks := map[string]bool{}
	for _, block := range a.Blocks {
		for _, n := range block {
			inBlocks[n] = true
			for _, m := range a.CascadeGroups[n] {
				inBlocks[m] = true
			}
		}
	}
	isolatedSet := map[string]bool{}
	for _, t := range a.Isolated {
		isolatedSet[t] = true
	}
	hubSet := map[string]bool{}
	for _, h := range a.Hubs {
		hubSet[h.Node] = true
	}
	memberRep := map[string]string{}
	for root, ms := range a.CascadeGroups {
		for _, m := range ms {
			memberRep[m] = root
		}
	}
	islandSeen := map[string]bool{}
	for _, t := range sc.Tables {
		if inBlocks[t] || isolatedSet[t] || hubSet[t] {
			continue
		}
		name, tables := t, 1
		if rep, ok := memberRep[t]; ok {
			name, tables = rep, len(a.CascadeGroups[rep])
		}
		if islandSeen[name] {
			continue
		}
		islandSeen[name] = true
		a.Islands = append(a.Islands, IslandReport{Name: name, Tables: tables})
	}
	sort.Slice(a.Islands, func(i, j int) bool {
		if a.Islands[i].Tables != a.Islands[j].Tables {
			return a.Islands[i].Tables > a.Islands[j].Tables
		}
		return a.Islands[i].Name < a.Islands[j].Name
	})

	for _, e := range ThinnestSeams(edges, bridges, 5) {
		a.ThinSeams = append(a.ThinSeams, SeamReport{
			A: e.A, B: e.B, Weight: e.Weight, FKs: len(e.FKs)})
	}

	a.Notes = append(a.Notes,
		"FK が無いことは無関係の証明ではない — アプリ層 JOIN・ポリモーフィック関連は静的スキャンでは見えない。クエリログ由来の共起で補うのは今後の拡張。",
		"CASCADE 集約は「切らない」判断を機械化したもの。切りたくなったらまず CASCADE を外す設計判断が先。",
	)
	return a
}

// WriteText は人間向けレポート。
func WriteText(w io.Writer, a *Analysis) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }

	p("スキーマ %s: %d テーブル / %d FK", a.Schema, a.TableCount, a.FKCount)
	p("")

	if len(a.CrossFKs) > 0 {
		p("■ スキーマ跨ぎ FK(%d 本)— DDL ロックが他スキーマに波及する。最優先で切る", len(a.CrossFKs))
		for _, fk := range a.CrossFKs {
			p("  %s.%s → %s.%s (%s)", a.Schema, fk.ChildTable, fk.ParentSchema, fk.ParentTable, fk.Constraint)
		}
		p("")
	}

	if len(a.Isolated) > 0 {
		p("■ 孤立テーブル(%d)— FK が無く、今日でも動かせる", len(a.Isolated))
		p("  %s", strings.Join(a.Isolated, ", "))
		p("")
	}

	if len(a.Hubs) > 0 {
		p("■ shared kernel 候補(次数 >= %d で除外)— 分割せず参照データとして共有 or 複製", a.HubThreshold)
		for _, h := range a.Hubs {
			p("  %-30s 次数 %d", h.Node, h.Degree)
		}
		p("")
	}

	if len(a.CascadeGroups) > 0 {
		p("■ CASCADE 集約(ライフサイクル一体 — 同じサービスから出さない)")
		var roots []string
		for r := range a.CascadeGroups {
			roots = append(roots, r)
		}
		sort.Strings(roots)
		for _, r := range roots {
			p("  [%s] %s", r, strings.Join(a.CascadeGroups[r], ", "))
		}
		p("")
	}

	if len(a.Islands) > 0 {
		p("■ hub 経由のみで繋がる島(%d)— hub との参照を値化すれば独立できる", len(a.Islands))
		for _, is := range a.Islands {
			if is.Tables > 1 {
				p("  %s (+%d)", is.Name, is.Tables-1)
			} else {
				p("  %s", is.Name)
			}
		}
		p("")
	}

	if len(a.Bridges) > 0 {
		p("■ 橋 = 切断点(%d 本)— 1 本切るだけで塊が分離する", len(a.Bridges))
		for _, b := range a.Bridges {
			p("  %s ×— %s   重み %.0f   分離後 %d ↔ %d テーブル", b.A, b.B, b.Weight, b.SideASize, b.SideBSize)
			for _, fk := range b.FKs {
				null := "NULL可"
				if fk.AllNotNull {
					null = "NOT NULL"
				}
				p("      %s.%s(%s) → %s  [%s / %s]", fk.ChildTable,
					strings.Join(fk.ChildCols, ","), null, fk.ParentTable, fk.DeleteRule, fk.Constraint)
			}
			p("      難易度: %s", b.Difficulty)
		}
		p("")
	} else {
		p("■ 橋なし — 1 本で分離できるポイントは無い(密結合)。最薄の継ぎ目から:")
		for _, s := range a.ThinSeams {
			p("  %s — %s   重み %.0f (FK %d 本)", s.A, s.B, s.Weight, s.FKs)
		}
		p("")
	}

	p("■ ブロック(2-辺連結成分)— 内部は密。これ以上の分割は段階 2 の設計判断")
	for i, b := range a.Blocks {
		total := 0
		names := make([]string, 0, len(b))
		for _, n := range b {
			if ms, ok := a.CascadeGroups[n]; ok {
				total += len(ms)
				names = append(names, fmt.Sprintf("%s(+%d)", n, len(ms)-1))
			} else {
				total++
				names = append(names, n)
			}
		}
		label := strings.Join(names, ", ")
		if len(names) > 8 {
			label = strings.Join(names[:8], ", ") + fmt.Sprintf(" … 他 %d", len(names)-8)
		}
		p("  B%-2d (%d tables) %s", i, total, label)
	}
	p("")
	for _, n := range a.Notes {
		p("注: %s", n)
	}
}

// WriteJSON は機械可読出力。
func WriteJSON(w io.Writer, a *Analysis) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(a)
}

// WriteMermaid は橋ブロック木を図にする(ブロック = subgraph、橋 = 太線)。
func WriteMermaid(w io.Writer, a *Analysis) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }
	p("flowchart LR")
	for i, b := range a.Blocks {
		p("  subgraph B%d[\"B%d (%d tables)\"]", i, i, len(b))
		for _, n := range b {
			p("    %s[\"%s\"]", sanitizeID(n), n)
		}
		p("  end")
	}
	for _, h := range a.Hubs {
		p("  %s{{\"%s (hub, deg %d)\"}}", sanitizeID(h.Node), h.Node, h.Degree)
	}
	for _, b := range a.Bridges {
		p("  %s ==\"cut? w=%.0f\"==> %s", sanitizeID(b.A), b.Weight, sanitizeID(b.B))
	}
}

func sanitizeID(s string) string {
	return strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(s)
}
