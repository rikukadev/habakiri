package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// compareFixture: users が combined でだけ hub になる(宣言 4 本、DB は 1 本)。
// DB にあるのは posts→users と order_items→orders(CASCADE)の 2 本だけ。
func compareFixture() *ScanResult {
	phys := &ScanResult{Schema: "shop", Dialect: "mysql",
		Tables: []string{"audit", "comments", "likes", "order_items", "orders", "payments", "posts", "users"},
		FKs: []FK{
			physFK("fk_items_order", "order_items", "order_id", "orders", "CASCADE", true),
			physFK("fk_posts_user", "posts", "user_id", "users", "NO ACTION", true),
		}}
	logic := &ScanResult{Schema: "yii1:shop",
		Tables: []string{"comments", "likes", "order_items", "orders", "payments", "posts", "users"},
		FKs: []FK{
			yiiFK("posts", "user_id", "users", "Post.php:8"),
			yiiFK("comments", "user_id", "users", "Comment.php:8"),
			yiiFK("orders", "user_id", "users", "Order.php:8"),
			yiiFK("likes", "user_id", "users", "Like.php:8"),
			yiiFK("comments", "post_id", "posts", "Comment.php:9"),
			yiiFK("likes", "post_id", "posts", "Like.php:9"),
			yiiFK("order_items", "order_id", "orders", "OrderItem.php:8"),
			yiiFK("payments", "order_id", "orders", "Payment.php:8"),
		}}
	return MergeScans(phys, logic)
}

func TestPrepareComparisonCommonConditions(t *testing.T) {
	ci := PrepareComparison(compareFixture(), 4, 0, true)

	t.Run("hub は combined の次数で決まり、全グラフで同じ", func(t *testing.T) {
		if len(ci.Common.Hubs) != 1 || ci.Common.Hubs[0].Node != "users" || ci.Common.Hubs[0].Degree != 4 {
			t.Fatalf("combined の hub: %+v", ci.Common.Hubs)
		}
		// Physical 単独なら users の次数は 1 で hub にならない。比較では固定される
		alone := Analyze(Project(ci.Combined, GraphPhysical, true), 4)
		if len(alone.Hubs) != 0 {
			t.Fatalf("前提が崩れた: 単独の physical で hub が出ている %+v", alone.Hubs)
		}
		for _, v := range ci.Views {
			if !reflect.DeepEqual(v.Analysis.Hubs, ci.Common.Hubs) {
				t.Errorf("%s: hub が固定されていない: %+v", v.Kind, v.Analysis.Hubs)
			}
			for _, e := range v.Analysis.Edges {
				if e.A == "users" || e.B == "users" {
					t.Errorf("%s: hub への辺が残っている: %+v", v.Kind, e)
				}
			}
		}
	})

	t.Run("CASCADE 縮約は combined のものを全グラフへ", func(t *testing.T) {
		want := map[string][]string{"order_items": {"order_items", "orders"}}
		if !reflect.DeepEqual(ci.Common.CascadeGroups, want) {
			t.Fatalf("combined の縮約: %v", ci.Common.CascadeGroups)
		}
		// 宣言には CASCADE が無いので、Logical 単独では縮約されない
		alone := Analyze(Project(ci.Combined, GraphLogical, true), 4)
		if len(alone.CascadeGroups) != 0 {
			t.Fatalf("前提が崩れた: 単独の logical で縮約が起きている %v", alone.CascadeGroups)
		}
		for _, v := range ci.Views {
			if !reflect.DeepEqual(v.Analysis.CascadeGroups, want) {
				t.Errorf("%s: 縮約が固定されていない: %v", v.Kind, v.Analysis.CascadeGroups)
			}
			for _, e := range v.Analysis.Edges {
				if e.A == "orders" || e.B == "orders" {
					t.Errorf("%s: 縮約されたはずの orders が頂点として残っている: %+v", v.Kind, e)
				}
			}
		}
		// Logical では payments→orders が、代表 order_items への辺として現れる
		found := false
		for _, e := range ci.View(GraphLogical).Analysis.Edges {
			if e.A == "order_items" && e.B == "payments" {
				found = true
			}
		}
		if !found {
			t.Errorf("logical: 縮約後の辺 order_items–payments が無い: %+v", ci.View(GraphLogical).Analysis.Edges)
		}
	})

	t.Run("頂点集合は全グラフで同じ(孤立も残る)", func(t *testing.T) {
		if got := ci.Common.NonHubTables(); len(got) != 7 {
			t.Fatalf("非 hub の頂点は 7: %v", got)
		}
		for _, v := range ci.Views {
			if v.Analysis.TableCount != 8 {
				t.Errorf("%s: テーブル数が変わった: %d", v.Kind, v.Analysis.TableCount)
			}
		}
		// Physical では posts→users(hub へ)と order_items→orders(縮約の内側)しか無い。
		// 残りは消えずに孤立として残る
		want := []string{"audit", "comments", "likes", "payments"}
		if got := ci.View(GraphPhysical).Analysis.Isolated; !reflect.DeepEqual(got, want) {
			t.Errorf("physical の孤立: %v, want %v", got, want)
		}
	})

	t.Run("combined の見方は単独の combined 解析と同じ結果", func(t *testing.T) {
		alone := Analyze(Project(ci.Combined, GraphCombined, true), 4)
		var x, y bytes.Buffer
		if err := json.NewEncoder(&x).Encode(alone); err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(&y).Encode(ci.View(GraphCombined).Analysis); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(x.Bytes(), y.Bytes()) {
			t.Errorf("共通条件を当て直したら combined が変わった:\n%s\n%s", x.String(), y.String())
		}
	})
}

// 固定された縮約では、グループの誰かが FK を持てばグループ全体がグラフに居る。
// a と b は DB の CASCADE で 1 頂点。Logical には b の FK が無いが、a が c と
// 繋がっているので b を孤立として数えない。
func TestFixedGroupIsOneVertex(t *testing.T) {
	phys := &ScanResult{Schema: "s", Tables: []string{"a", "b", "c"},
		FKs: []FK{physFK("fk_b_a", "b", "a_id", "a", "CASCADE", true)}}
	logic := &ScanResult{Schema: "yii1:s", Tables: []string{"a", "b", "c"},
		FKs: []FK{yiiFK("c", "a_id", "a", "C.php:8")}}
	ci := PrepareComparison(MergeScans(phys, logic), 0, 0, true)
	if got := ci.View(GraphLogical).Analysis.Isolated; len(got) != 0 {
		t.Errorf("logical: 縮約メンバーが孤立として数えられた: %v", got)
	}
	if got := ci.View(GraphPhysical).Analysis.Isolated; !reflect.DeepEqual(got, []string{"c"}) {
		t.Errorf("physical の孤立: %v", got)
	}
}

func TestPrepareComparisonServices(t *testing.T) {
	sc := MergeScans(&ScanResult{Schema: "p", Tables: partitionFixture().Tables, FKs: func() []FK {
		fks := append([]FK(nil), partitionFixture().FKs...)
		for i := range fks {
			stampPhysical(&fks[i])
		}
		return fks
	}()}, &ScanResult{Schema: "yii1:p"})
	free := PrepareComparison(sc, 5, 0, true).View(GraphCombined).Analysis.Partition
	two := PrepareComparison(sc, 5, 2, true).View(GraphCombined).Analysis.Partition
	if len(free.Levels) > 1 && len(two.Groups) != 2 {
		t.Errorf("--services 2 が効いていない: %d 個(階段 %d 段)", len(two.Groups), len(free.Levels))
	}
}
