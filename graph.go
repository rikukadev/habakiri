// graph.go: FK グラフの解析(純関数)。DB に触らない。
//
// パイプライン(順序が本質):
//  1. 束ね     — 同じテーブル対の複数 FK を 1 エッジに(平行エッジは橋の定義を壊す)
//  2. 縮約     — ON DELETE CASCADE はライフサイクル共有 = 同一集約確定。ノードごと潰す
//  3. hub 除外 — users 等の高次数ノードを外し shared kernel として別枠に。
//     残したままだと全体が 1 塊に溶けて橋がほぼ出ない
//  4. 橋検出   — Tarjan(DFS 1 回、O(V+E))。位相は無向で判定する
//  5. ブロック — 橋を除いた連結成分 = 2-辺連結成分。橋ブロック木が切り出しマップ
package main

import (
	"sort"
)

// Pair は無向エッジの端点(A < B に正規化)。
type Pair struct {
	A, B string
}

func mkPair(a, b string) Pair {
	if a > b {
		a, b = b, a
	}
	return Pair{A: a, B: b}
}

// Edge は束ねた後の 1 エッジ。
type Edge struct {
	Pair
	FKs        []FK    // このテーブル対に張られた FK(向きは FK.ChildTable が持つ)
	Weight     float64 // CASCADE 3 / NOT NULL 2 / NULLABLE 1 の合算
	HasCascade bool
}

// fkWeight は FK 1 本の結合強度。静的な本数勘定より属性の階層が効く:
// CASCADE(ライフサイクル共有)> NOT NULL(存在依存)> NULLABLE(弱い参照)。
func fkWeight(fk FK) float64 {
	switch {
	case fk.DeleteRule == "CASCADE":
		return 3
	case fk.AllNotNull:
		return 2
	default:
		return 1
	}
}

// BuildEdges は FK をテーブル対ごとに束ねる。自己参照(木構造・隣接リスト)は
// 分割の判断材料にならないので落とす。
func BuildEdges(fks []FK) map[Pair]*Edge {
	edges := map[Pair]*Edge{}
	for _, fk := range fks {
		if fk.ChildTable == fk.ParentTable {
			continue
		}
		p := mkPair(fk.ChildTable, fk.ParentTable)
		e, ok := edges[p]
		if !ok {
			e = &Edge{Pair: p}
			edges[p] = e
		}
		e.FKs = append(e.FKs, fk)
		e.Weight += fkWeight(fk)
		if fk.DeleteRule == "CASCADE" {
			e.HasCascade = true
		}
	}
	return edges
}

// --- union-find(縮約用) ---

type dsu struct{ parent map[string]string }

func newDSU() *dsu { return &dsu{parent: map[string]string{}} }

func (d *dsu) find(x string) string {
	p, ok := d.parent[x]
	if !ok || p == x {
		d.parent[x] = x
		return x
	}
	r := d.find(p)
	d.parent[x] = r
	return r
}

func (d *dsu) union(a, b string) {
	ra, rb := d.find(a), d.find(b)
	if ra == rb {
		return
	}
	// 代表は辞書順で安定させる(出力の決定性のため)
	if ra > rb {
		ra, rb = rb, ra
	}
	d.parent[rb] = ra
}

// Contract は CASCADE エッジで結ばれたノード群を 1 ノードに潰す。
// 返り値: 縮約後のエッジ集合(自己ループ化したものは消える)、
// 代表 → 構成テーブル(2 個以上のグループのみ)。
func Contract(edges map[Pair]*Edge) (map[Pair]*Edge, map[string][]string) {
	d := newDSU()
	for p, e := range edges {
		d.find(p.A)
		d.find(p.B)
		if e.HasCascade {
			d.union(p.A, p.B)
		}
	}

	groups := map[string][]string{}
	for n := range d.parent {
		r := d.find(n)
		groups[r] = append(groups[r], n)
	}
	multi := map[string][]string{}
	for r, ms := range groups {
		if len(ms) > 1 {
			sort.Strings(ms)
			multi[r] = ms
		}
	}

	out := map[Pair]*Edge{}
	for p, e := range edges {
		ra, rb := d.find(p.A), d.find(p.B)
		if ra == rb {
			continue // 縮約で内側に入った
		}
		np := mkPair(ra, rb)
		ne, ok := out[np]
		if !ok {
			ne = &Edge{Pair: np}
			out[np] = ne
		}
		ne.FKs = append(ne.FKs, e.FKs...)
		ne.Weight += e.Weight
		ne.HasCascade = ne.HasCascade || e.HasCascade
	}
	return out, multi
}

// AutoHubThreshold は hub 判定の既定閾値。小さいスキーマで誤爆しないよう下限 6、
// 大きいスキーマでは全ノード数の 15%。根拠は経験則なので --hub で上書きできる。
func AutoHubThreshold(nodeCount int) int {
	t := nodeCount * 15 / 100
	if t < 6 {
		t = 6
	}
	return t
}

