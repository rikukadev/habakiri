// report_svg.go: 解析結果を SVG の図として機械生成する。
//
// レイアウトは決定的(乱数・時刻を使わない)なので、同じ入力からは
// バイト単位で同じ図が出る。force シミュレーションではなく、橋ブロック木の
// 構造をそのまま使う放射配置:
//
//	最大ブロックを中心に置き、橋で繋がるブロックを BFS 層ごとに
//	同心円へ並べる。ブロック内部は円周配置。
//
// 汎用グラフ描画より貧しいが、habakiri の出力は「ブロック + 橋」という
// 木に近い構造なので、これで十分読める(Magento / Mastodon 実測)。
package main

import (
	"fmt"
	"html"
	"io"
	"math"
	"sort"
)

type svgNode struct {
	name   string
	tables int // 縮約で含む実テーブル数
	x, y   float64
	r      float64
	block  int
	// ラベル位置。ブロック内メンバーは円の外側へ放射状に逃がす
	// (内側に置くと相互に重なる — Mastodon 実測)。
	lx, ly float64
	anchor string // start / middle / end
}

// svgPalette: ブロックの彩色。テーマ非依存の中間トーンで、白地でも黒地でも読める。
var svgPalette = []string{
	"#6b7fd7", "#5aa17f", "#c98a4b", "#b56576", "#7a6fbe",
	"#4c9fb8", "#a3874c", "#8a7f8d", "#5f8d4e", "#bd6b73",
}

