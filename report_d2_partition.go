// report_d2_partition.go: 分割案(Girvan–Newman)の D2 図。
// グループ = コンテナ、中身は通常の E-R ノード。グループを跨ぐ辺
// (橋・hub 契約・疑い[強])がそのまま「サービス間 API 面」として見える。
package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// WriteD2PartitionView は分割案のスクリプトを書き出す。
func WriteD2PartitionView(w io.Writer, a *Analysis) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }

	p(`# habakiri 分割案 — %s(Q=%.2f / %d グループ)`, a.Schema, partQ(a), len(partGroups(a)))
	p(`direction: right`)
	p(`classes: {
  w1: {style: {stroke: "#a8a29a"; font-color: "#6e6a63"}}
  w2: {style: {stroke: "#4c7fb8"; font-color: "#4c7fb8"}}
  w3: {style: {stroke: "#a83232"; font-color: "#a83232"}}
  bridge: {style: {stroke-width: 4}}
  suspectS: {style: {stroke: "#7a5fae"; stroke-dash: 3; font-color: "#7a5fae"}}
}`)

	if a.Partition == nil {
		return
	}

	// ユニット代表 → グループ番号
	groupOfUnit := map[string]int{}
	hubGroup := map[string]int{}
	for gi, g := range a.Partition.Groups {
		for _, u := range g.Units {
			groupOfUnit[u] = gi
		}
		for _, h := range g.Hubs {
			hubGroup[h] = gi
		}
	}

	// 縮約ノード → ユニット代表(ブロック経由)
	unitName := map[string]string{}
	for _, block := range a.Blocks {
		members := append([]string(nil), block...)
		sort.Strings(members)
		for _, m := range members {
			unitName[m] = members[0]
		}
	}
	for _, is := range a.Islands {
		unitName[is.Name] = is.Name
	}
	memberRep := map[string]string{}
	for root, ms := range a.CascadeGroups {
		for _, m := range ms {
			memberRep[m] = root
		}
	}
	groupOfNode := func(node string) (int, bool) {
		u, ok := unitName[node]
		if !ok {
			return 0, false
		}
		gi, ok := groupOfUnit[u]
		return gi, ok
	}

	// グループコンテナ + ノード(sql_table)
	path := map[string]string{}
	childCols := map[string][]FK{}
	for _, e := range a.Edges {
		for _, fk := range e.FKs {
			childCols[fk.ChildTable] = append(childCols[fk.ChildTable], fk)
		}
	}
	emitNode := func(container, name string) {
		path[name] = container + "." + d2Quote(name)
		p(`%s: {shape: sql_table}`, path[name])
		if ms, ok := a.CascadeGroups[name]; ok {
			sorted := append([]string(nil), ms...)
			sort.Strings(sorted)
			seen := map[string]bool{name: true}
			for _, m := range sorted {
				if seen[m] {
					continue
				}
				seen[m] = true
				p(`%s.%s: CASCADE`, path[name], d2Quote(m))
			}
			return
		}
		seen := map[string]bool{}
		for _, fk := range childCols[name] {
			col := strings.Join(fk.ChildCols, ",")
			if seen[col] {
				continue
			}
			seen[col] = true
			nn := "FK NULL可"
			if fk.AllNotNull {
				nn = "FK NOT NULL"
			}
			p(`%s.%s: %s`, path[name], d2Quote(col), nn)
		}
	}

	for gi, g := range a.Partition.Groups {
		id := fmt.Sprintf("s%d", gi+1)
		p(`%s: {label: "S%d %s(%d tables)"; style: {stroke-dash: 3; fill: transparent; font-size: 20}}`,
			id, gi+1, g.Name, g.Tables)
		// このグループの縮約ノード = unitName 経由でこのグループに属する全ノード
		var members []string
		seen := map[string]bool{}
		for node, u := range unitName {
			if gj, ok := groupOfUnit[u]; ok && gj == gi && !seen[node] {
				seen[node] = true
				members = append(members, node)
			}
		}
		sort.Strings(members)
		for _, m := range members {
			emitNode(id, m)
		}
		// hub(所有者としてこのグループに落ちたもの)
		for _, h := range g.Hubs {
			path[h] = id + "." + d2Quote(h)
			p(`%s: {style: {bold: true; fill: "#2a2440"; font-color: "#ffffff"}}`, path[h])
		}
	}

	weightClass := func(maxW float64) string {
		switch {
		case maxW >= 3:
			return "w3"
		case maxW >= 2:
			return "w2"
		default:
			return "w1"
		}
	}

	// FK エッジ。グループ内は細く、グループ跨ぎは太線 + ✂(サービス間 API 面)。
	for _, e := range a.Edges {
		for _, fk := range e.FKs {
			from, to := path[nodeOrRep(fk.ChildTable, memberRep)], path[nodeOrRep(fk.ParentTable, memberRep)]
			if from == "" || to == "" || from == to {
				continue
			}
			ga, okA := groupOfNode(nodeOrRep(fk.ChildTable, memberRep))
			gb, okB := groupOfNode(nodeOrRep(fk.ParentTable, memberRep))
			cross := okA && okB && ga != gb
			cls := weightClass(fkWeight(fk))
			label := strings.Join(fk.ChildCols, ",")
			if cross {
				p(`%s -> %s: "✂ %s" {class: [%s; bridge]}`, from, to, label, cls)
			} else if e.Bridge {
				p(`%s -> %s: "%s" {class: [%s]}`, from, to, label, cls)
			}
			// グループ内の非橋(ブロック内)エッジは省く — コンテナ内の密結合は自明
		}
	}

	// hub 契約はグループ単位に集約して 1 本で描く(ユニット単位だと同じ hub へ
	// 20 本以上の並行線になり読めない — Mastodon 実測)。グループ跨ぎのみ。
	type ghKey struct {
		group int
		hub   string
	}
	ghAgg := map[ghKey]*HubContract{}
	for i := range a.HubContracts {
		hc := &a.HubContracts[i]
		gu, okU := groupOfUnit[hc.Unit]
		gh, okH := hubGroup[hc.Hub]
		if !okU || !okH || gu == gh {
			continue
		}
		k := ghKey{gu, hc.Hub}
		agg, ok := ghAgg[k]
		if !ok {
			agg = &HubContract{Hub: hc.Hub, Level: 1}
			ghAgg[k] = agg
		}
		agg.ToHub += hc.ToHub
		agg.FromHub += hc.FromHub
		agg.NotNull += hc.NotNull
		if hc.Level > agg.Level {
			agg.Level = hc.Level
		}
	}
	var ghKeys []ghKey
	for k := range ghAgg {
		ghKeys = append(ghKeys, k)
	}
	sort.Slice(ghKeys, func(i, j int) bool {
		if ghKeys[i].group != ghKeys[j].group {
			return ghKeys[i].group < ghKeys[j].group
		}
		return ghKeys[i].hub < ghKeys[j].hub
	})
	for _, k := range ghKeys {
		agg := ghAgg[k]
		cls := "w1"
		if agg.Level >= 2 {
			cls = "w2"
		}
		p(`s%d -> %s: "hub契約 %d本(NOT NULL %d)" {class: [%s; bridge]}`,
			k.group+1, path[agg.Hub], agg.ToHub+agg.FromHub, agg.NotNull, cls)
	}

	// 疑い[強](グループ跨ぎのみ)
	seenSus := map[string]bool{}
	for _, s := range a.Suspects {
		if !s.Strong {
			continue
		}
		fn := nodeOrRep(s.FromTable, memberRep)
		tn := nodeOrRep(s.ToTable, memberRep)
		if path[fn] == "" || path[tn] == "" || fn == tn {
			continue
		}
		ga, okA := groupOfNode(fn)
		gb, okB := groupOfNode(tn)
		if !okA || !okB || ga == gb {
			continue
		}
		k := fn + "\x00" + tn
		if seenSus[k] || seenSus[tn+"\x00"+fn] {
			continue
		}
		seenSus[k] = true
		p(`%s -- %s: {class: [suspectS]}`, path[fn], path[tn])
	}
}

func nodeOrRep(t string, memberRep map[string]string) string {
	if r, ok := memberRep[t]; ok {
		return r
	}
	return t
}

func partQ(a *Analysis) float64 {
	if a.Partition == nil {
		return 0
	}
	return a.Partition.Modularity
}

func partGroups(a *Analysis) []ServiceGroup {
	if a.Partition == nil {
		return nil
	}
	return a.Partition.Groups
}
