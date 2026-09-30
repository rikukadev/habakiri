package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func findRow(rows []EdgeDiffRow, child, parent string) *EdgeDiffRow {
	for i := range rows {
		if rows[i].ChildTable == child && rows[i].ParentTable == parent {
			return &rows[i]
		}
	}
	return nil
}

func findPair(rows []EdgeDiffRow, a, b string) *EdgeDiffRow {
	p := mkPair(a, b)
	for i := range rows {
		if rows[i].ChildTable == "" && rows[i].A == p.A && rows[i].B == p.B {
			return &rows[i]
		}
	}
	return nil
}

// 合流フィクスチャ(merge_test.go): DB は tbl_audit / tbl_comment / tbl_post / tbl_user を、
// 静的解析は comment / post / post_tag / tag / user のモデルを見ている。
func TestDiffEdgesMerged(t *testing.T) {
	sc := MergeScans(mergeFixture())
	d := DiffEdges(sc, nil)

	cases := []struct {
		child, parent     string
		physical, logical Presence
		class             string
		why               string
	}{
		{"tbl_comment", "tbl_post", Present, Present, EdgeBoth,
			"DB の FK と relations() の両方にある"},
		{"tbl_post", "tbl_user", Absent, Present, EdgeLogicalOnly,
			"DB は tbl_post を見ていて FK が無い"},
		// 静的解析は audit のモデルを見ていない。「ORM に宣言が無い」とは言えない
		{"tbl_audit", "tbl_user", Present, Unobserved, EdgeUndetermined,
			"audit のモデルは解析対象に無い"},
		// DB に post_tag / tag が無い。「DB に FK が無い」とは言えない
		{"post_tag", "tag", Unobserved, Present, EdgeUndetermined,
			"DB に該当テーブルが無い"},
	}
	for _, c := range cases {
		row := findRow(d.Edges, c.child, c.parent)
		if row == nil {
			t.Errorf("%s→%s の行が無い", c.child, c.parent)
			continue
		}
		if row.Physical != c.physical || row.Logical != c.logical || row.Class != c.class {
			t.Errorf("%s→%s: physical=%s logical=%s class=%s, want %s %s %s(%s)",
				c.child, c.parent, row.Physical, row.Logical, row.Class, c.physical, c.logical, c.class, c.why)
		}
		// --cooc を渡していないので、共起は「見ていない」
		if row.Observed != Unobserved {
			t.Errorf("%s→%s: observed=%s, want unobserved", c.child, c.parent, row.Observed)
		}
	}
	want := EdgeDiffCounts{Both: 1, LogicalOnly: 1, Undetermined: 2}
	if d.Counts != want {
		t.Errorf("counts = %+v, want %+v", d.Counts, want)
	}
	if len(d.HubEdges) != 0 {
		t.Errorf("hub を指定していないのに hub 枠に行がある: %+v", d.HubEdges)
	}
}

// hub に接する行は捨てず、別枠に同じ形で出す。
func TestDiffEdgesHubFrame(t *testing.T) {
	sc := MergeScans(mergeFixture())
	d := DiffEdges(sc, []string{"tbl_user"})

	if findRow(d.Edges, "tbl_post", "tbl_user") != nil || findRow(d.Edges, "tbl_audit", "tbl_user") != nil {
		t.Errorf("hub に接する行が通常枠に残っている: %+v", d.Edges)
	}
	row := findRow(d.HubEdges, "tbl_post", "tbl_user")
	if row == nil {
		t.Fatalf("tbl_post→tbl_user が hub 枠に無い: %+v", d.HubEdges)
	}
	if row.Class != EdgeLogicalOnly || !reflect.DeepEqual(row.Hubs, []string{"tbl_user"}) {
		t.Errorf("hub 枠の行 = %+v, want logical_only / hubs=[tbl_user]", *row)
	}
	// 枠を分けても合計は変わらない(捨てていない)
	all := DiffEdges(sc, nil)
	if got, want := len(d.Edges)+len(d.HubEdges), len(all.Edges); got != want {
		t.Errorf("行数の合計 = %d, want %d", got, want)
	}
	if d.Counts != (EdgeDiffCounts{Both: 1, Undetermined: 1}) ||
		d.HubCounts != (EdgeDiffCounts{LogicalOnly: 1, Undetermined: 1}) {
		t.Errorf("counts=%+v hub_counts=%+v", d.Counts, d.HubCounts)
	}
}