// layoutSVG はノード座標を計算する。
func layoutSVG(a *Analysis) map[string]*svgNode {
	tableCount := func(node string) int {
		if ms, ok := a.CascadeGroups[node]; ok {
			return len(ms)
		}
		return 1
	}

	nodes := map[string]*svgNode{}
	for bi, block := range a.Blocks {
		for _, n := range block {
			nodes[n] = &svgNode{name: n, tables: tableCount(n), block: bi}
		}
	}
	for _, n := range nodes {
		n.r = 7 + 4*math.Sqrt(float64(n.tables))
	}

	// ブロックの内部半径(メンバーを円周に並べるのに要る大きさ)
	blockR := make([]float64, len(a.Blocks))
	for bi, block := range a.Blocks {
		maxNodeR, sum := 0.0, 0.0
		for _, m := range block {
			r := nodes[m].r
			sum += 2*r + 18
			if r > maxNodeR {
				maxNodeR = r
			}
		}
		if len(block) == 1 {
			blockR[bi] = maxNodeR
			continue
		}
		// 円周にメンバーが収まる半径(ラベル幅ぶんの弧も確保する)
		labelArc := 0.0
		for range block {
			labelArc += 84
		}
		if labelArc > sum {
			sum = labelArc
		}
		blockR[bi] = math.Max(sum/(2*math.Pi)+maxNodeR, maxNodeR*2.2)
	}

	// ブロック間の隣接(橋)
	blockOf := BlockOf(a.Blocks)
	adj := make(map[int][]int)
	for _, b := range a.Bridges {
		x, y := blockOf[b.A], blockOf[b.B]
		adj[x] = append(adj[x], y)
		adj[y] = append(adj[y], x)
	}

	// 放射ツリーレイアウト。橋ブロック構造はほぼ木なので、
	// 子ブロックを親の角度セクター内に置けば橋の交差がほぼ消える。
	// セクター幅は部分木が必要とする弧長に比例して割る。
	order := make([]int, len(a.Blocks))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		ti := 0
		for _, m := range a.Blocks[order[i]] {
			ti += nodes[m].tables
		}
		tj := 0
		for _, m := range a.Blocks[order[j]] {
			tj += nodes[m].tables
		}
		if ti != tj {
			return ti > tj
		}
		return order[i] < order[j]
	})

	const gap = 130.0
	parent := make([]int, len(a.Blocks))
	depth := make([]int, len(a.Blocks))
	children := make(map[int][]int)
	for i := range parent {
		parent[i], depth[i] = -2, -1 // -2 = 未訪問
	}
	var roots []int
	for _, root := range order {
		if parent[root] != -2 {
			continue
		}
		parent[root], depth[root] = -1, 0
		roots = append(roots, root)
		queue := []int{root}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			next := append([]int(nil), adj[cur]...)
			sort.Ints(next)
			for _, nb := range next {
				if parent[nb] != -2 {
					continue
				}
				parent[nb], depth[nb] = cur, depth[cur]+1
				children[cur] = append(children[cur], nb)
				queue = append(queue, nb)
			}
		}
	}

	// 部分木の必要弧長(自分の直径+余白 と 子の合計 の大きい方)
	var need func(bi int) float64
	needMemo := map[int]float64{}
	need = func(bi int) float64 {
		if v, ok := needMemo[bi]; ok {
			return v
		}
		own := math.Max(2*blockR[bi]+100, 190) // ラベル幅ぶんの弧は最低限確保する
		sum := 0.0
		for _, c := range children[bi] {
			sum += need(c)
		}
		v := math.Max(own, sum)
		needMemo[bi] = v
		return v
	}

	// 深さごとのリング半径(コンポーネント単位で計算)
	blockCenter := make([][2]float64, len(a.Blocks))
	var placeTree func(bi int, radius []float64, a0, a1 float64)
	placeTree = func(bi int, radius []float64, a0, a1 float64) {
		th := (a0 + a1) / 2
		if depth[bi] == 0 {
			blockCenter[bi] = [2]float64{0, 0}
		} else {
			blockCenter[bi] = [2]float64{radius[depth[bi]] * math.Cos(th), radius[depth[bi]] * math.Sin(th)}
		}
		total := 0.0
		for _, c := range children[bi] {
			total += need(c)
		}
		if total == 0 {
			return
		}
		// ルート直下は全周を使う。それ以外は親のセクター内で分配。
		lo, hi := a0, a1
		if depth[bi] == 0 {
			lo, hi = -math.Pi/2, 3*math.Pi/2
		}
		cur := lo
		for _, c := range children[bi] {
			span := (hi - lo) * need(c) / total
			placeTree(c, radius, cur, cur+span)
			cur += span
		}
	}

	for ci, root := range roots {
		// 深さ最大値とリング半径
		maxDepth := 0
		var walk func(bi int)
		var comp []int
		walk = func(bi int) {
			comp = append(comp, bi)
			if depth[bi] > maxDepth {
				maxDepth = depth[bi]
			}
			for _, c := range children[bi] {
				walk(c)
			}
		}
		walk(root)

		maxRAt := make([]float64, maxDepth+1)
		for _, bi := range comp {
			if blockR[bi] > maxRAt[depth[bi]] {
				maxRAt[depth[bi]] = blockR[bi]
			}
		}
		radius := make([]float64, maxDepth+1)
		for d := 1; d <= maxDepth; d++ {
			radius[d] = radius[d-1] + maxRAt[d-1] + maxRAt[d] + gap
			// 弧長がリングに収まるだけの半径も確保する
			needAt := 0.0
			for _, bi := range comp {
				if depth[bi] == d {
					needAt += 2*blockR[bi] + 100
				}
			}
			if byArc := needAt / (2 * math.Pi); byArc > radius[d] {
				radius[d] = byArc
			}
		}
		placeTree(root, radius, -math.Pi/2, 3*math.Pi/2)

		// 2 つ目以降のコンポーネントは右へずらして重ねない
		if ci > 0 {
			extent := blockR[root]
			if maxDepth > 0 {
				extent = radius[maxDepth] + maxRAt[maxDepth]
			}
			// 直前までの最大 X を求めてオフセット
			maxX := 0.0
			for bj := range blockCenter {
				if parent[bj] == -2 {
					continue
				}
				if isInPlacedComponent(bj, roots[:ci], parent) {
					if x := blockCenter[bj][0] + blockR[bj]; x > maxX {
						maxX = x
					}
				}
			}
			off := maxX + extent + 220
			for _, bi := range comp {
				blockCenter[bi][0] += off
			}
		}
	}

	// ブロック内部: メンバーを円周に(1 個なら中心に)。ラベルは外周の放射方向へ。
	for bi, block := range a.Blocks {
		cx, cy := blockCenter[bi][0], blockCenter[bi][1]
		if len(block) == 1 {
			n := nodes[block[0]]
			n.x, n.y = cx, cy
			n.lx, n.ly, n.anchor = cx, cy+n.r+13, "middle"
			continue
		}
		members := append([]string(nil), block...)
		sort.Strings(members)
		for i, m := range members {
			th := 2*math.Pi*float64(i)/float64(len(members)) - math.Pi/2
			n := nodes[m]
			n.x = cx + (blockR[bi]-n.r-6)*math.Cos(th)
			n.y = cy + (blockR[bi]-n.r-6)*math.Sin(th)
			lr := blockR[bi] + 16
			n.lx = cx + lr*math.Cos(th)
			n.ly = cy + lr*math.Sin(th) + 4
			switch {
			case math.Cos(th) > 0.35:
				n.anchor = "start"
			case math.Cos(th) < -0.35:
				n.anchor = "end"
			default:
				n.anchor = "middle"
			}
		}
	}

	return nodes
}

