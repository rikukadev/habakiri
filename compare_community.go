// compare_community.go: 2 つのグラフ(physical / logical / combined)の分割結果を比べる(#31)。
//
// 出すもの:
//   - ARI(Adjusted Rand Index)— 共通の非 hub 頂点の上で
//   - 所属が変わったテーブル(どこからどこへ)
//   - コミュニティ間を跨ぐ辺(Cut Edge)の差分
//
// 規律:
//   - 差分は「乖離の候補」であって、設計上の問題の断定ではない。FK が少ない
//     グラフでは「結合が弱い」のではなく「観測が薄い」だけのことがある。
//     そのため「孤立している」「辺が無い」は、所属の移動や切断と混ぜずに
//     別の種類として出す
//   - 乱数・時刻・map 順への依存なし。集計は整数で行い、出力はソートする
package main

import (
	"sort"
)

// CommunityView は 1 つのグラフの分割を、比較できる形に展開したもの。
type CommunityView struct {
	Kind string
	// Tables: このグラフが知っている非 hub テーブルの全体(孤立を含む)。
	Tables []string
	// Assign: テーブル → コミュニティ名。どのコミュニティにも属さない
	// (孤立した)テーブルは入れない。
	Assign map[string]string
	// Edges: 非 hub の縮約ノード対。名前は縮約ノードの代表テーブル名。
	Edges      []Pair
	Modularity float64
}

// CommunityViewOf は解析結果から比較用の分割を取り出す。
// 分割案はユニット(ブロック/島)単位なので、縮約を解いてテーブル単位に展開する。
func CommunityViewOf(kind string, a *Analysis) CommunityView {
	v := CommunityView{Kind: kind, Assign: map[string]string{}}
	expand := func(node string) []string {
		if ms, ok := a.CascadeGroups[node]; ok {
			// 代表名がメンバーに含まれない実装になっても取りこぼさないよう、両方入れる
			return append([]string{node}, ms...)
		}
		return []string{node}
	}
	// hub は縮約ノード。CASCADE で一体になったテーブルごと外す
	hub := map[string]bool{}
	for _, h := range a.Hubs {
		for _, t := range expand(h.Node) {
			hub[t] = true
		}
	}
	// ユニット代表 → 縮約ノード(ブロックは辞書順先頭が代表、島は自分自身)
	unitNodes := map[string][]string{}
	for _, block := range a.Blocks {
		members := append([]string(nil), block...)
		sort.Strings(members)
		if len(members) > 0 {
			unitNodes[members[0]] = members
		}
	}
	for _, is := range a.Islands {
		unitNodes[is.Name] = []string{is.Name}
	}

	seen := map[string]bool{}
	add := func(t string) {
		if !hub[t] && !seen[t] {
			seen[t] = true
			v.Tables = append(v.Tables, t)
		}
	}
	if a.Partition != nil {
		v.Modularity = a.Partition.Modularity
		for _, g := range a.Partition.Groups {
			for _, u := range g.Units {
				nodes, ok := unitNodes[u]
				if !ok {
					nodes = []string{u}
				}
				for _, n := range nodes {
					for _, t := range expand(n) {
						if hub[t] {
							continue
						}
						v.Assign[t] = g.Name
						add(t)
					}
				}
			}
		}
	}
	for _, t := range a.Isolated {
		add(t)
	}
	for _, e := range a.Edges {
		if hub[e.A] || hub[e.B] {
			continue
		}
		v.Edges = append(v.Edges, mkPair(e.A, e.B))
		add(e.A)
		add(e.B)
	}
	sort.Strings(v.Tables)
	sort.Slice(v.Edges, func(i, j int) bool { return pairLess(v.Edges[i], v.Edges[j]) })
	return v
}

func pairLess(p, q Pair) bool {
	if p.A != q.A {
		return p.A < q.A
	}
	return p.B < q.B
}

// CommunitySide は比較の片側の要約。
type CommunitySide struct {
	Kind        string  `json:"kind"`
	Communities int     `json:"communities"`
	Modularity  float64 `json:"modularity"`
	// Isolated: 共通頂点のうち、このグラフではどのコミュニティにも属さない数。
	// ARI を読むときの前提(多いほど、ARI はこのグラフの観測の薄さに引っ張られる)。
	Isolated int `json:"isolated"`
}

// CommunityMatch は A のコミュニティと B のコミュニティの対応 1 組。
type CommunityMatch struct {
	A       string `json:"a"`
	B       string `json:"b"`
	Overlap int    `json:"overlap"` // 両方に属する共通頂点の数
}