// coocFixture: DB も静的解析も orders / payments / users / logs を全部見ている。
// 宣言は orders→users(両方)だけ。
func coocFixture() *ScanResult {
	tables := []string{"logs", "orders", "payments", "users"}
	phys := &ScanResult{Schema: "app", Dialect: "mysql", Tables: tables,
		FKs: []FK{physFK("fk_orders_user", "orders", "user_id", "users", "NO ACTION", true)}}
	logic := &ScanResult{Schema: "yii1:app", Tables: tables,
		FKs: []FK{yiiFK("orders", "user_id", "users", "protected/models/Order.php:8")}}
	sc := MergeScans(phys, logic)
	sc.Cooc = &CoocData{
		TotalTx: 100,
		// orders と payments は 10 tx ずつ、いつも一緒(NPMI = 1)。
		// logs は 90 tx に出るので、orders との共起は偶然で説明が付く(NPMI ≈ 0)。
		TableTx: map[string]int{"orders": 10, "payments": 10, "users": 10, "logs": 90},
		Pairs: []CoocPair{
			{A: "orders", B: "payments", Count: 10},
			{A: "logs", B: "orders", Count: 9},
			{A: "orders", B: "users", Count: 10},
		},
	}
	return sc
}

// 宣言がどちらにも無く、共起だけがある対は observed_only。
// 高頻度テーブルとの偶発的な共起は、関係として数えない。
func TestDiffEdgesObservedOnly(t *testing.T) {
	d := DiffEdges(coocFixture(), nil)

	row := findPair(d.Edges, "orders", "payments")
	if row == nil {
		t.Fatalf("orders—payments の行が無い: %+v", d.Edges)
	}
	if row.Class != EdgeObservedOnly || row.Physical != Absent || row.Logical != Absent || row.Observed != Present {
		t.Errorf("orders—payments = %+v, want observed_only(physical/logical absent)", *row)
	}
	if row.CoocCount != 10 || row.CoocNPMI < 0.99 {
		t.Errorf("共起の実測値が載っていない: count=%d npmi=%v", row.CoocCount, row.CoocNPMI)
	}
	if got := findPair(d.Edges, "logs", "orders"); got != nil {
		t.Errorf("NPMI が低い対(logs—orders)が行になった: %+v", *got)
	}

	// 宣言のある関係にも、共起の有無は列として載る。分類は宣言の側で決まる
	fk := findRow(d.Edges, "orders", "users")
	if fk == nil || fk.Class != EdgeBoth || fk.Observed != Present {
		t.Errorf("orders→users = %+v, want both / observed=present", fk)
	}
	if d.Counts != (EdgeDiffCounts{Both: 1, ObservedOnly: 1}) {
		t.Errorf("counts = %+v", d.Counts)
	}
}

// 共起ログを渡したが共起していない関係は absent(unobserved ではない)。
func TestDiffEdgesObservedAbsent(t *testing.T) {
	sc := coocFixture()
	sc.Cooc.Pairs = sc.Cooc.Pairs[:1] // orders—payments だけ
	d := DiffEdges(sc, nil)
	fk := findRow(d.Edges, "orders", "users")
	if fk == nil || fk.Observed != Absent {
		t.Errorf("orders→users の observed = %+v, want absent", fk)
	}
}

