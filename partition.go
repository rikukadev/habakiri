// partition.go: モノリスを「大物数個」へ割る分割案を Girvan–Newman で出す。
//
// 橋検出(Tarjan)の正統な一般化。橋 = 辺媒介中心性が極大の辺の特殊形なので、
// betweenness の高い辺から順に外し(Girvan–Newman)、重み付きモジュラリティが
// 最大になった時点のコミュニティを分割案とする。
//
// グラフの作り方(すべて決定的):
//
//	ノード = ユニット(ブロック/島)+ hub
//	辺     = 橋(重み = 橋の重み)
//	       + 宣言外の疑い[強](重み 1)
//	       + hub 契約(重み = 3 × FK本数 / hub次数 — 二部グラフ射影の定石。
//	         store のような万能 hub が全体を糊付けするのを次数で抑える)
//
// 乱数・時刻・map 順への依存なし。同点は辞書順で解決する。
package main

import (
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ServiceGroup は分割案のコミュニティ 1 つ。
type ServiceGroup struct {
	Name   string   `json:"name"`   // 最大ユニットの名前(hub 所有時は「<hub> 圏」)
	Tables int      `json:"tables"` // 実テーブル数合計(hub を除く)
	Units  []string `json:"units"`  // 所属ユニット(代表名)
	Hubs   []string `json:"hubs"`   // このコミュニティに落ちた hub(= 所有者)
	// Glue: このグループを内側で束ねている辺の本数の内訳。
	// 共起だけで束ねられたグループ(CoocOnly)は人間レビューの旗。
	Glue     map[string]int `json:"glue,omitempty"`
	CoocOnly bool           `json:"cooc_only,omitempty"`
}

// coocNPMIThreshold: 分割グラフに参加させる共起の NPMI 下限。
// 生カウントではなく正規化指標で足切りする(高頻度テーブルの偶発共起を落とす)。
const coocNPMIThreshold = 0.3

// Partition は分割案全体。Groups は選択中の段(既定 = Q 最大)。
// Levels は粒度の階段 — Girvan–Newman のデンドログラムを吸着後処理まで
// かけた各段で、粗い分割(2 個・3 個…)から細かい分割までを全部持つ。
type Partition struct {
	Groups     []ServiceGroup `json:"groups"`
	Modularity float64        `json:"modularity"`
	// MaxModularity: 階段全体での Q 最大。--services N で粗い段を選んだとき、
	// 差分(Max - 現在)が「組織が課した境界の制約コスト」の定量になる。
	MaxModularity float64          `json:"max_modularity"`
	Levels        []PartitionLevel `json:"levels"`
}

// PartitionLevel は粒度 1 段(グループ数 K とその内容)。
type PartitionLevel struct {
	K          int            `json:"k"`
	Modularity float64        `json:"modularity"`
	Groups     []ServiceGroup `json:"groups"`
}

// SelectLevel は「N 個くらいに割りたい」に最も近い段を選ぶ
// (|K-N| 最小、同点は Q が高い方 → K が小さい方)。
func (pt *Partition) SelectLevel(n int) {
	if len(pt.Levels) == 0 {
		return
	}
	best := -1
	for i, lv := range pt.Levels {
		if best < 0 {
			best = i
			continue
		}
		b := pt.Levels[best]
		di, db := abs(lv.K-n), abs(b.K-n)
		if di < db || (di == db && (lv.Modularity > b.Modularity+1e-9 ||
			(math.Abs(lv.Modularity-b.Modularity) <= 1e-9 && lv.K < b.K))) {
			best = i
		}
	}
	pt.Groups = pt.Levels[best].Groups
	pt.Modularity = pt.Levels[best].Modularity
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
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
	all := make([]int, nodes)
	for i := range all {
		all[i] = i
	}
	eb := map[[2]int]float64{}
	accumulateBetweenness(nodes, adj, w, all, eb)
	return eb
}

// accumulateBetweenness は sources(昇順)を始点とする寄与を eb に足す。
//
// Girvan–Newman で辺を外したとき、値が変わるのはその辺があった連結成分の
// 中の辺だけ。成分の外の始点からの寄与は 0 なので、成分の中の始点だけで
// 足し直せば、全体を計算し直したのと 1 ビットも違わない(足す順序も同じ
// 始点の昇順)。大規模スキーマ(1,000 テーブル超)で分割に数分かかっていた
// 原因(#65)。
//
// Dijkstra はヒープで回す。(距離, 番号) の順に取り出すので、線形探索
// (距離最小、同点は番号最小)と同じ順序になり、結果は変わらない。
func accumulateBetweenness(nodes int, adj [][]int, w map[[2]int]float64, sources []int, eb map[[2]int]float64) {
	key := func(a, b int) [2]int {
		if a > b {
			a, b = b, a
		}
		return [2]int{a, b}
	}
	// 内側のループで map を引かないよう、辺の長さ(1/重み)と辺の番号を
	// 隣接と並べて持つ
	length := make([][]float64, nodes)
	edgeID := make([][]int, nodes)
	var edges [][2]int
	idOf := map[[2]int]int{}
	for u := range adj {
		length[u] = make([]float64, len(adj[u]))
		edgeID[u] = make([]int, len(adj[u]))
		for k, v := range adj[u] {
			kk := key(u, v)
			length[u][k] = 1 / w[kk]
			id, ok := idOf[kk]
			if !ok {
				id = len(edges)
				idOf[kk] = id
				edges = append(edges, kk)
			}
			edgeID[u][k] = id
		}
	}

	// 始点ごとの寄与は互いに独立なので並列に求め、最後に始点の昇順で足し込む。
	// 1 つの始点が同じ辺に寄与するのは 1 回だけなので、足す順序は逐次計算と
	// 同じ(始点の昇順)になり、浮動小数の和も 1 ビットも変わらない。
	type contrib struct {
		edge int
		c    float64
	}
	results := make([][]contrib, len(sources))
	workers := runtime.GOMAXPROCS(0)
	if workers > len(sources) {
		workers = len(sources)
	}
	var wg sync.WaitGroup
	next := int64(-1)
	for wkr := 0; wkr < workers; wkr++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dist := make([]float64, nodes)
			sigma := make([]float64, nodes)
			done := make([]bool, nodes)
			delta := make([]float64, nodes)
			preds := make([][]int, nodes) // 先行ノード
			predE := make([][]int, nodes) // 先行ノードからの辺の番号
			order := make([]int, 0, nodes)
			var h distHeap
			for {
				si := int(atomic.AddInt64(&next, 1))
				if si >= len(sources) {
					return
				}
				s := sources[si]
				for i := range dist {
					dist[i] = math.Inf(1)
					sigma[i] = 0
					done[i] = false
					delta[i] = 0
					preds[i] = preds[i][:0]
					predE[i] = predE[i][:0]
				}
				order = order[:0]
				h = h[:0]
				dist[s], sigma[s] = 0, 1
				h.push(distItem{0, s})
				for len(h) > 0 {
					it := h.pop()
					u := it.v
					if done[u] || it.d != dist[u] {
						continue // 古い項目
					}
					done[u] = true
					order = append(order, u)
					for k, v := range adj[u] {
						d := dist[u] + length[u][k]
						const eps = 1e-12
						if d < dist[v]-eps {
							dist[v] = d
							sigma[v] = sigma[u]
							preds[v] = append(preds[v][:0], u)
							predE[v] = append(predE[v][:0], edgeID[u][k])
							h.push(distItem{d, v})
						} else if math.Abs(d-dist[v]) <= eps {
							sigma[v] += sigma[u]
							preds[v] = append(preds[v], u)
							predE[v] = append(predE[v], edgeID[u][k])
						}
					}
				}
				var out []contrib
				for i := len(order) - 1; i >= 0; i-- {
					v := order[i]
					for pi, u := range preds[v] {
						c := sigma[u] / sigma[v] * (1 + delta[v])
						out = append(out, contrib{predE[v][pi], c})
						delta[u] += c
					}
				}
				results[si] = out
			}
		}()
	}
	wg.Wait()
	for _, out := range results {
		for _, ct := range out {
			eb[edges[ct.edge]] += ct.c
		}
	}
}

