// baseline.go: 解析結果の差分監視(--baseline)。
//
// 決定的出力の配当を移行の計器にする: 前回の --json 出力と比較し、
// 「結合の逆行」だけを検出して非 0 で落ちる。改善(減少)は報告のみ。
//
// 逆行の定義:
//   - 新規の FK ペア(child→parent のテーブル対が増えた)
//   - hub 契約の FK 本数の増加(hub ごとの合計)
//   - スキーマ跨ぎ FK の増加
//
// 「橋の本数」は逆行指標にしない — 太い継ぎ目が痩せて橋になるのは前進。
// 比較はテーブル名粒度で行う(縮約ノード名は CASCADE 代表の変化で揺れるため)。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// fkPairs は Analysis から child→parent のテーブル対集合を作る。
func fkPairs(a *Analysis) map[[2]string]bool {
	out := map[[2]string]bool{}
	for _, e := range a.Edges {
		for _, fk := range e.FKs {
			out[[2]string{fk.ChildTable, fk.ParentTable}] = true
		}
	}
	for _, b := range a.Bridges {
		for _, fk := range b.FKs {
			out[[2]string{fk.ChildTable, fk.ParentTable}] = true
		}
	}
	return out
}

func crossPairs(a *Analysis) map[[2]string]bool {
	out := map[[2]string]bool{}
	for _, fk := range a.CrossFKs {
		out[[2]string{fk.ChildTable, fk.ParentSchema + "." + fk.ParentTable}] = true
	}
	return out
}

func hubTotals(a *Analysis) map[string]int {
	out := map[string]int{}
	for _, hc := range a.HubContracts {
		out[hc.Hub] += hc.ToHub + hc.FromHub
	}
	return out
}

// CompareBaseline は差分を書き出し、逆行があれば true を返す。
func CompareBaseline(w io.Writer, prev, cur *Analysis) bool {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }
	regressed := false

	sortedDiff := func(cur, prev map[[2]string]bool) (added, removed [][2]string) {
		for k := range cur {
			if !prev[k] {
				added = append(added, k)
			}
		}
		for k := range prev {
			if !cur[k] {
				removed = append(removed, k)
			}
		}
		less := func(s [][2]string) {
			sort.Slice(s, func(i, j int) bool {
				if s[i][0] != s[j][0] {
					return s[i][0] < s[j][0]
				}
				return s[i][1] < s[j][1]
			})
		}
		less(added)
		less(removed)
		return
	}

	p("■ ベースライン比較(%s)", prev.Schema)

	added, removed := sortedDiff(fkPairs(cur), fkPairs(prev))
	if len(added) > 0 {
		regressed = true
		p("  ✗ 新規の FK ペア(結合の逆行):")
		for _, k := range added {
			p("      %s → %s", k[0], k[1])
		}
	}
	if len(removed) > 0 {
		p("  ✓ 消えた FK ペア(前進): %d 対", len(removed))
	}

	cAdded, cRemoved := sortedDiff(crossPairs(cur), crossPairs(prev))
	if len(cAdded) > 0 {
		regressed = true
		p("  ✗ 新規のスキーマ跨ぎ FK(逆行):")
		for _, k := range cAdded {
			p("      %s → %s", k[0], k[1])
		}
	}
	if len(cRemoved) > 0 {
		p("  ✓ 消えたスキーマ跨ぎ FK: %d 対", len(cRemoved))
	}

	ph, ch := hubTotals(prev), hubTotals(cur)
	var hubs []string
	seen := map[string]bool{}
	for h := range ph {
		if !seen[h] {
			seen[h] = true
			hubs = append(hubs, h)
		}
	}
	for h := range ch {
		if !seen[h] {
			seen[h] = true
			hubs = append(hubs, h)
		}
	}
	sort.Strings(hubs)
	for _, h := range hubs {
		before, after := ph[h], ch[h]
		switch {
		case after > before:
			regressed = true
			p("  ✗ hub 契約の増加: %s %d → %d 本(逆行)", h, before, after)
		case after < before:
			p("  ✓ hub 契約の減少: %s %d → %d 本", h, before, after)
		}
	}

	if !regressed {
		p("  逆行なし")
	}
	return regressed
}

// LoadBaseline は過去の --json 出力を読む。
func LoadBaseline(path string) (*Analysis, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	var a Analysis
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	return &a, nil
}
