package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadCoocGeneralLog(t *testing.T) {
	// スレッド 10 と 11 が交錯。11 は ROLLBACK、12 は autocommit 単発(共起にならない)。
	log := `2026-09-30T12:00:00.000000Z	   10 Query	BEGIN
2026-09-30T12:00:00.100000Z	   11 Query	START TRANSACTION
2026-09-30T12:00:00.200000Z	   10 Query	INSERT INTO ` + "`orders`" + ` (id) VALUES (1)
2026-09-30T12:00:00.300000Z	   11 Query	UPDATE users SET x=1
2026-09-30T12:00:00.400000Z	   10 Query	UPDATE db1.order_items SET y=2
    WHERE id IN (1,2)
2026-09-30T12:00:00.500000Z	   11 Query	DELETE FROM sessions WHERE id=9
2026-09-30T12:00:00.600000Z	   10 Query	SELECT * FROM audit_log
2026-09-30T12:00:00.700000Z	   10 Query	COMMIT
2026-09-30T12:00:00.800000Z	   11 Query	ROLLBACK
2026-09-30T12:00:00.900000Z	   12 Query	INSERT INTO lonely (id) VALUES (1)
2026-09-30T12:00:01.000000Z	   10 Query	BEGIN
2026-09-30T12:00:01.100000Z	   10 Query	INSERT INTO orders (id) VALUES (2)
2026-09-30T12:00:01.200000Z	   10 Query	REPLACE INTO order_items (id) VALUES (2)
2026-09-30T12:00:01.300000Z	   10 Query	COMMIT
`
	pairs, err := LoadCooc(writeTmp(t, log))
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 {
		t.Fatalf("ペアが 1 種でない: %+v", pairs)
	}
	got := pairs[0]
	// orders×order_items が 2 tx(SELECT の audit_log は書き込みでないので入らない、
	// ROLLBACK した users×sessions は捨てる、スキーマ修飾とバッククォートは剥がす)
	if got.A != "order_items" || got.B != "orders" || got.Count != 2 {
		t.Errorf("orders×order_items ×2 を期待: %+v", got)
	}
}

func TestLoadCoocNeutral(t *testing.T) {
	pairs, err := LoadCooc(writeTmp(t, `# comment
orders, order_items, inventory
orders order_items
single_table
`))
	if err != nil {
		t.Fatal(err)
	}
	idx := map[string]int{}
	for _, p := range pairs {
		idx[p.A+"-"+p.B] = p.Count
	}
	if idx["order_items-orders"] != 2 || idx["inventory-orders"] != 1 || idx["inventory-order_items"] != 1 {
		t.Errorf("集計が違う: %+v", pairs)
	}
}

func TestCoocInAnalyze(t *testing.T) {
	sc := partitionFixture()
	// badges×orders は橋(L1)。共起を与えるとレベル +1 になる。
	// また posts×invoices は FK なし → 実測結合として Cooc に出る。
	sc.Cooc = []CoocPair{
		{A: "badges", B: "orders", Count: 5},
		{A: "invoices", B: "posts", Count: 3},
	}
	a := Analyze(sc, 5)

	t.Run("橋 × 共起 → 切断レベル +1", func(t *testing.T) {
		for _, b := range a.Bridges {
			if (b.A == "badges" && b.B == "orders") || (b.A == "orders" && b.B == "badges") {
				if b.CutLevel != 2 {
					t.Errorf("badges 橋のレベルが +1 されていない: %+v", b)
				}
				return
			}
		}
		t.Fatalf("badges の橋が見つからない: %+v", a.Bridges)
	})

	t.Run("FK なし共起は Cooc レポートに HasFK=false で出る", func(t *testing.T) {
		for _, c := range a.Cooc {
			if c.A == "invoices" && c.B == "posts" {
				if c.HasFK || c.Count != 3 {
					t.Errorf("invoices×posts: %+v", c)
				}
				return
			}
		}
		t.Fatalf("invoices×posts が Cooc に無い: %+v", a.Cooc)
	})
}
