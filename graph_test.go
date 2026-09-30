package main

import (
	"reflect"
	"testing"
)

func fk(child, parent, rule string, notNull bool) FK {
	return FK{Constraint: "fk_" + child + "_" + parent, ChildTable: child,
		ChildCols: []string{parent + "_id"}, ParentTable: parent,
		DeleteRule: rule, AllNotNull: notNull}
}

// 2 つの三角形を 1 本で繋いだグラフ — その 1 本だけが橋。
//
//	a—b—c—a   d—e—f—d
//	     c ——— d
func TestBridgesTwoTriangles(t *testing.T) {
	fks := []FK{
		fk("a", "b", "RESTRICT", false), fk("b", "c", "RESTRICT", false), fk("c", "a", "RESTRICT", false),
		fk("d", "e", "RESTRICT", false), fk("e", "f", "RESTRICT", false), fk("f", "d", "RESTRICT", false),
		fk("c", "d", "RESTRICT", false),
	}
	edges := BuildEdges(fks)
	bridges := Bridges(edges)
	want := []Pair{mkPair("c", "d")}
	if !reflect.DeepEqual(bridges, want) {
		t.Errorf("bridges = %v, want %v", bridges, want)
	}
	blocks := Blocks(edges, bridges)
	if len(blocks) != 2 || len(blocks[0]) != 3 || len(blocks[1]) != 3 {
		t.Errorf("blocks = %v", blocks)
	}
}

// 平行 FK(同じテーブル対に 2 本)は 1 エッジに束ねられ、橋になり得る。
// 束ねなければ「平行エッジは橋ではない」という定義に食われて見落とす。
func TestParallelFKsCollapse(t *testing.T) {
	fks := []FK{
		fk("order", "user", "RESTRICT", true),
		{Constraint: "fk2", ChildTable: "order", ChildCols: []string{"updated_by"},
			ParentTable: "user", DeleteRule: "SET NULL", AllNotNull: false},
	}
	edges := BuildEdges(fks)
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	e := edges[mkPair("order", "user")]
	if len(e.FKs) != 2 || e.Weight != 3 { // NOT NULL(2) + NULLABLE(1)
		t.Errorf("FKs=%d weight=%v", len(e.FKs), e.Weight)
	}
	bridges := Bridges(edges)
	if len(bridges) != 1 {
		t.Errorf("束ねた後は橋として検出されるべき: %v", bridges)
	}
}

// 自己参照 FK は落とす。
func TestSelfReferenceDropped(t *testing.T) {
	edges := BuildEdges([]FK{fk("category", "category", "RESTRICT", false)})
	if len(edges) != 0 {
		t.Errorf("自己参照はエッジにしない: %v", edges)
	}
}

// CASCADE 縮約: a -CASCADE-> b, b — c なら {a,b} が 1 ノードに潰れ、
// c との橋が残る。a—c 間のエッジは縮約ノードへ付け替えられる。
func TestContractCascade(t *testing.T) {
	fks := []FK{
		fk("line_item", "order", "CASCADE", true), // ライフサイクル一体
		fk("order", "shipment", "RESTRICT", false),
		fk("line_item", "shipment", "RESTRICT", false),
	}
	edges := BuildEdges(fks)
	edges, groups := Contract(edges)

	if len(groups) != 1 {
		t.Fatalf("groups = %v", groups)
	}
	for root, members := range groups {
		if root != "line_item" { // 辞書順代表
			t.Errorf("root = %s", root)
		}
		if !reflect.DeepEqual(members, []string{"line_item", "order"}) {
			t.Errorf("members = %v", members)
		}
	}
	// 縮約後は {line_item} — shipment の 1 エッジ(2 本の FK が束なる)
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	e := edges[mkPair("line_item", "shipment")]
	if e == nil || len(e.FKs) != 2 {
		t.Errorf("縮約ノードへの付け替えが壊れている: %+v", edges)
	}
}

// hub 除去: スター型の中心(users)を外すと葉が孤立し、橋は消える。
func TestRemoveHubs(t *testing.T) {
	var fks []FK
	leaves := []string{"a", "b", "c", "d", "e", "f", "g"}
	for _, l := range leaves {
		fks = append(fks, fk(l, "users", "RESTRICT", true))
	}
	edges := BuildEdges(fks)
	edges, hubs := RemoveHubs(edges, 6)
	if len(hubs) != 1 || hubs[0].Node != "users" || hubs[0].Degree != 7 {
		t.Errorf("hubs = %v", hubs)
	}
	if len(edges) != 0 {
		t.Errorf("hub に繋がるエッジは消える: %v", edges)
	}
}

// 橋が無い密グラフではフォールバックが最薄エッジを返す。
func TestThinnestSeams(t *testing.T) {
	// 完全グラフ K4(橋なし)。1 本だけ NULLABLE で軽い。
	fks := []FK{
		fk("a", "b", "RESTRICT", true), fk("a", "c", "RESTRICT", true),
		fk("a", "d", "RESTRICT", true), fk("b", "c", "RESTRICT", true),
		fk("b", "d", "RESTRICT", true),
		fk("c", "d", "RESTRICT", false), // 最薄
	}
	edges := BuildEdges(fks)
	bridges := Bridges(edges)
	if len(bridges) != 0 {
		t.Fatalf("K4 に橋は無い: %v", bridges)
	}
	seams := ThinnestSeams(edges, bridges, 2)
	if len(seams) != 2 || seams[0].Pair != mkPair("c", "d") {
		t.Errorf("seams = %+v", seams)
	}
}

// Analyze の通し: 孤立テーブル・CASCADE 集約・橋がそれぞれの枠に出る。
func TestAnalyzeEndToEnd(t *testing.T) {
	sc := &ScanResult{
		Schema: "app",
		Tables: []string{"audit_log", "line_item", "order", "payment", "shipment"},
		FKs: []FK{
			fk("line_item", "order", "CASCADE", true),
			fk("payment", "order", "RESTRICT", true),
			fk("shipment", "payment", "RESTRICT", false),
		},
	}
	a := Analyze(sc, 100) // hub 判定を実質無効化
	if !reflect.DeepEqual(a.Isolated, []string{"audit_log"}) {
		t.Errorf("isolated = %v", a.Isolated)
	}
	if len(a.CascadeGroups) != 1 {
		t.Errorf("cascade groups = %v", a.CascadeGroups)
	}
	// 縮約後: {line_item,order} — payment — shipment の鎖 = 橋 2 本
	if len(a.Bridges) != 2 {
		t.Fatalf("bridges = %+v", a.Bridges)
	}
	// 軽い順に並ぶ: shipment—payment(NULLABLE, w=1)が先
	if a.Bridges[0].Weight != 1 || a.Bridges[1].Weight != 2 {
		t.Errorf("重み順ソートが壊れている: %+v", a.Bridges)
	}
	if a.Bridges[0].Difficulty[:len("易")] != "易" {
		t.Errorf("NULLABLE のみは易: %s", a.Bridges[0].Difficulty)
	}
}
