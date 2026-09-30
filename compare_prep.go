// compare_prep.go: グラフ比較の前処理。
//
// Physical / Logical / Combined を比べるとき、hub や CASCADE 縮約をグラフごとに
// 決め直すと、分割の差が「入力の差」なのか「前処理の差」なのか分からなくなる
// (Physical では次数が足りず hub でなくなる、縮約が起きない、など)。
// そこで次を combined から 1 回だけ決め、全グラフに同じものを当てる:
//
//	頂点集合     — combined の全テーブル。ある見方で FK を持たないテーブルも
//	               孤立頂点として残す(消さない)
//	hub 集合     — combined の次数で判定。各グラフで次数を数え直さない
//	CASCADE 縮約 — combined の Union–Find の結果をそのまま適用
//
// --services N・重み→距離の変換・共起の算入有無は、指定値を全グラフへそのまま渡す。
// 単独解析(比較モード以外)の経路には触れない。
package main

import (
	"sort"
)

// CommonConditions は全グラフに共通で当てる条件。
type CommonConditions struct {
	Tables        []string            `json:"-"`
	Hubs          []Hub               `json:"hubs"`           // 次数は combined のもの
	HubThreshold  int                 `json:"hub_threshold"`  //
	CascadeGroups map[string][]string `json:"cascade_groups"` // 代表 → メンバー
}

// GraphView は 1 つの見方を共通条件で解析した結果。
// テーブル単位の分割への展開は compare_community.go(CommunityViewOf)が持つ。
type GraphView struct {
	Kind     GraphKind
	Scan     *ScanResult // 射影後
	Analysis *Analysis
}

// ComparisonInput は前処理の結果(Edge Diff / Community Diff / Coverage の入力)。
type ComparisonInput struct {
	Combined *ScanResult // 合流済み・射影前
	Common   CommonConditions
	Views    []GraphView // physical, logical, combined の順
}

// View は種類で引く。
func (ci *ComparisonInput) View(kind GraphKind) *GraphView {
	for i := range ci.Views {
		if ci.Views[i].Kind == kind {
			return &ci.Views[i]
		}
	}
	return nil
}

// HubSet は共通の hub 集合。
func (c CommonConditions) HubSet() map[string]bool {
	out := map[string]bool{}
	for _, h := range c.Hubs {
		out[h.Node] = true
	}
	return out
}

// NonHubTables は ARI などを測る共通の頂点集合(ソート済み)。
func (c CommonConditions) NonHubTables() []string {
	hubs := c.HubSet()
	var out []string
	for _, t := range c.Tables {
		if !hubs[t] {
			out = append(out, t)
		}
	}
	return out
}

// PrepareComparison は combined から共通条件を決め、3 つの見方を同じ条件で解析する。
// sc は合流済みの ScanResult(単独ソースでも動くが、片方の見方は空になる)。
func PrepareComparison(sc *ScanResult, hubThreshold, services int, includeObserved bool) *ComparisonInput {
	base := Analyze(Project(sc, GraphCombined, includeObserved), hubThreshold)
	ci := &ComparisonInput{
		Combined: sc,
		Common: CommonConditions{
			Tables:        append([]string(nil), sc.Tables...),
			Hubs:          base.Hubs,
			HubThreshold:  base.HubThreshold,
			CascadeGroups: base.CascadeGroups,
		},
	}
	sort.Strings(ci.Common.Tables)
	if ci.Common.CascadeGroups == nil {
		ci.Common.CascadeGroups = map[string][]string{}
	}

	for _, kind := range []GraphKind{GraphPhysical, GraphLogical, GraphCombined} {
		psc := Project(sc, kind, includeObserved)
		a := analyze(psc, hubThreshold, &ci.Common)
		if services > 0 && a.Partition != nil {
			a.Partition.SelectLevel(services)
		}
		ci.Views = append(ci.Views, GraphView{Kind: kind, Scan: psc, Analysis: a})
	}
	return ci
}

// removeFixedHubs は与えられた hub 集合に触れる辺を外す。次数は数え直さない。
func removeFixedHubs(edges map[Pair]*Edge, hubs []Hub) (map[Pair]*Edge, []Hub) {
	set := map[string]bool{}
	for _, h := range hubs {
		set[h.Node] = true
	}
	out := map[Pair]*Edge{}
	for p, e := range edges {
		if set[p.A] || set[p.B] {
			continue
		}
		out[p] = e
	}
	return out, append([]Hub(nil), hubs...)
}

// contractFixed は与えられたグループ(代表 → メンバー)でノードを潰す。
// Contract と違い、このグラフに CASCADE があるかどうかは見ない。
func contractFixed(edges map[Pair]*Edge, groups map[string][]string) (map[Pair]*Edge, map[string][]string) {
	rep := map[string]string{}
	for root, ms := range groups {
		for _, m := range ms {
			rep[m] = root
		}
	}
	find := func(t string) string {
		if r, ok := rep[t]; ok {
			return r
		}
		return t
	}
	out := map[Pair]*Edge{}
	for p, e := range edges {
		ra, rb := find(p.A), find(p.B)
		if ra == rb {
			continue
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
	cp := make(map[string][]string, len(groups))
	for root, ms := range groups {
		cp[root] = append([]string(nil), ms...)
	}
	return out, cp
}