// 所属変化の種類。
const (
	// MoveReassigned: 両方のグラフでコミュニティに属していて、対応しない先へ移った。
	MoveReassigned = "reassigned"
	// MoveIsolatedInA / MoveIsolatedInB: 片方では孤立している。関係が無いと
	// 確認できたわけではなく、そのグラフの入力に関係が現れなかっただけの
	// ことがあるので、reassigned とは分ける。
	MoveIsolatedInA = "isolated_in_a"
	MoveIsolatedInB = "isolated_in_b"
)

// MovedTable は所属が変わったテーブル 1 件。From / To の空文字は孤立。
type MovedTable struct {
	Table string `json:"table"`
	From  string `json:"from"` // A でのコミュニティ
	To    string `json:"to"`   // B でのコミュニティ
	Kind  string `json:"kind"`
}

// 辺の状態(そのグラフでの)。
const (
	EdgeCut      = "cut"      // 辺があり、両端が別のコミュニティ
	EdgeInternal = "internal" // 辺があり、両端が同じコミュニティ
	// EdgeAbsent: このグラフには辺が無い。「関係が無い」の確認ではない。
	EdgeAbsent = "absent"
)

// CutEdgeDiff は、片方でだけコミュニティを跨ぐ辺 1 本。
type CutEdgeDiff struct {
	A      string `json:"a"`
	B      string `json:"b"`
	StateA string `json:"state_a"`
	StateB string `json:"state_b"`
}

// CommunityDiff は 2 つの分割の比較結果。
type CommunityDiff struct {
	A CommunitySide `json:"a"`
	B CommunitySide `json:"b"`
	// Vertices: 比較に使った共通の非 hub 頂点の数。
	// Excluded: 片方にしか無い(片方で hub 扱い、または片方が知らない)ため外した数。
	Vertices int `json:"vertices"`
	Excluded int `json:"excluded"`
	// ARI: 共通頂点の上での Adjusted Rand Index。孤立頂点は 1 点だけの
	// コミュニティとして数える。頂点が 2 未満なら nil(判定不能)。
	ARI *float64 `json:"ari"`
	// ARIConnected: 両方のグラフで孤立していない頂点だけで測った ARI。
	// ARI との差が大きいときは、差の多くが「片方で孤立」に由来する。
	ARIConnected      *float64 `json:"ari_connected"`
	ConnectedVertices int      `json:"connected_vertices"`

	Matches []CommunityMatch `json:"matches"`
	Moved   []MovedTable     `json:"moved"`
	// CutEdges: 片方でだけコミュニティを跨ぐ辺。CutBoth は両方で跨ぐ辺の数。
	CutEdges []CutEdgeDiff `json:"cut_edges"`
	CutBoth  int           `json:"cut_both"`
}

// DiffCommunities は 2 つの分割を共通の非 hub 頂点の上で比べる。
func DiffCommunities(a, b CommunityView) CommunityDiff {
	d := CommunityDiff{
		A: CommunitySide{Kind: a.Kind, Modularity: a.Modularity},
		B: CommunitySide{Kind: b.Kind, Modularity: b.Modularity},
	}
	inB := map[string]bool{}
	for _, t := range b.Tables {
		inB[t] = true
	}
	var verts []string
	common := map[string]bool{}
	for _, t := range a.Tables {
		if inB[t] {
			verts = append(verts, t)
			common[t] = true
		}
	}
	sort.Strings(verts)
	d.Vertices = len(verts)
	d.Excluded = (len(a.Tables) - len(verts)) + (len(b.Tables) - len(verts))

	commA, commB := map[string]bool{}, map[string]bool{}
	var connected []string
	for _, t := range verts {
		ca, okA := a.Assign[t]
		cb, okB := b.Assign[t]
		if okA {
			commA[ca] = true
		} else {
			d.A.Isolated++
		}
		if okB {
			commB[cb] = true
		} else {
			d.B.Isolated++
		}
		if okA && okB {
			connected = append(connected, t)
		}
	}
	d.A.Communities, d.B.Communities = len(commA), len(commB)
	d.ConnectedVertices = len(connected)

	// 孤立頂点は 1 点だけのコミュニティ。名前が実コミュニティと衝突しないよう
	// 制御文字で始める。
	label := func(assign map[string]string, t string) string {
		if c, ok := assign[t]; ok {
			return c
		}
		return "\x00" + t
	}
	d.ARI = adjustedRand(verts,
		func(t string) string { return label(a.Assign, t) },
		func(t string) string { return label(b.Assign, t) })
	d.ARIConnected = adjustedRand(connected,
		func(t string) string { return a.Assign[t] },
		func(t string) string { return b.Assign[t] })

	d.Matches = matchCommunities(connected, a.Assign, b.Assign)
	counterpart := map[string]string{}
	for _, m := range d.Matches {
		counterpart[m.A] = m.B
	}
	for _, t := range verts {
		ca, okA := a.Assign[t]
		cb, okB := b.Assign[t]
		switch {
		case okA && okB:
			if to, ok := counterpart[ca]; !ok || to != cb {
				d.Moved = append(d.Moved, MovedTable{Table: t, From: ca, To: cb, Kind: MoveReassigned})
			}
		case okA && !okB:
			d.Moved = append(d.Moved, MovedTable{Table: t, From: ca, Kind: MoveIsolatedInB})
		case !okA && okB:
			d.Moved = append(d.Moved, MovedTable{Table: t, To: cb, Kind: MoveIsolatedInA})
		}
	}

	stateOf := func(v CommunityView) map[Pair]string {
		st := map[Pair]string{}
		for _, p := range v.Edges {
			if !common[p.A] || !common[p.B] {
				continue
			}
			ca, okA := v.Assign[p.A]
			cb, okB := v.Assign[p.B]
			if okA && okB && ca == cb {
				st[p] = EdgeInternal
			} else {
				st[p] = EdgeCut
			}
		}
		return st
	}
	stA, stB := stateOf(a), stateOf(b)
	var pairs []Pair
	for p := range stA {
		pairs = append(pairs, p)
	}
	for p := range stB {
		if _, dup := stA[p]; !dup {
			pairs = append(pairs, p)
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairLess(pairs[i], pairs[j]) })
	for _, p := range pairs {
		sa, sb := stA[p], stB[p]
		if sa == "" {
			sa = EdgeAbsent
		}
		if sb == "" {
			sb = EdgeAbsent
		}
		switch {
		case sa == EdgeCut && sb == EdgeCut:
			d.CutBoth++
		case sa == EdgeCut || sb == EdgeCut:
			d.CutEdges = append(d.CutEdges, CutEdgeDiff{A: p.A, B: p.B, StateA: sa, StateB: sb})
		}
	}
	return d
}

