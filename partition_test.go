package main

import (
	"reflect"
	"testing"
)

// 合成フィクスチャ: 2 つの塊(orders 系 / posts 系)が hub(users)を
// 共有し、小粒の衛星(badges)が orders 側に弱く繋がる形。
func partitionFixture() *ScanResult {
	fk := func(child, col, parent string, notNull bool, rule string) FK {
		return FK{Constraint: child + "." + col, ChildTable: child,
			ChildCols: []string{col}, ParentTable: parent, DeleteRule: rule, AllNotNull: notNull}
	}
	return &ScanResult{
		Schema: "test",
		Tables: []string{"badges", "comments", "invoices", "items", "orders", "post_likes", "posts", "shipments", "users"},
		FKs: []FK{
			// orders 塊(内部は環 = ブロック)
			fk("items", "order_id", "orders", true, "NO ACTION"),
			fk("shipments", "order_id", "orders", true, "NO ACTION"),
			fk("shipments", "item_id", "items", true, "NO ACTION"),
			fk("invoices", "item_id", "items", true, "NO ACTION"),
			// posts 塊(3 テーブル — 吸着閾値 3 を超えて独立サービスたりうる大きさ)
			fk("post_likes", "post_id", "posts", true, "NO ACTION"),
			fk("comments", "post_id", "posts", true, "NO ACTION"),
			// hub(users)への契約: orders 側は太く、posts 側も持つ
			fk("orders", "user_id", "users", true, "NO ACTION"),
			fk("items", "user_id", "users", false, "NO ACTION"),
			fk("invoices", "user_id", "users", false, "NO ACTION"),
			fk("posts", "user_id", "users", true, "NO ACTION"),
			fk("post_likes", "user_id", "users", false, "NO ACTION"),
			// 小粒衛星: badges は orders への橋 1 本だけ
			fk("badges", "order_id", "orders", false, "NO ACTION"),
		},
	}
}

func TestHubContractsAndPartition(t *testing.T) {
	a := Analyze(partitionFixture(), 5) // users(次数 5)だけが hub になる閾値

	t.Run("hub 契約が方向・NOT NULL 込みで集計される", func(t *testing.T) {
		found := false
		for _, hc := range a.HubContracts {
			if hc.Hub != "users" {
				t.Errorf("hub が users 以外: %+v", hc)
			}
			// orders ブロック(items/orders/shipments、代表は辞書順先頭 items)
			if hc.Unit == "items" {
				found = true
				if hc.ToHub != 2 || hc.NotNull != 1 || hc.Level != 2 {
					t.Errorf("orders 塊→users の契約: %+v", hc)
				}
			}
		}
		if !found {
			t.Fatalf("orders 塊の hub 契約が無い: %+v", a.HubContracts)
		}
	})

	t.Run("分割案: orders 圏と posts 圏に割れ、badges は吸着される", func(t *testing.T) {
		if a.Partition == nil || len(a.Partition.Groups) < 2 {
			t.Fatalf("分割されていない: %+v", a.Partition)
		}
		groupOf := map[string]int{}
		for gi, g := range a.Partition.Groups {
			for _, u := range g.Units {
				groupOf[u] = gi
			}
		}
		if groupOf["items"] == groupOf["post_likes"] {
			t.Errorf("orders 塊と posts 塊が同じグループ: %+v", a.Partition.Groups)
		}
		if groupOf["badges"] != groupOf["items"] {
			t.Errorf("小粒 badges が orders 圏に吸着されていない: %+v", a.Partition.Groups)
		}
	})

	t.Run("粒度の階段と SelectLevel", func(t *testing.T) {
		if len(a.Partition.Levels) < 2 {
			t.Fatalf("階段が 1 段しかない: %+v", a.Partition.Levels)
		}
		for i := 1; i < len(a.Partition.Levels); i++ {
			if a.Partition.Levels[i].K <= a.Partition.Levels[i-1].K {
				t.Errorf("階段の K が昇順でない: %+v", a.Partition.Levels)
			}
		}
		p := *a.Partition
		p.SelectLevel(1)
		if len(p.Groups) > 2 {
			t.Errorf("SelectLevel(1) で粗い段が選ばれていない: %d groups", len(p.Groups))
		}
	})

	t.Run("決定的(2 回実行で同一)", func(t *testing.T) {
		b := Analyze(partitionFixture(), 5)
		if !reflect.DeepEqual(a.Partition, b.Partition) {
			t.Error("分割案が実行ごとに変わる")
		}
		if !reflect.DeepEqual(a.HubContracts, b.HubContracts) {
			t.Error("hub 契約が実行ごとに変わる")
		}
	})
}