// Hub は除外されたノードとその次数。
type Hub struct {
	Node   string `json:"node"`
	Degree int    `json:"degree"`
}

// RemoveHubs は次数 >= threshold のノードを外す。除いた結果また閾値を超える
// ノードが生まれることは(次数は単調減少なので)ない。1 パスでよい。
func RemoveHubs(edges map[Pair]*Edge, threshold int) (map[Pair]*Edge, []Hub) {
	deg := map[string]int{}
	for p := range edges {
		deg[p.A]++
		deg[p.B]++
	}
	hubSet := map[string]bool{}
	var hubs []Hub
	for n, dg := range deg {
		if dg >= threshold {
			hubSet[n] = true
			hubs = append(hubs, Hub{Node: n, Degree: dg})
		}
	}
	sort.Slice(hubs, func(i, j int) bool {
		if hubs[i].Degree != hubs[j].Degree {
			return hubs[i].Degree > hubs[j].Degree
		}
		return hubs[i].Node < hubs[j].Node
	})
	out := map[Pair]*Edge{}
	for p, e := range edges {
		if hubSet[p.A] || hubSet[p.B] {
			continue
		}
		out[p] = e
	}
	return out, hubs
}

// Bridges は無向グラフの橋を返す(Tarjan)。平行エッジは BuildEdges/Contract で
// 既に 1 本化されている前提だが、実装は「親へ戻る同じエッジだけを飛ばす」
// (エッジ id で判定)ので、仮に平行エッジが混ざっても正しい。
func Bridges(edges map[Pair]*Edge) []Pair {
	type half struct {
		to  string
		eid int
	}
	list := make([]Pair, 0, len(edges))
	for p := range edges {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].A != list[j].A {
			return list[i].A < list[j].A
		}
		return list[i].B < list[j].B
	})
	adj := map[string][]half{}
	for i, p := range list {
		adj[p.A] = append(adj[p.A], half{p.B, i})
		adj[p.B] = append(adj[p.B], half{p.A, i})
	}

	disc := map[string]int{}
	low := map[string]int{}
	timer := 0
	var bridges []Pair

	var dfs func(u string, parentEID int)
	dfs = func(u string, parentEID int) {
		timer++
		disc[u] = timer
		low[u] = timer
		for _, h := range adj[u] {
			if h.eid == parentEID {
				continue
			}
			if disc[h.to] == 0 {
				dfs(h.to, h.eid)
				if low[h.to] < low[u] {
					low[u] = low[h.to]
				}
				if low[h.to] > disc[u] {
					bridges = append(bridges, list[h.eid])
				}
			} else if disc[h.to] < low[u] {
				low[u] = disc[h.to]
			}
		}
	}
	// ノード列挙も決定的に
	var nodes []string
	for n := range adj {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		if disc[n] == 0 {
			dfs(n, -1)
		}
	}
	sort.Slice(bridges, func(i, j int) bool {
		if bridges[i].A != bridges[j].A {
			return bridges[i].A < bridges[j].A
		}
		return bridges[i].B < bridges[j].B
	})
	return bridges
}

// Blocks は橋を取り除いた後の連結成分(= 2-辺連結成分)。
// これが「自然な塊」で、橋ブロック木の頂点になる。
func Blocks(edges map[Pair]*Edge, bridges []Pair) [][]string {
	bridgeSet := map[Pair]bool{}
	for _, b := range bridges {
		bridgeSet[b] = true
	}
	d := newDSU()
	for p := range edges {
		d.find(p.A)
		d.find(p.B)
		if !bridgeSet[p] {
			d.union(p.A, p.B)
		}
	}
	groups := map[string][]string{}
	for n := range d.parent {
		r := d.find(n)
		groups[r] = append(groups[r], n)
	}
	var out [][]string
	for _, ms := range groups {
		sort.Strings(ms)
		out = append(out, ms)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i][0] < out[j][0]
	})
	return out
}

// BlockOf はノード → 所属ブロックの引き当て表を作る。
func BlockOf(blocks [][]string) map[string]int {
	m := map[string]int{}
	for i, b := range blocks {
		for _, n := range b {
			m[n] = i
		}
	}
	return m
}

// ThinnestSeams は橋が無い(または少ない)ときのフォールバック。重みが最小の
// エッジを「最薄の継ぎ目」として返す。橋ではないので切っても分離はしないが、
// カット候補の探索起点になる。
func ThinnestSeams(edges map[Pair]*Edge, bridges []Pair, n int) []*Edge {
	bridgeSet := map[Pair]bool{}
	for _, b := range bridges {
		bridgeSet[b] = true
	}
	var rest []*Edge
	for p, e := range edges {
		if !bridgeSet[p] {
			rest = append(rest, e)
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].Weight != rest[j].Weight {
			return rest[i].Weight < rest[j].Weight
		}
		if rest[i].A != rest[j].A {
			return rest[i].A < rest[j].A
		}
		return rest[i].B < rest[j].B
	})
	if len(rest) > n {
		rest = rest[:n]
	}
	return rest
}
