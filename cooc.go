// cooc.go: 同一トランザクション内の書き込み共起(材料 2)を読む。
//
// FK が「宣言された関係」なのに対し、共起は「実際に効いている関係」。
// 対応形式(自動判別):
//
//  1. MySQL general log — 接続(スレッド)ID ごとに BEGIN〜COMMIT を追跡し、
//     その間の INSERT / UPDATE / DELETE / REPLACE の対象テーブル集合を 1 tx とする。
//     autocommit の単発文は tx にならない(共起の証拠にならない)ので読まない。
//  2. Postgres ログ(log_statement=all の stderr)— [pid] を接続として
//     BEGIN〜COMMIT を追跡。statement: と execute <name>: の両方を読む。
//  3. 中立形式 — 1 行 = 1 tx のテーブル列挙(カンマ / 空白区切り、# はコメント)。
//     APM トレース等からの持ち込み口。
//
// 狙いは時間的局所性ではなくトランザクション原子性 — 「近い時刻」ではなく
// 「一緒に成功 / 一緒に失敗を要求されている」ことが不変条件の証拠になる。
// 時間窓ベースの緩い共起(saga やジョブ経由の非同期結合を拾う)は将来枠で、
// その場合も中立形式で外部集計を持ち込めば今日でも使える。
//
// 観測期間の罠(月次バッチは 1 週間のログに現れない)があるため、
// 「共起が無いこと」は無関係の証明に使わない。使うのは:
//   - FK なし・共起あり → 実測の暗黙結合(図と分割グラフに参加)
//   - 橋 × 共起あり     → 同一 tx の原子性に依存 = 切断レベル +1
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// CoocPair はテーブル対の共起回数(A < B に正規化)。
type CoocPair struct {
	A     string `json:"a"`
	B     string `json:"b"`
	Count int    `json:"count"`
}

var (
	// MySQL general log のエントリ行: 時刻  スレッドID コマンド\t引数
	reGenLog = regexp.MustCompile(`^\S+\s+(\d+)\s+(Query|Execute|Prepare|Connect|Quit|Init)\b\s*(.*)$`)
	// Postgres の行: 時刻 ... [pid] LOG:  statement: SQL / execute <name>: SQL
	rePgLog = regexp.MustCompile(`\[(\d+)\].*?(?:LOG|STATEMENT):\s+(?:statement|execute [^:]*):\s*(.*)$`)
	// 書き込み文の先頭テーブル(スキーマ修飾・クォートは剥がす)
	reWriteTable = regexp.MustCompile("(?i)^\\s*(?:INSERT(?:\\s+IGNORE)?\\s+INTO|UPDATE(?:\\s+IGNORE)?|DELETE\\s+FROM|REPLACE\\s+INTO)\\s+[`\"]?(?:[a-zA-Z0-9_$]+[`\"]?\\.[`\"]?)?([a-zA-Z0-9_$]+)[`\"]?")
	reBegin      = regexp.MustCompile(`(?i)^\s*(BEGIN|START\s+TRANSACTION)\b`)
	reCommit     = regexp.MustCompile(`(?i)^\s*COMMIT\b`)
	reRollback   = regexp.MustCompile(`(?i)^\s*ROLLBACK\b`)
)

// LoadCooc はファイルを読んで共起ペアを集計する。
func LoadCooc(path string) ([]CoocPair, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cooc: %w", err)
	}
	defer func() { _ = f.Close() }()

	counts := map[[2]string]int{}
	addTx := func(tables map[string]bool) {
		if len(tables) < 2 {
			return
		}
		list := make([]string, 0, len(tables))
		for t := range tables {
			list = append(list, t)
		}
		sort.Strings(list)
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				counts[[2]string{list[i], list[j]}]++
			}
		}
	}

	// 接続 ID → 進行中トランザクションの書き込みテーブル集合(nil = tx 外)
	open := map[string]map[string]bool{}
	sawDBLog := false
	var neutralTx []map[string]bool

	handle := func(thread, arg string) {
		switch {
		case reBegin.MatchString(arg):
			open[thread] = map[string]bool{}
		case reCommit.MatchString(arg):
			if tx := open[thread]; tx != nil {
				addTx(tx)
			}
			delete(open, thread)
		case reRollback.MatchString(arg):
			delete(open, thread)
		default:
			if tx := open[thread]; tx != nil {
				if w := reWriteTable.FindStringSubmatch(arg); w != nil {
					tx[strings.ToLower(w[1])] = true
				}
			}
		}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := reGenLog.FindStringSubmatch(line); m != nil {
			sawDBLog = true
			thread, cmd, arg := m[1], m[2], m[3]
			if cmd != "Query" && cmd != "Execute" {
				// Connect / Quit などで tx は閉じる(取りこぼしより安全側)
				delete(open, thread)
				continue
			}
			handle(thread, arg)
			continue
		}
		if m := rePgLog.FindStringSubmatch(line); m != nil {
			sawDBLog = true
			handle(m[1], m[2])
			continue
		}
		if sawDBLog {
			continue // DB ログの継続行(SQL の折り返し)。先頭行で用は足りている
		}
		// 中立形式の候補として蓄積(のちに DB ログと判明したら捨てる)
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tx := map[string]bool{}
		for _, t := range strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			if t != "" {
				tx[strings.ToLower(t)] = true
			}
		}
		neutralTx = append(neutralTx, tx)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("cooc: %w", err)
	}
	// DB ログだと判明した場合、判明前に中立形式として読んだ行はヘッダの
	// 誤検出なので捨てる(mysqld のバナー行がゴミペアになる — Magento 実測)。
	if !sawDBLog {
		for _, tx := range neutralTx {
			addTx(tx)
		}
	}

	var out []CoocPair
	for k, c := range counts {
		out = append(out, CoocPair{A: k[0], B: k[1], Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out, nil
}
