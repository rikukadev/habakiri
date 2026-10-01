package main

import (
	"os"
	"strings"
	"testing"
)

func TestBundleTables(t *testing.T) {
	sc, err := LoadSchemaJSON("testdata/bundle/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := LoadBundleRules("testdata/bundle/rules.txt")
	if err != nil {
		t.Fatal(err)
	}
	out, rename := BundleTables(sc, rules)

	t.Run("本体が実在する影テーブルだけ束ねる", func(t *testing.T) {
		want := "customers,ghost_version,order_items,orders"
		if got := strings.Join(out.Tables, ","); got != want {
			t.Errorf("tables = %s, want %s", got, want)
		}
		if rename["orders_version"] != "orders" || rename["archive_orders"] != "orders" || rename["customers_version"] != "customers" {
			t.Errorf("付け替え表: %v", rename)
		}
		if _, ok := rename["ghost_version"]; ok {
			t.Error("本体の無い ghost_version を束ねた")
		}
	})

	t.Run("本体と同じ関係になる FK は 1 本(重みは加算しない)", func(t *testing.T) {
		idx := fkIndex(out.FKs)
		fk, ok := idx["orders(customer_id)→customers"]
		if !ok {
			t.Fatalf("orders → customers が無い: %v", idx)
		}
		if len(fk.Evidences) != 2 {
			t.Errorf("本体と影の 2 件の証拠を期待: %+v", fk.Evidences)
		}
		if e := BuildEdges(out.FKs)[mkPair("customers", "orders")]; e == nil || e.Weight != 2 {
			t.Errorf("重みが倍になっていないか: %+v", e)
		}
		// 影 → 本体の FK は頂点の内側
		for key := range idx {
			if strings.HasPrefix(key, "orders(id)") {
				t.Errorf("影 → 本体の FK が残っている: %s", key)
			}
		}
	})

	t.Run("注記: 束ねた数と、本体の無い表", func(t *testing.T) {
		notes := strings.Join(out.Notes, "\n")
		for _, want := range []string{"suffix _version 2 個", "prefix archive_ 1 個", "本体の無い表は束ねていない — suffix _version: ghost_version"} {
			if !strings.Contains(notes, want) {
				t.Errorf("注に %q が無い: %v", want, out.Notes)
			}
		}
	})
}

func TestRenameCooc(t *testing.T) {
	c := &CoocData{
		Pairs: []CoocPair{
			{A: "orders", B: "orders_version", Count: 9}, // 頂点の内側 → 捨てる
			{A: "customers", B: "orders", Count: 2},
			{A: "customers", B: "orders_version", Count: 3}, // orders に寄せて足す
		},
		TableTx: map[string]int{"orders": 10, "orders_version": 9, "customers": 5},
		TotalTx: 12,
	}
	out := renameCooc(c, map[string]string{"orders_version": "orders"})
	if len(out.Pairs) != 1 || out.Pairs[0] != (CoocPair{A: "customers", B: "orders", Count: 5}) {
		t.Errorf("pairs = %+v", out.Pairs)
	}
	if out.TableTx["orders"] != 19 || out.TotalTx != 12 {
		t.Errorf("tx: %v / %d", out.TableTx, out.TotalTx)
	}
}

func TestCLIBundle(t *testing.T) {
	plain := string(runCLI(t, "--schema-json", "testdata/bundle/schema.json"))
	if !strings.Contains(plain, "customers_version") {
		t.Fatalf("前提: 束ねなければ customers_version が出る:\n%s", plain)
	}
	out := string(runCLI(t, "--schema-json", "testdata/bundle/schema.json", "--bundle", "testdata/bundle/rules.txt"))
	if strings.Contains(out, "customers_version") && !strings.Contains(out, "影テーブル") {
		t.Errorf("customers_version が孤立として残っている:\n%s", out)
	}
	if !strings.Contains(out, "スキーマ shop: 4 テーブル") {
		t.Errorf("束ねた後のテーブル数:\n%s", out)
	}

	t.Setenv("HABAKIRI_DSN", "")
	var stdout, stderr strings.Builder
	bad := t.TempDir() + "/bad.txt"
	if err := os.WriteFile(bad, []byte("infix _v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run("habakiri", []string{"--schema-json", "testdata/bundle/schema.json", "--bundle", bad}, &stdout, &stderr); code != 2 {
		t.Errorf("不正な規則は exit 2: got %d", code)
	}
}
