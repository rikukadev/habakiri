// partition.go: モノリスを「大物数個」へ割る分割案を Girvan–Newman で出す。
//
// 橋検出(Tarjan)の正統な一般化。橋 = 辺媒介中心性が極大の辺の特殊形なので、
// betweenness の高い辺から順に外し(Girvan–Newman)、重み付きモジュラリティが
// 最大になった時点のコミュニティを分割案とする。
//
// グラフの作り方(すべて決定的):
//   ノード = ユニット(ブロック/島)+ hub
//   辺     = 橋(重み = 橋の重み)
//          + 宣言外の疑い[強](重み 1)
//          + hub 契約(重み = 3 × FK本数 / hub次数 — 二部グラフ射影の定石。
//            store のような万能 hub が全体を糊付けするのを次数で抑える)
//
// 乱数・時刻・map 順への依存なし。同点は辞書順で解決する。
package main

import (
	"math"
	"sort"
)

// ServiceGroup は分割案のコミュニティ 1 つ。
type ServiceGroup struct {
	Name   string   `json:"name"`   // 最大ユニットの名前
	Tables int      `json:"tables"` // 実テーブル数合計(hub を除く)
	Units  []string `json:"units"`  // 所属ユニット(代表名)
	Hubs   []string `json:"hubs"`   // このコミュニティに落ちた hub(= 所有者)
}

// Partition は分割案全体。
type Partition struct {
	Groups     []ServiceGroup `json:"groups"`
	Modularity float64        `json:"modularity"`
}

type pgraph struct {
	nodes []string // sorted
	idx   map[string]int
	w     map[[2]int]float64 // i<j
}

func (g *pgraph) addEdge(a, b string, w float64) {
	i, j := g.idx[a], g.idx[b]
	if i == j {
		return
	}
	if i > j {
		i, j = j, i
	}
	g.w[[2]int{i, j}] += w
}

// edgeBetweenness: Brandes(重み付き・距離 = 1/重み)。決定的。
func edgeBetweenness(nodes int, adj [][]int, w map[[2]int]float64) map[[2]int]float64 {
	eb := map[[2]int]float64{}
	key := func(a, b int) [2]int {
		if a > b {
			a, b = b, a
		}
		return [2]int{a, b}
	}
	for s := 0; s < nodes; s++ {
		// Dijkstra(小さいグラフなので線形探索で十分・決定的)
		dist := make([]float64, nodes)
		sigma := make([]float64, nodes)
		done := make([]bool, nodes)
		preds := make([][]int, nodes)
		for i := range dist {
			dist[i] = math.Inf(1)
		}
		dist[s], sigma[s] = 0, 1
		var order []int
		for {
			u, best := -1, math.Inf(1)
			for i := 0; i < nodes; i++ {
				if !done[i] && dist[i] < best {
					u, best = i, dist[i]
				}
			}
			if u < 0 {
				break
			}
			done[u] = true
			order = append(order, u)
			for _, v := range adj[u] {
				d := dist[u] + 1/w[key(u, v)]
				const eps = 1e-12
				if d < dist[v]-eps {
					dist[v] = d
					sigma[v] = sigma[u]
					preds[v] = []int{u}
				} else if math.Abs(d-dist[v]) <= eps {
					sigma[v] += sigma[u]
					preds[v] = append(preds[v], u)
				}
			}
		}
		delta := make([]float64, nodes)
		for i := len(order) - 1; i >= 0; i-- {
			v := order[i]
			for _, u := range preds[v] {
				c := sigma[u] / sigma[v] * (1 + delta[v])
				eb[key(u, v)] += c
				delta[u] += c
			}
		}
	}
	return eb
}

// modularity: 現在の連結成分をコミュニティとみなした重み付き Q(元の全辺で評価)。
// 浮動小数の加算順が結果を揺らさないよう、辺はソート済みキーで回す
// (map 順で足すと Q がラストビットで揺れ、分割が実行ごとに変わりうる — テストで実測)。
func modularity(nodes int, keys [][2]int, orig map[[2]int]float64, comp []int) float64 {
	var m float64
	deg := make([]float64, nodes)
	for _, k := range keys {
		wt := orig[k]
		m += wt
		deg[k[0]] += wt
		deg[k[1]] += wt
	}
	if m == 0 {
		return 0
	}
	in := make([]float64, nodes)  // comp id < nodes
	tot := make([]float64, nodes)
	for _, k := range keys {
		if comp[k[0]] == comp[k[1]] {
			in[comp[k[0]]] += orig[k]
		}
	}
	for i := 0; i < nodes; i++ {
		tot[comp[i]] += deg[i]
	}
	q := 0.0
	for c := 0; c < nodes; c++ {
		q += in[c] / m
		q -= (tot[c] / (2 * m)) * (tot[c] / (2 * m))
	}
	return q
}

