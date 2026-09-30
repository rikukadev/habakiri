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

// WriteD2 は「切る前」のスクリプト(橋を ✂ で強調)を書き出す。
func WriteD2(w io.Writer, a *Analysis) {
	writeD2(w, a, 0)
}

// WriteD2Level は「レベル L まで切った後」のスクリプトを書き出す。
// レベル L 以下の橋は除去され、それより高い橋は ✂ 付きで残る。
//   L=1: 結果整合だけで切れる橋のみ(サービス切り出しはこれで足りることが多い)
//   L=2: 存在保証 API / マスタ複製込み(実務では稀)
//   L=3: 最大分解(参考値)
func WriteD2Level(w io.Writer, a *Analysis, level int) {
	writeD2(w, a, level)
}

func writeD2(w io.Writer, a *Analysis, cutLevel int) {
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
  cooc: {style: {stroke: "#c98a4b"; font-color: "#c98a4b"; stroke-width: 2}}
}`)

	// 図に入れるのはグラフ(テーブル・FK・橋・疑い)だけ。統計・hub 一覧・
	// 島・孤立などの文字情報は --html / テキスト出力の持ち場(ユーザー指示)。

	// ノードのパス(ブロックはコンテナに入れる)と、テーブルごとの FK 列
	path := map[string]string{}
	childCols := map[string][]FK{} // 単独テーブル名 → その子側 FK(行として出す)
	for _, e := range a.Edges {
		for _, fk := range e.FKs {
			childCols[fk.ChildTable] = append(childCols[fk.ChildTable], fk)
		}
	}

	// ブロックの枠は描かない(ユーザー判断で一旦不要)。切った後の図では
	// 橋が消えることで、ブロック = 独立成分として自然に離れて配置される。
	for _, block := range a.Blocks {
		members := append([]string(nil), block...)
		sort.Strings(members)
		for _, name := range members {
			path[name] = d2Quote(name)
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
			switch {
			case e.Bridge && e.CutLevel <= cutLevel:
				// このレベルでは切断済み — 橋は存在しない
			case e.Bridge:
				p(`%s -> %s: "✂L%d %s" {class: [%s; bridge]}`, from, to, e.CutLevel, label, cls)
			default:
				p(`%s -> %s: "%s" {class: [%s]}`, from, to, label, cls)
			}
		}
	}

	// 実測共起(橙)。FK なしの対のみ = 宣言に現れない不変条件の候補。
	for _, c := range a.Cooc {
		if c.HasFK || c.Suppressed {
			continue
		}
		pa, pb := path[c.A], path[c.B]
		if pa == "" || pb == "" {
			continue
		}
		p(`%s -- %s: "共起 %d(npmi %.2f)" {class: [cooc]}`, pa, pb, c.Count, c.NPMI)
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
		// [弱](メソッド参照)は図では省く — ノイズが利得を上回る(実測)。
		// [強]もラベルは付けない: 線種(破線紫)と HTML の注で十分。
		if !s.Strong {
			continue
		}
		p(`%s -- %s: {class: [suspectS]}`, path[fn], path[tn])
	}

}