// adjustedRand は 2 つのラベル付けの Adjusted Rand Index を返す。
// 頂点が 2 未満なら nil(対が 1 つも無く、一致も不一致も測れない)。
//
// 対の数え上げは整数で行い、最後の 1 回だけ浮動小数に落とす
// (加算順で結果がラストビット揺れるのを避ける)。
func adjustedRand(verts []string, la, lb func(string) string) *float64 {
	n := int64(len(verts))
	if n < 2 {
		return nil
	}
	type cell struct{ a, b string }
	nij := map[cell]int64{}
	ai, bj := map[string]int64{}, map[string]int64{}
	for _, t := range verts {
		x, y := la(t), lb(t)
		nij[cell{x, y}]++
		ai[x]++
		bj[y]++
	}
	c2 := func(k int64) int64 { return k * (k - 1) / 2 }
	var sumIJ, sumA, sumB int64
	for _, k := range nij {
		sumIJ += c2(k)
	}
	for _, k := range ai {
		sumA += c2(k)
	}
	for _, k := range bj {
		sumB += c2(k)
	}
	total := c2(n)
	expected := float64(sumA) * float64(sumB) / float64(total)
	max := float64(sumA+sumB) / 2
	var ari float64
	if max == expected {
		// 分母が 0 になるのは、両方が「全員 1 点ずつ」または「全員同じ」で
		// 一致しているときだけ。完全一致として 1 を返す。
		ari = 1
	} else {
		ari = (float64(sumIJ) - expected) / (max - expected)
	}
	return &ari
}

// matchCommunities は A と B のコミュニティを 1 対 1 に対応づける。
// 重なり(共通頂点の数)が大きい組から貪欲に確定する。同点は名前の辞書順。
// 分裂・合流があると対応の付かないコミュニティが残るが、それでよい
// (そこに属するテーブルは「所属が変わった」として出る)。
func matchCommunities(verts []string, assignA, assignB map[string]string) []CommunityMatch {
	overlap := map[CommunityMatch]int{}
	for _, t := range verts {
		overlap[CommunityMatch{A: assignA[t], B: assignB[t]}]++
	}
	cands := make([]CommunityMatch, 0, len(overlap))
	for k, n := range overlap {
		k.Overlap = n
		cands = append(cands, k)
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Overlap != cands[j].Overlap {
			return cands[i].Overlap > cands[j].Overlap
		}
		if cands[i].A != cands[j].A {
			return cands[i].A < cands[j].A
		}
		return cands[i].B < cands[j].B
	})
	usedA, usedB := map[string]bool{}, map[string]bool{}
	var out []CommunityMatch
	for _, c := range cands {
		if usedA[c.A] || usedB[c.B] {
			continue
		}
		usedA[c.A], usedB[c.B] = true, true
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].A < out[j].A })
	return out
}