func components(nodes int, w map[[2]int]float64) []int {
	comp := make([]int, nodes)
	for i := range comp {
		comp[i] = -1
	}
	adj := make([][]int, nodes)
	for k := range w {
		adj[k[0]] = append(adj[k[0]], k[1])
		adj[k[1]] = append(adj[k[1]], k[0])
	}
	c := 0
	for i := 0; i < nodes; i++ {
		if comp[i] >= 0 {
			continue
		}
		stack := []int{i}
		comp[i] = c
		for len(stack) > 0 {
			u := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, v := range adj[u] {
				if comp[v] < 0 {
					comp[v] = c
					stack = append(stack, v)
				}
			}
		}
		c++
	}
	return comp
}

// BuildPartition は分割案を計算する。
func BuildPartition(a *Analysis) *Partition {
	// ユニット一覧(hub 契約とブロック・島から確定)
	unitTables := map[string]int{}
	tableCount := func(node string) int {
		if ms, ok := a.CascadeGroups[node]; ok {
			return len(ms)
		}
		return 1
	}
	unitName := map[string]string{} // 縮約ノード → ユニット代表
	for _, block := range a.Blocks {
		members := append([]string(nil), block...)
		sort.Strings(members)
		n := 0
		for _, m := range members {
			n += tableCount(m)
		}
		unitTables[members[0]] = n
		for _, m := range members {
			unitName[m] = members[0]
		}
	}
	for _, is := range a.Islands {
		unitTables[is.Name] = is.Tables
		unitName[is.Name] = is.Name
	}
	memberRep := map[string]string{}
	for root, ms := range a.CascadeGroups {
		for _, m := range ms {
			memberRep[m] = root
		}
	}
	resolveUnit := func(t string) string {
		rep := t
		if r, ok := memberRep[t]; ok {
			rep = r
		}
		return unitName[rep]
	}

	hubDeg := map[string]int{}
	var nodes []string
	for u := range unitTables {
		nodes = append(nodes, u)
	}
	for _, h := range a.Hubs {
		hubDeg[h.Node] = h.Degree
		nodes = append(nodes, h.Node)
	}
	sort.Strings(nodes)
	g := &pgraph{nodes: nodes, idx: map[string]int{}, w: map[[2]int]float64{}}
	for i, n := range nodes {
		g.idx[n] = i
	}

	// 辺: 橋
	for _, e := range a.Edges {
		if !e.Bridge {
			continue // ブロック内の辺はユニット内なので分割グラフには不要
		}
		ua, ub := unitName[e.A], unitName[e.B]
		if ua != "" && ub != "" && ua != ub {
			g.addEdge(ua, ub, e.Weight)
		}
	}
	// 辺: 疑い[強]
	seenS := map[[2]string]bool{}
	for _, s := range a.Suspects {
		if !s.Strong {
			continue
		}
		ua, ub := resolveUnit(s.FromTable), resolveUnit(s.ToTable)
		if ua == "" || ub == "" || ua == ub {
			continue
		}
		if ua > ub {
			ua, ub = ub, ua
		}
		if seenS[[2]string{ua, ub}] {
			continue
		}
		seenS[[2]string{ua, ub}] = true
		g.addEdge(ua, ub, 1)
	}
	// 辺: hub 契約(二部射影の重み: 3 × FK本数 / hub次数)
	for _, hc := range a.HubContracts {
		if hubDeg[hc.Hub] == 0 {
			continue
		}
		w := 3 * float64(hc.ToHub+hc.FromHub) / float64(hubDeg[hc.Hub])
		g.addEdge(hc.Unit, hc.Hub, w)
	}

	// Girvan–Newman: betweenness 最大の辺を外しながら Q 最大の分割を探す
	orig := map[[2]int]float64{}
	for k, v := range g.w {
		orig[k] = v
	}
	var origKeys [][2]int
	for k := range orig {
		origKeys = append(origKeys, k)
	}
	sort.Slice(origKeys, func(i, j int) bool {
		if origKeys[i][0] != origKeys[j][0] {
			return origKeys[i][0] < origKeys[j][0]
		}
		return origKeys[i][1] < origKeys[j][1]
	})
	cur := map[[2]int]float64{}
	for k, v := range g.w {
		cur[k] = v
	}
	n := len(nodes)
	bestComp := components(n, cur)
	bestQ := modularity(n, origKeys, orig, bestComp)
	for len(cur) > 0 {
		adj := make([][]int, n)
		for k := range cur {
			adj[k[0]] = append(adj[k[0]], k[1])
			adj[k[1]] = append(adj[k[1]], k[0])
		}
		for i := range adj {
			sort.Ints(adj[i])
		}
		eb := edgeBetweenness(n, adj, cur)
		// 最大 betweenness の辺(同点は辞書順)を除去
		var target [2]int
		best := -1.0
		var keys [][2]int
		for k := range cur {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i][0] != keys[j][0] {
				return keys[i][0] < keys[j][0]
			}
			return keys[i][1] < keys[j][1]
		})
		for _, k := range keys {
			if eb[k] > best+1e-9 {
				best, target = eb[k], k
			}
		}
		delete(cur, target)
		comp := components(n, cur)
		// 同点(±1e-9)は「より分離が進んだ側」を採用する。小さいグラフでは
		// 正しい分割が同点 Q でしか現れないことがある(テストで実測)。
		// 過分割に倒れても、後段の小コミュニティ吸着が回収する。
		if q := modularity(n, origKeys, orig, comp); q > bestQ-1e-9 {
			bestQ, bestComp = math.Max(q, bestQ), comp
		}
	}

	// 小コミュニティの吸着。モジュラリティ最大化は resolution limit のため
	// 弱結合の衛星(1〜2 テーブル)を独立させがちなので、規定サイズ未満の
	// コミュニティは「元グラフでの総結合重みが最大のコミュニティ」へ編入する。
	// 同点は相手コミュニティのテーブル数が大きい方 → 代表ノード辞書順。
	const smallGroupMax = 3 // このテーブル数未満は独立サービスにしない
	for {
		compTables := map[int]int{}
		for i, name := range nodes {
			if _, isHub := hubDeg[name]; !isHub {
				compTables[bestComp[i]] += unitTables[name]
			}
		}
		// 吸着対象: 最小の小コミュニティ(決定的に 1 個ずつ処理)
		small, smallT := -1, smallGroupMax
		var cids []int
		for c := range compTables {
			cids = append(cids, c)
		}
		sort.Ints(cids)
		for _, c := range cids {
			if compTables[c] < smallT {
				small, smallT = c, compTables[c]
			}
		}
		if small < 0 {
			break
		}
		// 元グラフでの総結合重みが最大の相手を探す
		gain := map[int]float64{}
		for _, k := range origKeys {
			wt := orig[k]
			ca, cb := bestComp[k[0]], bestComp[k[1]]
			if ca == small && cb != small {
				gain[cb] += wt
			} else if cb == small && ca != small {
				gain[ca] += wt
			}
		}
		target, bestW := -1, 0.0
		var gks []int
		for c := range gain {
			gks = append(gks, c)
		}
		sort.Ints(gks)
		for _, c := range gks {
			if gain[c] > bestW+1e-9 ||
				(math.Abs(gain[c]-bestW) <= 1e-9 && target >= 0 && compTables[c] > compTables[target]) {
				target, bestW = c, gain[c]
			}
		}
		if target < 0 {
			// どこにも繋がっていない孤立コミュニティ: 便宜的に最大コミュニティへ
			for _, c := range cids {
				if c != small && (target < 0 || compTables[c] > compTables[target]) {
					target = c
				}
			}
			if target < 0 {
				break
			}
		}
		for i := range bestComp {
			if bestComp[i] == small {
				bestComp[i] = target
			}
		}
	}

	// コミュニティ → ServiceGroup
	groups := map[int]*ServiceGroup{}
	for i, name := range nodes {
		c := bestComp[i]
		sg, ok := groups[c]
		if !ok {
			sg = &ServiceGroup{}
			groups[c] = sg
		}
		if _, isHub := hubDeg[name]; isHub {
			sg.Hubs = append(sg.Hubs, name)
		} else {
			sg.Units = append(sg.Units, name)
			sg.Tables += unitTables[name]
		}
	}
	part := &Partition{Modularity: bestQ}
	for _, sg := range groups {
		sort.Strings(sg.Hubs)
		// 名前 = 最大ユニット(同数なら辞書順)
		sort.Slice(sg.Units, func(i, j int) bool {
			if unitTables[sg.Units[i]] != unitTables[sg.Units[j]] {
				return unitTables[sg.Units[i]] > unitTables[sg.Units[j]]
			}
			return sg.Units[i] < sg.Units[j]
		})
		// hub を所有するグループは「<hub>圏」— 実態を表す名前になる
		// (最大ユニット名だと bulk_import_rows 圏 accounts のような誤解を生む)。
		if len(sg.Hubs) > 0 {
			sg.Name = sg.Hubs[0] + " 圏"
		} else if len(sg.Units) > 0 {
			sg.Name = sg.Units[0]
		}
		part.Groups = append(part.Groups, *sg)
	}
	sort.Slice(part.Groups, func(i, j int) bool {
		if part.Groups[i].Tables != part.Groups[j].Tables {
			return part.Groups[i].Tables > part.Groups[j].Tables
		}
		return part.Groups[i].Name < part.Groups[j].Name
	})
	return part
}
