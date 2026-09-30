// report_d2.go: 解析結果を D2(https://d2lang.com)スクリプトとして出力する。
// sql_table shape で E-R 図の語彙(箱 + 列 + ポート接続)をそのまま使い、
// レイアウトは D2 のエンジン(dagre)に任せる — 手書きレイアウトはしない。
//
// 視覚エンコードは SVG 版と同じ:
//   線種 = 種類(実線 = FK / 太線 = 橋 / 破線紫 = 宣言外の疑い)
//   色   = 重み(グレー NULL可 / 青 NOT NULL / 朱 CASCADE 級)
package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// d2Quote: D2 のキーとして安全な形に(記号入りはクオート)。
func d2Quote(s string) string {
	if strings.ContainsAny(s, ".:-{}[]#&()' \"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// WriteD2 はスクリプトを書き出す。
func WriteD2(w io.Writer, a *Analysis) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }

	p(`# habakiri — %s(%d テーブル / %d FK / 橋 %d 本)`, a.Schema, a.TableCount, a.FKCount, len(a.Bridges))
	p(`direction: right`)
	p(`classes: {
  w1: {style: {stroke: "#a8a29a"; font-color: "#6e6a63"}}
  w2: {style: {stroke: "#4c7fb8"; font-color: "#4c7fb8"}}
  w3: {style: {stroke: "#a83232"; font-color: "#a83232"}}
  bridge: {style: {stroke-width: 4}}
  suspectS: {style: {stroke: "#7a5fae"; stroke-dash: 3; font-color: "#7a5fae"}}
  suspectW: {style: {stroke: "#c4b5e0"; stroke-dash: 3; font-color: "#c4b5e0"}}
}`)

	// hub と凡例(テキストブロック)
	var hubLines []string
	for _, h := range a.Hubs {
		hubLines = append(hubLines, fmt.Sprintf("- %s(次数 %d)", h.Node, h.Degree))
	}
	p(`meta: |md
## %s
%d テーブル / %d FK / 橋 %d 本

**hub(除外・共有 or 複製で扱う):**
%s

線 = FK(実線・矢印は子→親)/ 破線紫 = 宣言外の疑い
色 = 重み: グレー NULL可 / 青 NOT NULL / 朱 CASCADE 級 / 太線 = 橋 ✂
| {near: top-left}`, a.Schema, a.TableCount, a.FKCount, len(a.Bridges), strings.Join(hubLines, "\n"))

	// ノードのパス(ブロックはコンテナに入れる)と、テーブルごとの FK 列
	path := map[string]string{}
	childCols := map[string][]FK{} // 単独テーブル名 → その子側 FK(行として出す)
	for _, e := range a.Edges {
		for _, fk := range e.FKs {
			childCols[fk.ChildTable] = append(childCols[fk.ChildTable], fk)
		}
	}

	for bi, block := range a.Blocks {
		container := ""
		if len(block) > 1 {
			id := fmt.Sprintf("b%d", bi)
			p(`%s: {label: "ブロック B%d"; style: {stroke-dash: 3; fill: transparent}}`, id, bi)
			container = id + "."
		}
		members := append([]string(nil), block...)
		sort.Strings(members)
		for _, name := range members {
			path[name] = container + d2Quote(name)
			p(`%s: {shape: sql_table}`, path[name])
			if ms, ok := a.CascadeGroups[name]; ok {
				// CASCADE 集約: メンバーを行として列挙
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
			} else {
				// 単独テーブル: 子側 FK 列を行として列挙
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
		}
	}

	rep := map[string]string{}
	for root, ms := range a.CascadeGroups {
		for _, m := range ms {
			rep[m] = root
		}
	}
	nodeOf := func(t string) string {
		if p, ok := path[t]; ok {
			return p
		}
		if p, ok := path[rep[t]]; ok {
			return p
		}
		return ""
	}

	// FK エッジ(子 → 親、列名ラベル、重み色、橋は太線 + ✂)
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
	for _, e := range a.Edges {
		if len(e.FKs) == 0 {
			continue
		}
		// FK 1 本ごとに向きが分かるので、束ねずに 1 本ずつ描く(E-R 図の流儀)
		for _, fk := range e.FKs {
			from, to := nodeOf(fk.ChildTable), nodeOf(fk.ParentTable)
			if from == "" || to == "" {
				continue
			}
			// 単独テーブルで列の行があるなら、行ポートから接続する
			if _, isGroup := a.CascadeGroups[fk.ChildTable]; !isGroup && path[fk.ChildTable] != "" {
				from = from + "." + d2Quote(strings.Join(fk.ChildCols, ","))
			}
			cls := weightClass(fkWeight(fk))
			label := strings.Join(fk.ChildCols, ",")
			if e.Bridge {
				p(`%s -> %s: "✂ %s" {class: [%s; bridge]}`, from, to, label, cls)
			} else {
				p(`%s -> %s: "%s" {class: [%s]}`, from, to, label, cls)
			}
		}
	}

	// 宣言外の疑い(破線紫)。FK が既にある対・同一ノードは省く。
	fkPair := map[string]bool{}
	for _, e := range a.Edges {
		fkPair[e.A+"\x00"+e.B] = true
		fkPair[e.B+"\x00"+e.A] = true
	}
	nodeName := func(t string) string {
		if _, ok := path[t]; ok {
			return t
		}
		return rep[t]
	}
	seenSus := map[string]bool{}
	for _, s := range a.Suspects {
		fn, tn := nodeName(s.FromTable), nodeName(s.ToTable)
		// 島・孤立・hub に落ちた側はグラフ上に居ないので描かない
		if path[fn] == "" || path[tn] == "" || fn == tn || fkPair[fn+"\x00"+tn] {
			continue
		}
		key := fn + "\x00" + tn
		if seenSus[key] || seenSus[tn+"\x00"+fn] {
			continue
		}
		seenSus[key] = true
		cls, lbl := "suspectW", "疑い[弱]"
		if s.Strong {
			cls, lbl = "suspectS", "疑い[強]"
		}
		p(`%s -- %s: "%s" {class: [%s]}`, path[fn], path[tn], lbl, cls)
	}

	// hub 経由のみの島(グリッドコンテナ)
	if len(a.Islands) > 0 {
		p(`islands: {`)
		p(`  label: "hub 経由のみで繋がる島(%d)— hub との参照を値化すれば独立できる"`, len(a.Islands))
		p(`  grid-columns: 5`)
		p(`  style: {stroke-dash: 3; fill: transparent}`)
		for _, is := range a.Islands {
			label := is.Name
			if is.Tables > 1 {
				label = fmt.Sprintf("%s (+%d)", is.Name, is.Tables-1)
			}
			p(`  %s: {label: %q}`, d2Quote(is.Name), label)
		}
		p(`}`)
	}

	// 孤立(テキスト)。1 行に全部並べるとキャンバスがその幅まで伸びるので、
	// 6 個ずつの箇条書きに畳む(Magento の孤立 110 個で実測 25,000px になった)。
	if len(a.Isolated) > 0 {
		var lines []string
		for i := 0; i < len(a.Isolated); i += 6 {
			end := i + 6
			if end > len(a.Isolated) {
				end = len(a.Isolated)
			}
			lines = append(lines, "- "+strings.Join(a.Isolated[i:end], ", "))
		}
		p(`isolated: |md
**孤立 %d(FK なし — 今日でも動かせる)**

%s
| {near: bottom-left}`, len(a.Isolated), strings.Join(lines, "\n"))
	}
}