// 単独ソース(静的解析だけ)では、DB 側は「見ていない」。
// logical_only(DB は強制していない)とも observed_only(宣言に無い)とも言わない。
func TestDiffEdgesSingleSourceStaysUndetermined(t *testing.T) {
	sc := &ScanResult{Schema: "yii1:app", Tables: []string{"orders", "payments", "users"},
		FKs: []FK{yiiFK("orders", "user_id", "users", "protected/models/Order.php:8")},
		Cooc: &CoocData{TotalTx: 100,
			TableTx: map[string]int{"orders": 10, "payments": 10},
			Pairs:   []CoocPair{{A: "orders", B: "payments", Count: 10}}}}
	d := DiffEdges(sc, nil)

	fk := findRow(d.Edges, "orders", "users")
	if fk == nil || fk.Physical != Unobserved || fk.Logical != Present || fk.Class != EdgeUndetermined {
		t.Errorf("orders→users = %+v, want physical=unobserved / undetermined", fk)
	}
	pair := findPair(d.Edges, "orders", "payments")
	if pair == nil || pair.Physical != Unobserved || pair.Logical != Absent || pair.Class != EdgeUndetermined {
		t.Errorf("orders—payments = %+v, want physical=unobserved / logical=absent / undetermined", pair)
	}

	// DB スキャンだけのときは逆(ORM 側が「見ていない」)
	db := &ScanResult{Schema: "app", Dialect: "mysql", Tables: []string{"orders", "users"},
		FKs: []FK{physFK("fk_orders_user", "orders", "user_id", "users", "NO ACTION", true)}}
	d = DiffEdges(db, nil)
	fk = findRow(d.Edges, "orders", "users")
	if fk == nil || fk.Physical != Present || fk.Logical != Unobserved || fk.Class != EdgeUndetermined {
		t.Errorf("DB だけ: orders→users = %+v, want logical=unobserved / undetermined", fk)
	}
}

// 宣言は子にも親にも書けるので、片方のモデルしか見ていなければ「宣言が無い」とは言わない。
func TestDiffEdgesLogicalNeedsBothModels(t *testing.T) {
	phys := &ScanResult{Schema: "app", Dialect: "mysql", Tables: []string{"orders", "users"},
		FKs: []FK{physFK("fk_orders_user", "orders", "user_id", "users", "NO ACTION", true)}}
	// 静的解析は orders のモデルだけを見ている(users のモデルは対象外)
	logic := &ScanResult{Schema: "yii1:app", Tables: []string{"orders"}}
	d := DiffEdges(MergeScans(phys, logic), nil)
	fk := findRow(d.Edges, "orders", "users")
	if fk == nil || fk.Logical != Unobserved || fk.Class != EdgeUndetermined {
		t.Errorf("orders→users = %+v, want logical=unobserved / undetermined", fk)
	}

	// 両方のモデルを見ていれば physical_only と言える
	logic.Tables = []string{"orders", "users"}
	d = DiffEdges(MergeScans(phys, logic), nil)
	fk = findRow(d.Edges, "orders", "users")
	if fk == nil || fk.Logical != Absent || fk.Class != EdgePhysicalOnly {
		t.Errorf("orders→users = %+v, want logical=absent / physical_only", fk)
	}
}

// 同じテーブル対でも列が違えば別の関係。片方だけ DB にあるとき、対でまとめて
// both にしない。
func TestDiffEdgesPerRelationNotPerPair(t *testing.T) {
	tables := []string{"messages", "users"}
	phys := &ScanResult{Schema: "app", Dialect: "mysql", Tables: tables,
		FKs: []FK{physFK("fk_msg_sender", "messages", "sender_id", "users", "NO ACTION", true)}}
	logic := &ScanResult{Schema: "yii1:app", Tables: tables,
		FKs: []FK{
			yiiFK("messages", "sender_id", "users", "protected/models/Message.php:8"),
			yiiFK("messages", "receiver_id", "users", "protected/models/Message.php:9"),
		}}
	d := DiffEdges(MergeScans(phys, logic), nil)
	got := map[string]string{}
	for _, r := range d.Edges {
		got[r.ChildCols[0]] = r.Class
	}
	want := map[string]string{"sender_id": EdgeBoth, "receiver_id": EdgeLogicalOnly}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("class = %v, want %v", got, want)
	}
}

// 出力は実行ごとに同じ。
func TestDiffEdgesDeterministic(t *testing.T) {
	run := func() string {
		sc := coocFixture()
		sc.Cooc.Pairs = append(sc.Cooc.Pairs, CoocPair{A: "payments", B: "users", Count: 10})
		b, err := json.Marshal(DiffEdges(sc, []string{"users"}))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	first := run()
	for i := 0; i < 50; i++ {
		if got := run(); got != first {
			t.Fatalf("実行 %d で出力が変わった:\n%s\n%s", i, first, got)
		}
	}
}
