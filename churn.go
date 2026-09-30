// churn.go: 切り出し 1 本目候補のランキング(--churn / --criticality)。
//
// 移行の順序基準のうち機械化できるもの:
//
//	基準1 橋の少なさ(切断コスト)   … 既存の橋検出
//	基準3 変更頻度(移すリターン)   … git log から機械採取
//	基準4 事故コスト                … 機械化不能 → --criticality で人手注入
//
// 練習台の選定を主張から計算にする。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// Candidate は切り出し候補 1 ユニット。
type Candidate struct {
	Unit    string `json:"unit"`
	Tables  int    `json:"tables"`
	Bridges int    `json:"bridges"` // このユニットに触れる橋の本数(少ないほど安い)
	// Churn: モデルファイルの変更回数合計(多いほどリターン大)。モデルファイルを
	// 1 つも対応づけられなかったユニットは nil(未計測)— 「変更 0 回」と区別する。
	Churn *int `json:"churn"`
	// ChurnFiles: churn を数えたモデルファイルの数。
	ChurnFiles  int     `json:"churn_files"`
	Criticality float64 `json:"criticality"` // 人手注入の事故コスト(低いほど練習台向き)
}

// LoadChurn は git log からファイル別変更回数を採る。リネームを追い、移動前の
// パスへの変更は今のパスに寄せる(-M。移動前の履歴を落とさないため)。
func LoadChurn(gitDir string) (map[string]int, error) {
	out, err := exec.Command("git", "-C", gitDir, "log", "-M", "--name-status", "--pretty=format:").Output()
	if err != nil {
		return nil, fmt.Errorf("churn: git log: %w", err)
	}
	return parseChurnLog(string(out)), nil
}

// parseChurnLog は git log -M --name-status の出力(新しい順)を数える。
// 「R100<TAB>旧<TAB>新」を見たら、それより古い旧パスへの変更を新パス側
// (さらに後で改名されていれば、その今の名前)へ寄せる。
func parseChurnLog(log string) map[string]int {
	counts := map[string]int{}
	alias := map[string]string{} // 旧パス → 今のパス
	current := func(p string) string {
		for i := 0; i < 64; i++ { // 改名の連鎖を辿る(循環の保険に上限)
			next, ok := alias[p]
			if !ok {
				break
			}
			p = next
		}
		return p
	}
	for _, line := range strings.Split(log, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		switch {
		case len(f) == 3 && (strings.HasPrefix(f[0], "R") || strings.HasPrefix(f[0], "C")):
			now := current(f[2])
			counts[now]++
			if strings.HasPrefix(f[0], "R") {
				alias[f[1]] = now
			}
		case len(f) == 2:
			counts[current(f[1])]++
		}
	}
	return counts
}

// LoadCriticality は {テーブル名: 重要度} の JSON を読む。
func LoadCriticality(path string) (map[string]float64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("criticality: %w", err)
	}
	var m map[string]float64
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("criticality: %w", err)
	}
	return m, nil
}

// tableChurn はファイル別 churn をテーブル別へ写す(サフィックス一致)。
// 2 つ目の戻り値はテーブルごとのモデルファイル数 — 0 のテーブルは未計測。
func tableChurn(fileTables map[string]string, fileChurn map[string]int) (map[string]int, map[string]int) {
	out, files := map[string]int{}, map[string]int{}
	for _, table := range fileTables {
		files[table]++
	}
	for gitPath, n := range fileChurn {
		for file, table := range fileTables {
			if file == gitPath || strings.HasSuffix(file, "/"+gitPath) {
				out[table] += n
			}
		}
	}
	return out, files
}

// BuildCandidates はユニット(ブロック/島)ごとのランキングを作る。
// 並び: churn を測れたユニット → 測れないユニット(末尾の別枠)。それぞれの中は
// 橋 昇順 → churn 降順 → criticality 昇順 → 名前。
// tFiles は churn を数えたモデルファイル数(nil = --churn 無し。全ユニット未計測)。
func BuildCandidates(a *Analysis, tChurn, tFiles map[string]int, tCrit map[string]float64) []Candidate {
	tableCount := func(node string) int {
		if ms, ok := a.CascadeGroups[node]; ok {
			return len(ms)
		}
		return 1
	}
	members := func(node string) []string {
		if ms, ok := a.CascadeGroups[node]; ok {
			return ms
		}
		return []string{node}
	}
	type unit struct {
		name  string
		nodes []string
	}
	var units []unit
	for _, block := range a.Blocks {
		ms := append([]string(nil), block...)
		sort.Strings(ms)
		units = append(units, unit{name: ms[0], nodes: ms})
	}
	for _, is := range a.Islands {
		units = append(units, unit{name: is.Name, nodes: []string{is.Name}})
	}

	nodeUnit := map[string]string{}
	for _, u := range units {
		for _, n := range u.nodes {
			nodeUnit[n] = u.name
		}
	}
	bridgeCount := map[string]int{}
	for _, b := range a.Bridges {
		bridgeCount[nodeUnit[b.A]]++
		bridgeCount[nodeUnit[b.B]]++
	}

	var out []Candidate
	for _, u := range units {
		c := Candidate{Unit: u.name, Bridges: bridgeCount[u.name]}
		churn := 0
		for _, n := range u.nodes {
			c.Tables += tableCount(n)
			for _, t := range members(n) {
				churn += tChurn[t]
				c.ChurnFiles += tFiles[t]
				if v := tCrit[t]; v > c.Criticality {
					c.Criticality = v
				}
			}
		}
		if c.ChurnFiles > 0 {
			c.Churn = &churn
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if mi, mj := out[i].Churn != nil, out[j].Churn != nil; mi != mj {
			return mi // 測れたユニットが先
		}
		if out[i].Bridges != out[j].Bridges {
			return out[i].Bridges < out[j].Bridges
		}
		if out[i].Churn != nil && *out[i].Churn != *out[j].Churn {
			return *out[i].Churn > *out[j].Churn
		}
		if out[i].Criticality != out[j].Criticality {
			return out[i].Criticality < out[j].Criticality
		}
		return out[i].Unit < out[j].Unit
	})
	return out
}

// WriteCandidates はランキング上位を書き出す。
func WriteCandidates(w io.Writer, cands []Candidate, top int) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }
	measured := 0
	for _, c := range cands {
		if c.Churn != nil {
			measured++
		}
	}
	p("■ 切り出し 1 本目候補(橋 昇順 × churn 降順 × criticality 昇順)")
	p("  churn を測れたユニット %d / %d(モデルファイルを対応づけられないユニットは churn - として末尾に別枠)", measured, len(cands))
	for i, c := range cands {
		if i >= top {
			p("  … 他 %d ユニット(--json に全量)", len(cands)-top)
			break
		}
		churn := "-"
		if c.Churn != nil {
			churn = fmt.Sprintf("%d", *c.Churn)
		}
		p("  %2d. %s(%d テーブル) 橋 %d / churn %s / crit %.1f",
			i+1, c.Unit, c.Tables, c.Bridges, churn, c.Criticality)
	}
}