// distHeap は (距離, 番号) の小さい順に取り出す二分ヒープ。
type distItem struct {
	d float64
	v int
}

type distHeap []distItem

func (h distItem) less(o distItem) bool { return h.d < o.d || (h.d == o.d && h.v < o.v) }

func (h *distHeap) push(it distItem) {
	*h = append(*h, it)
	a := *h
	for i := len(a) - 1; i > 0; {
		p := (i - 1) / 2
		if !a[i].less(a[p]) {
			break
		}
		a[i], a[p] = a[p], a[i]
		i = p
	}
}

func (h *distHeap) pop() distItem {
	a := *h
	top := a[0]
	last := len(a) - 1
	a[0] = a[last]
	a = a[:last]
	for i := 0; ; {
		l, r, m := 2*i+1, 2*i+2, i
		if l < len(a) && a[l].less(a[m]) {
			m = l
		}
		if r < len(a) && a[r].less(a[m]) {
			m = r
		}
		if m == i {
			break
		}
		a[i], a[m] = a[m], a[i]
		i = m
	}
	*h = a
	return top
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
	in := make([]float64, nodes) // comp id < nodes
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
// Girvan–Newman のデンドログラムから、成分数が増える各瞬間をスナップショットし、
// それぞれに小コミュニティ吸着をかけて「粒度の階段」(Levels)を作る。
// 既定の選択は吸着後モジュラリティ最大の段。
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

	edgeType := map[[2]int]map[string]bool{}
	mark := func(x, y string, kind string) {
		i, j := g.idx[x], g.idx[y]
		if i == j {
			return
		}
		if i > j {
			i, j = j, i
		}
		k := [2]int{i, j}
		if edgeType[k] == nil {
			edgeType[k] = map[string]bool{}
		}
		edgeType[k][kind] = true
	}

	// 辺: 橋
	for _, e := range a.Edges {
		if !e.Bridge {
			continue // ブロック内の辺はユニット内なので分割グラフには不要
		}
		ua, ub := unitName[e.A], unitName[e.B]
		if ua != "" && ub != "" && ua != ub {
			g.addEdge(ua, ub, e.Weight)
			mark(ua, ub, "FK")
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
		mark(ua, ub, "疑い")
	}
	// 辺: 実測共起(FK なし・共起 hub 非接続・NPMI ≥ 閾値のみ。
	// 重み = 2 × NPMI — NOT NULL 級を上限に、正規化指標で強さを測る)
	if !a.coocNoWeight {
		for _, c := range a.Cooc {
			if c.HasFK || c.Suppressed || c.NPMI < coocNPMIThreshold {
				continue
			}
			ua, ub := resolveUnit(c.A), resolveUnit(c.B)
			if ua == "" || ub == "" || ua == ub {
				continue
			}
			g.addEdge(ua, ub, 2*c.NPMI)
			mark(ua, ub, "共起")
		}
	}

	// 辺: hub 契約(二部射影の重み: 3 × FK本数 / hub次数)
	for _, hc := range a.HubContracts {
		if hubDeg[hc.Hub] == 0 {
			continue
		}
		w := 3 * float64(hc.ToHub+hc.FromHub) / float64(hubDeg[hc.Hub])
		g.addEdge(hc.Unit, hc.Hub, w)
		mark(hc.Unit, hc.Hub, "hub契約")
	}

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

	countComps := func(comp []int) int {
		seen := map[int]bool{}
		for _, c := range comp {
			seen[c] = true
		}
		return len(seen)
	}

	// GN: 成分数が増える瞬間ごとにスナップショット(= デンドログラムの段)
	var snapshots [][]int
	initComp := components(n, cur)
	snapshots = append(snapshots, initComp)
	prevCount := countComps(initComp)
	buildAdj := func() [][]int {
		adj := make([][]int, n)
		for k := range cur {
			adj[k[0]] = append(adj[k[0]], k[1])
			adj[k[1]] = append(adj[k[1]], k[0])
		}
		for i := range adj {
			sort.Ints(adj[i])
		}
		return adj
	}
	eb := edgeBetweenness(n, buildAdj(), cur)
	for len(cur) > 0 {
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
		delete(eb, target)
		comp := components(n, cur)
		if c := countComps(comp); c > prevCount {
			snapshots = append(snapshots, comp)
			prevCount = c
		}
		// 外した辺の両端がいる成分だけ計算し直す(ほかの成分の値は変わらない)
		affected := map[int]bool{comp[target[0]]: true, comp[target[1]]: true}
		var sources []int
		for v := 0; v < n; v++ {
			if affected[comp[v]] {
				sources = append(sources, v)
			}
		}
		for k := range cur {
			if affected[comp[k[0]]] {
				delete(eb, k)
			}
		}
		accumulateBetweenness(n, buildAdj(), cur, sources, eb)
	}

	// 各スナップショットに吸着をかけ、粒度の階段を作る(重複段は畳む)
	pt := &Partition{}
	seenSig := map[string]bool{}
	for _, snap := range snapshots {
		comp := absorbSmall(append([]int(nil), snap...), n, nodes, hubDeg, unitTables, origKeys, orig)
		groups := buildGroups(comp, nodes, hubDeg, unitTables)
		sig := ""
		for _, gr := range groups {
			sig += gr.Name + "|" + strings.Join(gr.Units, ",") + ";"
		}
		if seenSig[sig] {
			continue
		}
		seenSig[sig] = true
		pt.Levels = append(pt.Levels, PartitionLevel{
			K:          len(groups),
			Modularity: modularity(n, origKeys, orig, comp),
			Groups:     groups,
		})
	}
	// 同じ K の段は最高 Q のものだけ残す(選択肢としては同粒度の別解だが、
	// 階段の一覧性を優先する)。
	bestAtK := map[int]int{}
	for i, lv := range pt.Levels {
		if j, ok := bestAtK[lv.K]; !ok || lv.Modularity > pt.Levels[j].Modularity+1e-9 {
			bestAtK[lv.K] = i
		}
	}
	var kept []PartitionLevel
	for i, lv := range pt.Levels {
		if bestAtK[lv.K] == i {
			kept = append(kept, lv)
		}
	}
	pt.Levels = kept
	sort.SliceStable(pt.Levels, func(i, j int) bool { return pt.Levels[i].K < pt.Levels[j].K })

	// 既定 = 吸着後モジュラリティ最大の段(同点はより分離が進んだ段)
	best := 0
	for i, lv := range pt.Levels {
		if lv.Modularity > pt.Levels[best].Modularity+1e-9 ||
			(math.Abs(lv.Modularity-pt.Levels[best].Modularity) <= 1e-9 && lv.K > pt.Levels[best].K) {
			best = i
		}
	}
	if len(pt.Levels) > 0 {
		pt.Groups = pt.Levels[best].Groups
		pt.Modularity = pt.Levels[best].Modularity
		pt.MaxModularity = pt.Levels[best].Modularity
		for _, lv := range pt.Levels {
			if lv.Modularity > pt.MaxModularity {
				pt.MaxModularity = lv.Modularity
			}
		}
	}

	// 選択中グループの結束内訳(FK / 疑い / 共起 / hub契約)。
	// 共起だけで束ねられたグループは人間レビューの旗(CoocOnly)。
	fillGlue := func(groups []ServiceGroup) {
		gi := map[string]int{}
		for i, gr := range groups {
			for _, u := range gr.Units {
				gi[u] = i
			}
			for _, h := range gr.Hubs {
				gi[h] = i
			}
		}
		glue := make([]map[string]int, len(groups))
		for k, kinds := range edgeType {
			a, b := nodes[k[0]], nodes[k[1]]
			ga, okA := gi[a]
			gb, okB := gi[b]
			if !okA || !okB || ga != gb {
				continue
			}
			if glue[ga] == nil {
				glue[ga] = map[string]int{}
			}
			for kind := range kinds {
				glue[ga][kind]++
			}
		}
		for i := range groups {
			if glue[i] == nil {
				continue
			}
			groups[i].Glue = glue[i]
			if len(groups[i].Units) >= 2 && len(glue[i]) > 0 {
				only := true
				for kind := range glue[i] {
					if kind != "共起" {
						only = false
					}
				}
				groups[i].CoocOnly = only
			}
		}
	}
	fillGlue(pt.Groups)
	for i := range pt.Levels {
		fillGlue(pt.Levels[i].Groups)
	}
	return pt
}

// absorbSmall: 規定サイズ未満のコミュニティを最強結合先へ編入する(決定的)。
func absorbSmall(bestComp []int, n int, nodes []string, hubDeg map[string]int,
	unitTables map[string]int, origKeys [][2]int, orig map[[2]int]float64) []int {
	const smallGroupMax = 3 // このテーブル数未満は独立サービスにしない
	// 各ノードに接する辺の位置(origKeys の添字)。吸着先の集計で全辺を
	// 走査しないため。添字の昇順に足すので、和の順序は全辺を回すのと同じ。
	incident := make([][]int, n)
	for ei, k := range origKeys {
		incident[k[0]] = append(incident[k[0]], ei)
		incident[k[1]] = append(incident[k[1]], ei)
	}
	// コミュニティごとのテーブル数(hub 以外のノードを持つコミュニティだけ)と
	// メンバーを差分で持ち回る。吸着のたびに全ノードを数え直さないため。
	compTables := map[int]int{}
	members := map[int][]int{}
	for i, name := range nodes {
		members[bestComp[i]] = append(members[bestComp[i]], i)
		if _, isHub := hubDeg[name]; !isHub {
			compTables[bestComp[i]] += unitTables[name]
		}
	}
	smallSet := map[int]bool{} // テーブル数が規定未満のコミュニティ
	for c, t := range compTables {
		if t < smallGroupMax {
			smallSet[c] = true
		}
	}
	for len(smallSet) > 0 {
		// 最小のテーブル数、同点は番号の小さい方(全体を番号順に見るのと同じ選び方)
		small, smallT := -1, smallGroupMax
		for c := range smallSet {
			if t := compTables[c]; t < smallT || (t == smallT && c < small) {
				small, smallT = c, t
			}
		}
		var touching []int
		seenEdge := map[int]bool{}
		for _, v := range members[small] {
			for _, ei := range incident[v] {
				if !seenEdge[ei] {
					seenEdge[ei] = true
					touching = append(touching, ei)
				}
			}
		}
		sort.Ints(touching)
		gain := map[int]float64{}
		for _, ei := range touching {
			k := origKeys[ei]
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
			// どこにも繋がっていない: 最大のコミュニティへ(番号順に見て最初の最大)
			var cids []int
			for c := range compTables {
				cids = append(cids, c)
			}
			sort.Ints(cids)
			for _, c := range cids {
				if c != small && (target < 0 || compTables[c] > compTables[target]) {
					target = c
				}
			}
			if target < 0 {
				break
			}
		}
		for _, v := range members[small] {
			bestComp[v] = target
		}
		members[target] = append(members[target], members[small]...)
		delete(members, small)
		if t, ok := compTables[small]; ok {
			compTables[target] += t
			delete(compTables, small)
		}
		delete(smallSet, small)
		if compTables[target] < smallGroupMax {
			smallSet[target] = true
		} else {
			delete(smallSet, target)
		}
	}
	return bestComp
}

// buildGroups: コミュニティ割当 → ServiceGroup 一覧(決定的な順序)。
func buildGroups(comp []int, nodes []string, hubDeg map[string]int, unitTables map[string]int) []ServiceGroup {
	groups := map[int]*ServiceGroup{}
	for i, name := range nodes {
		c := comp[i]
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
	var out []ServiceGroup
	for _, sg := range groups {
		sort.Strings(sg.Hubs)
		sort.Slice(sg.Units, func(i, j int) bool {
			if unitTables[sg.Units[i]] != unitTables[sg.Units[j]] {
				return unitTables[sg.Units[i]] > unitTables[sg.Units[j]]
			}
			return sg.Units[i] < sg.Units[j]
		})
		// hub を所有するグループは「<hub>圏」— 実態を表す名前になる。
		if len(sg.Hubs) > 0 {
			sg.Name = sg.Hubs[0] + " 圏"
		} else if len(sg.Units) > 0 {
			sg.Name = sg.Units[0]
		}
		out = append(out, *sg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tables != out[j].Tables {
			return out[i].Tables > out[j].Tables
		}
		return out[i].Name < out[j].Name
	})
	return out
}
