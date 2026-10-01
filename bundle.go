// bundle.go: 影テーブル(履歴・アーカイブ)を本体と 1 頂点に束ねる(--bundle)。
//
// <本体>_version のような影テーブルは、多くが FK を持たず孤立テーブルとして
// 大量に出る。名前に頼らずに見抜くのは難しい(列集合の包含で探すと、参照
// マスタ同士が id・name・監査列を共有するので候補が数万組になる)。そこで
// ツールは見抜かず、使う側が束ね方の規則を渡す(--relations と同じ考え方で、
// アプリ固有の規則をツールに持たせない)(#66)。
//
// 規則ファイル(1 行 1 規則、# から行末はコメント):
//
//	suffix _version     orders_version → orders
//	prefix archive_     archive_orders → orders
//
// 本体が実在するときだけ束ねる。規則に当たっても本体が無い表は束ねず注記する。
package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// BundleRule は束ね方の規則 1 つ。
type BundleRule struct {
	Kind  string // "suffix" / "prefix"
	Value string
}

func (r BundleRule) String() string { return r.Kind + " " + r.Value }

// base は規則に当たれば本体の名前を返す。
func (r BundleRule) base(t string) (string, bool) {
	switch r.Kind {
	case "suffix":
		if len(t) > len(r.Value) && strings.HasSuffix(t, r.Value) {
			return t[:len(t)-len(r.Value)], true
		}
	case "prefix":
		if len(t) > len(r.Value) && strings.HasPrefix(t, r.Value) {
			return t[len(r.Value):], true
		}
	}
	return "", false
}

// LoadBundleRules は規則ファイルを読む。
func LoadBundleRules(path string) ([]BundleRule, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	defer func() { _ = f.Close() }()
	var rules []BundleRule
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || (fields[0] != "suffix" && fields[0] != "prefix") {
			return nil, fmt.Errorf("bundle: %s:%d: 「suffix 値」か「prefix 値」の形で書く(%q)", path, n, strings.TrimSpace(line))
		}
		rules = append(rules, BundleRule{Kind: fields[0], Value: fields[1]})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("bundle: %s に規則が無い", path)
	}
	return rules, nil
}

// BundleTables は規則で影テーブルを本体に束ねた写しと、付け替え表を返す。
// 規則は上から順に試し、本体が実在する最初の規則で束ねる。
func BundleTables(sc *ScanResult, rules []BundleRule) (*ScanResult, map[string]string) {
	exists := map[string]bool{}
	for _, t := range sc.Tables {
		exists[t] = true
	}
	rename := map[string]string{}
	bundled := map[string]int{}      // 規則 → 束ねた数
	orphans := map[string][]string{} // 規則 → 本体の無い表
	for _, t := range sc.Tables {
		firstHit := ""
		for _, r := range rules {
			b, ok := r.base(t)
			if !ok {
				continue
			}
			if firstHit == "" {
				firstHit = r.String()
			}
			if exists[b] {
				rename[t] = b
				bundled[r.String()]++
				firstHit = ""
				break
			}
		}
		if firstHit != "" {
			orphans[firstHit] = append(orphans[firstHit], t)
		}
	}
	// orders_version_version → orders_version → orders のような連鎖を畳む
	for t := range rename {
		seen := map[string]bool{t: true}
		for {
			next, ok := rename[rename[t]]
			if !ok || seen[next] {
				break
			}
			seen[next] = true
			rename[t] = next
		}
	}
	out := renameTables(sc, rename)

	var counts []string
	for _, r := range rules {
		if n := bundled[r.String()]; n > 0 {
			counts = append(counts, fmt.Sprintf("%s %d 個", r, n))
		}
	}
	if len(counts) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"影テーブルを本体と 1 頂点に束ねた(--bundle): %s — 本体と同じ関係になる FK は 1 本にまとめた(重みは加算しない)",
			strings.Join(counts, " / ")))
	}
	var orphanParts []string
	for _, r := range rules {
		ts := orphans[r.String()]
		if len(ts) == 0 {
			continue
		}
		sort.Strings(ts)
		shown := ts
		if len(shown) > 10 {
			shown = append(append([]string(nil), shown[:10]...), fmt.Sprintf("他 %d 個", len(ts)-10))
		}
		orphanParts = append(orphanParts, fmt.Sprintf("%s: %s", r, strings.Join(shown, ", ")))
	}
	if len(orphanParts) > 0 {
		out.Notes = append(out.Notes,
			"--bundle の規則に当たったが本体の無い表は束ねていない — "+strings.Join(orphanParts, " / "))
	}
	return out, rename
}