// WriteSVG は図を書き出す。
func WriteSVG(w io.Writer, a *Analysis) {
	nodes := layoutSVG(a)

	// 描画範囲
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	names := make([]string, 0, len(nodes))
	for name, n := range nodes {
		names = append(names, name)
		minX = math.Min(minX, n.x-n.r-70)
		maxX = math.Max(maxX, n.x+n.r+70)
		minY = math.Min(minY, n.y-n.r-40)
		maxY = math.Max(maxY, n.y+n.r+50)
	}
	sort.Strings(names)
	if len(names) == 0 {
		minX, minY, maxX, maxY = 0, 0, 400, 200
	}
	// ヘッダ = タイトル + hub 一覧 + 凡例(左に縦積み — 右上固定だと狭い図で
	// タイトルに重なる)。フッタ = 島グリッド + 孤立(2 行まで)。
	headerH := 46.0 + 18*float64(len(a.Hubs)) + 20 + 17*6 + 16
	islandCols := 5
	const islandCell = 200.0
	islandRows := int(math.Ceil(float64(len(a.Islands)) / float64(islandCols)))
	islandH := 0.0
	if len(a.Islands) > 0 {
		islandH = 50 + float64(islandRows)*74
	}
	isoLines := math.Min(2, math.Ceil(float64(len(a.Isolated))/6))
	footerH := islandH + 80 + 16*isoLines
	// 島グリッドと凡例が入る最低幅を確保する
	if w := 5*islandCell + 60; maxX-minX < w && (len(a.Islands) > 0 || len(names) < 6) {
		mid := (minX + maxX) / 2
		minX, maxX = mid-w/2, mid+w/2
	}
	minY -= headerH
	maxY += footerH

	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }
	esc := html.EscapeString

	p(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="%.0f %.0f %.0f %.0f" font-family="ui-monospace,SF Mono,Menlo,monospace">`,
		minX, minY, maxX-minX, maxY-minY)
	p(`<style>text{fill:#1d1c1a}@media(prefers-color-scheme:dark){text{fill:#e8e6e1}.hull{stroke:#555!important}}</style>`)

	// ヘッダ(統計と hub)
	p(`<text x="%.0f" y="%.0f" font-size="16" font-weight="bold">%s — %d テーブル / %d FK / 橋 %d 本</text>`,
		minX+20, minY+28, esc(a.Schema), a.TableCount, a.FKCount, len(a.Bridges))
	for i, h := range a.Hubs {
		p(`<text x="%.0f" y="%.0f" font-size="11" opacity=".75">hub(除外): %s 次数 %d</text>`,
			minX+20, minY+52+float64(i)*18, esc(h.Node), h.Degree)
	}

	// 凡例(左・hub の下に縦積み): 線種 = 種類、色 = 重み
	lx, ly := minX+20, minY+52+18*float64(len(a.Hubs))+20
	p(`<g font-size="10">`)
	p(`<text x="%.0f" y="%.0f" font-weight="bold" font-size="11">凡例 — 線種=種類 / 色=重み</text>`, lx, ly)
	legend := []struct {
		color, dash string
		width       float64
		label       string
	}{
		{"#a8a29a", "", 1.4, "FK w1(NULL可)"},
		{"#4c7fb8", "", 1.4, "FK w2(NOT NULL)"},
		{"#a83232", "", 1.4, "FK w3(CASCADE 級)"},
		{"#4c7fb8", "", 2.0, "橋 = ✂ 付き太線(色は重み)"},
		{"#7a5fae", "2 5", 1.8, "宣言外の疑い[強](callback)"},
		{"#c4b5e0", "2 5", 1.2, "宣言外の疑い[弱](メソッド)"},
	}
	for i, l := range legend {
		y := ly + 18 + float64(i)*17
		dash := ""
		if l.dash != "" {
			dash = fmt.Sprintf(` stroke-dasharray="%s"`, l.dash)
		}
		p(`<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="%s" stroke-width="%.1f"%s/>`,
			lx, y-3, lx+34, y-3, l.color, l.width, dash)
		p(`<text x="%.0f" y="%.1f">%s</text>`, lx+42, y, esc(l.label))
	}
	p(`</g>`)

	// ブロックの外郭(2 ノード以上のみ)
	for _, block := range a.Blocks {
		if len(block) < 2 {
			continue
		}
		cx, cy, maxD := 0.0, 0.0, 0.0
		for _, m := range block {
			cx += nodes[m].x
			cy += nodes[m].y
		}
		cx /= float64(len(block))
		cy /= float64(len(block))
		for _, m := range block {
			d := math.Hypot(nodes[m].x-cx, nodes[m].y-cy) + nodes[m].r
			if d > maxD {
				maxD = d
			}
		}
		p(`<circle class="hull" cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="#999" stroke-width="1" stroke-dasharray="3 4" opacity=".55"/>`,
			cx, cy, maxD+10)
	}

	// エッジ。線種 = つながりの種類(実線 = FK / 点線 = 宣言外の疑い)、
	// 色 = 重み(グレー 1: NULL可 / 青 2: NOT NULL / 朱 3: CASCADE 級)。
	weightColor := func(maxW float64) string {
		switch {
		case maxW >= 3:
			return "#a83232"
		case maxW >= 2:
			return "#4c7fb8"
		default:
			return "#a8a29a"
		}
	}

	// 宣言外の疑い(点線・紫)。縮約でノード名が変わるので member → 代表を引く。
	rep := map[string]string{}
	for root, ms := range a.CascadeGroups {
		for _, m := range ms {
			rep[m] = root
		}
	}
	resolve := func(t string) *svgNode {
		if n := nodes[t]; n != nil {
			return n
		}
		return nodes[rep[t]]
	}
	fkPair := map[string]bool{}
	for _, e := range a.Edges {
		fkPair[e.A+"\x00"+e.B] = true
	}
	suspectSeen := map[string]bool{}
	for _, s := range a.Suspects {
		na, nb := resolve(s.FromTable), resolve(s.ToTable)
		if na == nil || nb == nil || na.name == nb.name {
			continue // hub 行き・孤立・同一集約内はグラフ上に描けない/意味がない
		}
		x, y := na.name, nb.name
		if x > y {
			x, y = y, x
		}
		if fkPair[x+"\x00"+y] || suspectSeen[x+"\x00"+y] {
			continue // FK が既にある対には重ねない・重複疑いは 1 本に
		}
		suspectSeen[x+"\x00"+y] = true
		color, width, label := "#c4b5e0", 1.2, "弱"
		if s.Strong {
			color, width, label = "#7a5fae", 1.8, "強"
		}
		p(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="%.1f" stroke-dasharray="2 5" stroke-linecap="round"><title>宣言外の結合の疑い[%s] %s ↔ %s(callback/メソッド言及)</title></line>`,
			na.x, na.y, nb.x, nb.y, color, width, label, esc(s.FromTable), esc(s.ToTable))
	}

	// FK エッジ(実線)。橋は太い半透明の下敷き + ✂ ラベルで強調。
	for _, e := range a.Edges {
		na, nb := nodes[e.A], nodes[e.B]
		if na == nil || nb == nil {
			continue
		}
		color := weightColor(e.MaxWeight)
		if e.Bridge {
			p(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="7" opacity=".18"/>`,
				na.x, na.y, nb.x, nb.y, color)
			p(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="2"><title>橋 %s × %s(重み合計 %.0f / FK %d 本)</title></line>`,
				na.x, na.y, nb.x, nb.y, color, esc(e.A), esc(e.B), e.Weight, e.FKCount)
			p(`<text x="%.1f" y="%.1f" font-size="10" fill="%s" text-anchor="middle">✂ w=%.0f</text>`,
				(na.x+nb.x)/2, (na.y+nb.y)/2-5, color, e.Weight)
			continue
		}
		p(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.4" opacity=".65"><title>%s — %s(FK %d 本 / 重み合計 %.0f)</title></line>`,
			na.x, na.y, nb.x, nb.y, color, esc(e.A), esc(e.B), e.FKCount, e.Weight)
	}

	// ノード
	for _, name := range names {
		n := nodes[name]
		color := svgPalette[n.block%len(svgPalette)]
		label := name
		if n.tables > 1 {
			label = fmt.Sprintf("%s (+%d)", name, n.tables-1)
		}
		p(`<g><circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s" fill-opacity=".85" stroke="%s"/><title>%s</title></g>`,
			n.x, n.y, n.r, color, color, esc(svgTooltip(a, name)))
		p(`<text x="%.1f" y="%.1f" font-size="10" text-anchor="%s">%s</text>`,
			n.lx, n.ly, n.anchor, esc(label))
	}

	// hub 経由のみの島(グリッド帯)。DB スキャンでは sales 系のような大物が
	// ここに出る — 「hub との契約を整理すれば独立できる」塊。
	y := maxY - footerH
	if len(a.Islands) > 0 {
		y += 34
		p(`<text x="%.0f" y="%.1f" font-size="12" font-weight="bold" opacity=".8">hub 経由のみで繋がる島(%d)— hub との参照を値化すれば独立できる:</text>`,
			minX+20, y, len(a.Islands))
		for i, is := range a.Islands {
			col, row := i%islandCols, i/islandCols
			cx := minX + 110 + float64(col)*islandCell
			cy := y + 40 + float64(row)*74
			r := 7 + 4*math.Sqrt(float64(is.Tables))
			name := is.Name
			if len(name) > 24 { // 全名は hover の title にある
				name = name[:23] + "…"
			}
			label := name
			if is.Tables > 1 {
				label = fmt.Sprintf("%s (+%d)", name, is.Tables-1)
			}
			p(`<g><circle cx="%.1f" cy="%.1f" r="%.1f" fill="#8f8a94" fill-opacity=".6" stroke="#8f8a94"/><title>%s</title></g>`,
				cx, cy, r, esc(svgTooltip(a, is.Name)))
			p(`<text x="%.1f" y="%.1f" font-size="9.5" text-anchor="middle">%s</text>`,
				cx, cy+r+11, esc(label))
		}
		y += 40 + float64(islandRows)*74 - 34
	}

	// 孤立テーブル(2 行まで。全量は HTML / JSON にある)
	if len(a.Isolated) > 0 {
		y += 30
		p(`<text x="%.0f" y="%.1f" font-size="12" font-weight="bold" opacity=".8">孤立 %d(FK なし — 今日でも動かせる):</text>`,
			minX+20, y, len(a.Isolated))
		shown := 0
		for i := 0; i < len(a.Isolated) && i < 12; i += 6 {
			end := i + 6
			if end > len(a.Isolated) {
				end = len(a.Isolated)
			}
			y += 16
			line := ""
			for _, t := range a.Isolated[i:end] {
				if line != "" {
					line += ", "
				}
				line += t
			}
			shown = end
			p(`<text x="%.0f" y="%.1f" font-size="10" opacity=".65">%s</text>`, minX+20, y, esc(line))
		}
		if shown < len(a.Isolated) {
			y += 16
			p(`<text x="%.0f" y="%.1f" font-size="10" opacity=".65">… 他 %d 件(--html / --json に全量)</text>`,
				minX+20, y, len(a.Isolated)-shown)
		}
	}

	p(`</svg>`)
}

// isInPlacedComponent: bj が既に配置済みコンポーネント(roots のいずれかを祖先に持つ)か。
func isInPlacedComponent(bj int, placedRoots []int, parent []int) bool {
	r := bj
	for parent[r] >= 0 {
		r = parent[r]
	}
	for _, pr := range placedRoots {
		if r == pr {
			return true
		}
	}
	return false
}

// svgTooltip はノードのホバー詳細(所属テーブル一覧)。
func svgTooltip(a *Analysis, node string) string {
	ms, ok := a.CascadeGroups[node]
	if !ok {
		return node
	}
	s := node + " = CASCADE 集約:"
	seen := map[string]bool{}
	sorted := append([]string(nil), ms...)
	sort.Strings(sorted)
	for _, m := range sorted {
		if seen[m] {
			continue
		}
		seen[m] = true
		s += "\n  " + m
	}
	return s
}
