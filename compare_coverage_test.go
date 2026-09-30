package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func rate(t *testing.T, p *float64) float64 {
	t.Helper()
	if p == nil {
		t.Fatalf("rate が nil")
	}
	return *p
}

// 合流フィクスチャ(merge_test.go): DB は 4 テーブル(audit / comment / post / user)、
// 物理 FK は comment→post と audit→user。静的解析は 5 モデル、宣言 3 本。
func TestBuildCoverageMerged(t *testing.T) {
	sc := MergeScans(mergeFixture())
	c := BuildCoverage(sc, DiffEdges(sc, nil))

	want := Coverage{
		PhysicalRead: true, LogicalRead: true, CoocRead: false,
		DBTables: 4, TablesWithPhysicalFK: 4, // 2 本の FK の両端で 4 テーブル全部
		LogicalTables: 5, RelationsDeclared: 3, Relations: 3, Duplicates: 0,
		Both: 1, LogicalOnly: 1, Undetermined: 2,
	}
	got := c
	got.PhysicalFKRate = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("coverage =\n %+v\nwant\n %+v", got, want)
	}
	if r := rate(t, c.PhysicalFKRate); r != 1 {
		t.Errorf("physical_fk_rate = %v, want 1", r)
	}
}

// 同じ関係を両側から宣言している(belongs_to と has_many)とき、宣言は 2 件、関係は 1 本。
func TestBuildCoverageCountsDuplicateDeclarations(t *testing.T) {
	both := FK{Constraint: "rails:comments→posts", ChildTable: "comments", ChildCols: []string{"post_id"},
		ParentTable: "posts", DeleteRule: "NO ACTION", Nullable: NullableFalse,
		Evidences: []Evidence{
			{Source: SourceRails, Origin: "app/models/comment.rb:2"},
			{Source: SourceRails, Origin: "app/models/post.rb:3"},
		}}
	single := FK{Constraint: "rails:posts→users", ChildTable: "posts", ChildCols: []string{"user_id"},
		ParentTable: "users", DeleteRule: "NO ACTION", Nullable: NullableFalse,
		Evidences: []Evidence{{Source: SourceRails, Origin: "app/models/post.rb:2"}}}
	sc := &ScanResult{Schema: "rails:app", Tables: []string{"comments", "posts", "users"}, FKs: []FK{both, single}}
	c := BuildCoverage(sc, DiffEdges(sc, nil))
	if c.RelationsDeclared != 3 || c.Relations != 2 || c.Duplicates != 1 {
		t.Errorf("declared=%d relations=%d duplicates=%d, want 3 2 1", c.RelationsDeclared, c.Relations, c.Duplicates)
	}
}

// DB を読んでいない実行の「物理 FK 0 本」は観測結果ではない。
// 0 と書かず、読んでいないことを言い、率は出さない。旗も立てない。
func TestCoverageStaticOnlyDoesNotClaimZero(t *testing.T) {
	sc := &ScanResult{Schema: "yii1:app", Tables: []string{"orders", "users"},
		FKs: []FK{yiiFK("orders", "user_id", "users", "protected/models/Order.php:8")}}
	c := BuildCoverage(sc, DiffEdges(sc, nil))
	if c.PhysicalRead || !c.LogicalRead {
		t.Errorf("physical_read=%v logical_read=%v, want false true", c.PhysicalRead, c.LogicalRead)
	}
	if c.PhysicalFKRate != nil {
		t.Errorf("physical_fk_rate = %v, want nil(DB を読んでいない)", *c.PhysicalFKRate)
	}
	view := view("logical", map[string]string{"orders": "orders", "users": "orders"}, nil)
	for _, g := range GroupCoverages(sc, view, c) {
		if g.Thin {
			t.Errorf("DB を読んでいないのに旗が立った: %+v", g)
		}
	}
}

// #24 の受け入れ条件の形: 同じ宣言に対し、A は物理 FK がほぼ無く、B は一部に足してある。
// A の保有率が B より低いことが数値で出る。
func TestCoverageSchemaAHasLowerRateThanB(t *testing.T) {
	tables := []string{"comments", "items", "orders", "posts", "users"}
	logic := func() *ScanResult {
		return &ScanResult{Schema: "yii1:app", Tables: tables, FKs: []FK{
			yiiFK("items", "order_id", "orders", "protected/models/Item.php:8"),
			yiiFK("orders", "user_id", "users", "protected/models/Order.php:8"),
			yiiFK("comments", "post_id", "posts", "protected/models/Comment.php:8"),
			yiiFK("posts", "user_id", "users", "protected/models/Post.php:8"),
		}}
	}
	physA := &ScanResult{Schema: "app", Dialect: "mysql", Tables: tables}
	physB := &ScanResult{Schema: "app", Dialect: "mysql", Tables: tables, FKs: []FK{
		physFK("fk_items_order", "items", "order_id", "orders", "CASCADE", true),
		physFK("fk_orders_user", "orders", "user_id", "users", "NO ACTION", true),
	}}
	cov := func(phys *ScanResult) Coverage {
		sc := MergeScans(phys, logic())
		return BuildCoverage(sc, DiffEdges(sc, nil))
	}
	a, b := cov(physA), cov(physB)

	if ra, rb := rate(t, a.PhysicalFKRate), rate(t, b.PhysicalFKRate); ra != 0 || rb != 0.6 {
		t.Errorf("physical_fk_rate A=%v B=%v, want 0 と 0.6(items / orders / users の 3/5)", ra, rb)
	}
	// 宣言は同じなので、宣言側の数字は A と B で変わらない
	if a.Relations != 4 || b.Relations != 4 || a.LogicalTables != b.LogicalTables {
		t.Errorf("宣言側の数字が A と B で違う: %+v / %+v", a, b)
	}
	// B で足した 2 本は both へ移り、残りは logical_only のまま
	if a.LogicalOnly != 4 || a.Both != 0 || b.LogicalOnly != 2 || b.Both != 2 {
		t.Errorf("A: both=%d logical_only=%d / B: both=%d logical_only=%d, want 0 4 / 2 2",
			a.Both, a.LogicalOnly, b.Both, b.LogicalOnly)
	}
}