// renameTables はテーブル名を持つ全フィールドを付け替えた写しを返す。
// 付け替えで同じ関係(子・列・親)になる FK は 1 本にまとめて証拠を足し、
// 付け替えで自己参照になった FK(影テーブル → 本体)は頂点の内側なので捨てる。
func renameTables(sc *ScanResult, rename map[string]string) *ScanResult {
	if len(rename) == 0 {
		return sc
	}
	name := func(t string) string {
		if n, ok := rename[t]; ok {
			return n
		}
		return t
	}
	dedupe := func(ts []string) []string {
		if ts == nil {
			return nil
		}
		seen := map[string]bool{}
		var out []string
		for _, t := range ts {
			if n := name(t); !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out
	}
	out := *sc
	out.Tables = dedupe(sc.Tables)
	out.PhysicalTables = dedupe(sc.PhysicalTables)
	out.LogicalTables = dedupe(sc.LogicalTables)
	out.ModelTables = dedupe(sc.ModelTables)
	out.Notes = append([]string(nil), sc.Notes...)

	out.FKs = nil
	index := map[string]int{}
	for _, fk := range sc.FKs {
		selfRef := fk.ChildTable == fk.ParentTable
		fk.ChildTable, fk.ParentTable = name(fk.ChildTable), name(fk.ParentTable)
		fk.Evidences = append([]Evidence(nil), fk.Evidences...)
		if !selfRef && fk.ChildTable == fk.ParentTable {
			continue
		}
		key := relationKey(fk)
		if i, ok := index[key]; ok {
			for _, ev := range fk.Evidences {
				dup := false
				for _, e := range out.FKs[i].Evidences {
					dup = dup || (e.Source == ev.Source && e.Origin == ev.Origin)
				}
				if !dup {
					out.FKs[i].Evidences = append(out.FKs[i].Evidences, ev)
				}
			}
			continue
		}
		index[key] = len(out.FKs)
		out.FKs = append(out.FKs, fk)
	}
	out.CrossFKs = nil
	for _, fk := range sc.CrossFKs {
		fk.ChildTable = name(fk.ChildTable)
		out.CrossFKs = append(out.CrossFKs, fk)
	}
	out.Suspects = nil
	for _, s := range sc.Suspects {
		s.FromTable, s.ToTable = name(s.FromTable), name(s.ToTable)
		if s.FromTable != s.ToTable {
			out.Suspects = append(out.Suspects, s)
		}
	}
	if sc.FileTables != nil {
		out.FileTables = map[string]string{}
		for f, t := range sc.FileTables {
			out.FileTables[f] = name(t)
		}
	}
	out.Cooc = renameCooc(sc.Cooc, rename)
	return &out
}

// renameCooc は共起のテーブル名を付け替える。本体と影テーブルの組は頂点の
// 内側なので捨て、影テーブルと他の表の組は本体の組に足す。tx 数も合算するが、
// 本体と影の両方を書いた tx は二重に数えうる(近似。上側に寄る)。
func renameCooc(c *CoocData, rename map[string]string) *CoocData {
	if c == nil || len(rename) == 0 {
		return c
	}
	name := func(t string) string {
		if n, ok := rename[t]; ok {
			return n
		}
		return t
	}
	out := &CoocData{TableTx: map[string]int{}, TotalTx: c.TotalTx}
	for t, n := range c.TableTx {
		out.TableTx[name(t)] += n
	}
	sum := map[[2]string]int{}
	var keys [][2]string
	for _, p := range c.Pairs {
		a, b := name(p.A), name(p.B)
		if a == b {
			continue
		}
		if a > b {
			a, b = b, a
		}
		k := [2]string{a, b}
		if _, ok := sum[k]; !ok {
			keys = append(keys, k)
		}
		sum[k] += p.Count
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		out.Pairs = append(out.Pairs, CoocPair{A: k[0], B: k[1], Count: sum[k]})
	}
	return out
}