// thinFixture: DB は 8 テーブル。x1..x4 は物理 FK で繋がり、y1..y4 は宣言だけで繋がる。
// 全体の保有率は 4/8 = 0.5。
func thinFixture() *ScanResult {
	tables := []string{"x1", "x2", "x3", "x4", "y1", "y2", "y3", "y4"}
	phys := &ScanResult{Schema: "app", Dialect: "mysql", Tables: tables, FKs: []FK{
		physFK("fk_x2", "x2", "x1_id", "x1", "NO ACTION", true),
		physFK("fk_x3", "x3", "x1_id", "x1", "NO ACTION", true),
		physFK("fk_x4", "x4", "x1_id", "x1", "NO ACTION", true),
	}}
	logic := &ScanResult{Schema: "yii1:app", Tables: append(append([]string(nil), tables...), "z1", "z2"), FKs: []FK{
		yiiFK("y2", "y1_id", "y1", "protected/models/Y2.php:8"),
		yiiFK("y3", "y1_id", "y1", "protected/models/Y3.php:8"),
		yiiFK("y4", "y1_id", "y1", "protected/models/Y4.php:8"),
		yiiFK("z2", "z1_id", "z1", "protected/models/Z2.php:8"),
	}}
	return MergeScans(phys, logic)
}

func TestGroupCoveragesFlagsThinGroups(t *testing.T) {
	sc := thinFixture()
	cov := BuildCoverage(sc, DiffEdges(sc, nil))
	if r := rate(t, cov.PhysicalFKRate); r != 0.5 {
		t.Fatalf("全体の保有率 = %v, want 0.5", r)
	}
	v := view("combined", map[string]string{
		"x1": "x1", "x2": "x1", "x3": "x1", "x4": "x1",
		"y1": "y1", "y2": "y1", "y3": "y1", "y4": "y1",
		"z1": "z1", "z2": "z1",
	}, nil)
	got := GroupCoverages(sc, v, cov)

	one, zero := 1.0, 0.0
	want := []GroupCoverage{
		{Name: "x1", Tables: 4, DBTables: 4, WithPhysicalFK: 4, PhysicalFKRate: &one},
		// 保有率 0 は全体(0.5)の半分を下回る
		{Name: "y1", Tables: 4, DBTables: 4, WithPhysicalFK: 0, PhysicalFKRate: &zero,
			Thin: true, ThinReason: ThinBelowAverage},
		// DB に 1 つも無い。率は出せない
		{Name: "z1", Tables: 2, DBTables: 0, Thin: true, ThinReason: ThinNotInDB},
	}
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		t.Errorf("group coverages =\n %s\nwant\n %s", gj, wj)
	}
}

// 「大きく下回る」は全体の半分未満。ちょうど半分は旗を立てない。
func TestGroupCoveragesThresholdIsStrict(t *testing.T) {
	sc := thinFixture()
	cov := BuildCoverage(sc, DiffEdges(sc, nil)) // 全体 0.5 → 閾値 0.25
	thin := func(assign map[string]string) bool {
		gs := GroupCoverages(sc, view("combined", assign, nil), cov)
		if len(gs) != 1 {
			t.Fatalf("グループ数 = %d, want 1", len(gs))
		}
		return gs[0].Thin
	}
	// 4 テーブル中 1 つ(x2)が物理 FK に関わる = 0.25。ちょうど閾値
	if thin(map[string]string{"x2": "g", "y1": "g", "y2": "g", "y3": "g"}) {
		t.Errorf("保有率 0.25(= 全体の半分ちょうど)に旗が立った")
	}
	// 5 テーブル中 1 つ = 0.2。閾値を下回る
	if !thin(map[string]string{"x2": "g", "y1": "g", "y2": "g", "y3": "g", "y4": "g"}) {
		t.Errorf("保有率 0.2(< 0.25)に旗が立たない")
	}
}

// 共起の tx 数は、ログを渡したときだけ数える。
func TestBuildCoverageCooc(t *testing.T) {
	sc := coocFixture()
	c := BuildCoverage(sc, DiffEdges(sc, nil))
	if !c.CoocRead || c.CoocTx != 100 || c.ObservedOnly != 1 {
		t.Errorf("cooc_read=%v cooc_tx=%d observed_only=%d, want true 100 1", c.CoocRead, c.CoocTx, c.ObservedOnly)
	}
}
