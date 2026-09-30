package main

import (
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"testing"
)

func ariOf(t *testing.T, a, b []int) *float64 {
	t.Helper()
	verts := make([]string, len(a))
	la, lb := map[string]string{}, map[string]string{}
	for i := range a {
		v := string(rune('a' + i))
		verts[i] = v
		la[v] = string(rune('0' + a[i]))
		lb[v] = string(rune('0' + b[i]))
	}
	return adjustedRand(verts, func(s string) string { return la[s] }, func(s string) string { return lb[s] })
}

// ARI の値は手計算(対の数え上げ)と突き合わせる。
func TestAdjustedRand(t *testing.T) {
	cases := []struct {
		name string
		a, b []int
		want float64
	}{
		{"完全一致", []int{0, 0, 1, 1}, []int{0, 0, 1, 1}, 1},
		// ラベルの付け替えは結果を変えない
		{"ラベルだけ違う", []int{0, 0, 1, 1}, []int{7, 7, 3, 3}, 1},
		// sumIJ=1, sumA=2, sumB=1, C(4,2)=6 → (1 - 1/3) / (3/2 - 1/3) = 4/7
		{"片方が 1 つ割れた", []int{0, 0, 1, 1}, []int{0, 0, 1, 2}, 4.0 / 7.0},
		// sumIJ=0, sumA=sumB=2 → (0 - 2/3) / (2 - 2/3) = -1/2。偶然より悪い一致は負になる
		{"直交", []int{0, 0, 1, 1}, []int{0, 1, 0, 1}, -0.5},
		// 分母が 0 になる 2 つの場合。どちらも一致しているので 1
		{"両方とも全員 1 点ずつ", []int{0, 1, 2}, []int{4, 5, 6}, 1},
		{"両方とも全員同じ", []int{0, 0, 0}, []int{1, 1, 1}, 1},
		// 片方だけが自明な分割のときは分母が 0 にならず、0 になる(情報なし)
		{"片方だけ全員同じ", []int{0, 0, 0, 0}, []int{0, 0, 1, 1}, 0},
	}
	for _, c := range cases {
		got := ariOf(t, c.a, c.b)
		if got == nil {
			t.Errorf("%s: nil", c.name)
			continue
		}
		if math.Abs(*got-c.want) > 1e-12 {
			t.Errorf("%s: ARI = %v, want %v", c.name, *got, c.want)
		}
	}
	// 対が 1 つも作れないときは測れない。0 や 1 を返して「一致」「不一致」に見せない
	if got := ariOf(t, []int{0}, []int{0}); got != nil {
		t.Errorf("頂点 1 個: ARI = %v, want nil(判定不能)", *got)
	}
	if got := ariOf(t, nil, nil); got != nil {
		t.Errorf("頂点 0 個: ARI = %v, want nil(判定不能)", *got)
	}
}

func view(kind string, assign map[string]string, isolated []string, edges ...Pair) CommunityView {
	v := CommunityView{Kind: kind, Assign: assign, Edges: edges}
	for t := range assign {
		v.Tables = append(v.Tables, t)
	}
	v.Tables = append(v.Tables, isolated...)
	sort.Strings(v.Tables)
	return v
}

// 同じ分割同士なら、差分は何も出ない。
func TestDiffCommunitiesIdentical(t *testing.T) {
	assign := map[string]string{"orders": "orders", "items": "orders", "posts": "posts", "comments": "posts"}
	edges := []Pair{mkPair("items", "orders"), mkPair("comments", "posts"), mkPair("orders", "posts")}
	d := DiffCommunities(view("logical", assign, nil, edges...), view("logical", assign, nil, edges...))
	if d.ARI == nil || *d.ARI != 1 {
		t.Errorf("ARI = %v, want 1", d.ARI)
	}
	if len(d.Moved) != 0 || len(d.CutEdges) != 0 {
		t.Errorf("差分が出た: moved=%v cut=%v", d.Moved, d.CutEdges)
	}
	if d.CutBoth != 1 {
		t.Errorf("CutBoth = %d, want 1(orders—posts は両方で跨ぐ)", d.CutBoth)
	}
	if d.Vertices != 4 || d.Excluded != 0 {
		t.Errorf("vertices=%d excluded=%d, want 4 0", d.Vertices, d.Excluded)
	}
}

// 「片方で孤立」は「別のコミュニティへ移った」と混ぜない。FK が少ないグラフで
// 孤立して見えるのは、関係が無いと確認できたからではなく、入力に現れなかった
// だけのことがある。
func TestDiffCommunitiesKeepsIsolationApartFromReassignment(t *testing.T) {
	// A(物理 FK が薄い): comments と tags が孤立。shipments は orders 側
	a := view("physical",
		map[string]string{"orders": "orders", "items": "orders", "shipments": "orders", "posts": "posts"},
		[]string{"comments", "tags"},
		mkPair("items", "orders"), mkPair("orders", "shipments"))
	// B(宣言あり): comments は posts へ。shipments は posts 側へ移っている。tags は両方で孤立
	b := view("logical",
		map[string]string{"orders": "orders", "items": "orders", "shipments": "posts", "posts": "posts", "comments": "posts"},
		[]string{"tags"},
		mkPair("items", "orders"), mkPair("orders", "shipments"), mkPair("comments", "posts"))

	d := DiffCommunities(a, b)

	wantMoved := []MovedTable{
		{Table: "comments", To: "posts", Kind: MoveIsolatedInA},
		{Table: "shipments", From: "orders", To: "posts", Kind: MoveReassigned},
	}
	if !reflect.DeepEqual(d.Moved, wantMoved) {
		t.Errorf("moved =\n %+v\nwant\n %+v", d.Moved, wantMoved)
	}
	if d.A.Isolated != 2 || d.B.Isolated != 1 {
		t.Errorf("isolated A=%d B=%d, want 2 1", d.A.Isolated, d.B.Isolated)
	}
	// 両方で孤立している tags は、どちらでも「1 点だけ」で一致している。移動ではない
	for _, m := range d.Moved {
		if m.Table == "tags" {
			t.Errorf("両方で孤立している tags が moved に出た: %+v", m)
		}
	}
	if d.ARI == nil || *d.ARI >= 1 {
		t.Errorf("ARI = %v, want < 1", d.ARI)
	}
	// 孤立を除いた 4 頂点(orders items shipments posts)でも shipments の移動で 1 未満
	if d.ConnectedVertices != 4 || d.ARIConnected == nil || *d.ARIConnected >= 1 {
		t.Errorf("connected=%d ARIConnected=%v, want 4 と 1 未満", d.ConnectedVertices, d.ARIConnected)
	}

	// 辺: orders—shipments は A では内側、B では跨ぐ。comments—posts は A に辺が無い
	// (= A では cut でも internal でもない)ので、跨ぎの差分には出さない
	wantCut := []CutEdgeDiff{{A: "orders", B: "shipments", StateA: EdgeInternal, StateB: EdgeCut}}
	if !reflect.DeepEqual(d.CutEdges, wantCut) {
		t.Errorf("cut edges =\n %+v\nwant\n %+v", d.CutEdges, wantCut)
	}
}

// 片方にしか辺が無く、そこで跨いでいるときは absent として出す
// (「もう片方では同じコミュニティ」とは言わない)。
func TestDiffCommunitiesCutEdgeAbsentOnOtherSide(t *testing.T) {
	assign := map[string]string{"orders": "orders", "items": "orders", "posts": "posts"}
	a := view("physical", assign, nil, mkPair("items", "orders"))
	b := view("combined", assign, nil, mkPair("items", "orders"), mkPair("orders", "posts"))
	d := DiffCommunities(a, b)
	want := []CutEdgeDiff{{A: "orders", B: "posts", StateA: EdgeAbsent, StateB: EdgeCut}}
	if !reflect.DeepEqual(d.CutEdges, want) {
		t.Errorf("cut edges = %+v, want %+v", d.CutEdges, want)
	}
	if d.ARI == nil || *d.ARI != 1 {
		t.Errorf("ARI = %v, want 1(分割は同じ、辺だけが違う)", d.ARI)
	}
}

// 片方で hub 扱いのテーブルは比較から外し、外した数を報告する。
func TestDiffCommunitiesExcludesNonCommonVertices(t *testing.T) {
	a := view("physical", map[string]string{"orders": "orders", "items": "orders"}, nil)
	b := view("logical", map[string]string{"orders": "orders", "items": "orders", "users": "orders"}, nil,
		mkPair("orders", "users"))
	d := DiffCommunities(a, b)
	if d.Vertices != 2 || d.Excluded != 1 {
		t.Errorf("vertices=%d excluded=%d, want 2 1", d.Vertices, d.Excluded)
	}
	if len(d.CutEdges) != 0 || d.CutBoth != 0 {
		t.Errorf("共通頂点の外へ出る辺が数えられた: %+v both=%d", d.CutEdges, d.CutBoth)
	}
}

// 合流(A の 2 つが B で 1 つに)は、重なりが大きい側を対応とし、小さい側の
// テーブルを「移った」として出す。同点は名前の辞書順で決まる。
func TestMatchCommunitiesMerge(t *testing.T) {
	a := view("physical", map[string]string{
		"o1": "orders", "o2": "orders", "o3": "orders", "p1": "posts", "p2": "posts"}, nil)
	b := view("logical", map[string]string{
		"o1": "all", "o2": "all", "o3": "all", "p1": "all", "p2": "all"}, nil)
	d := DiffCommunities(a, b)
	wantMatch := []CommunityMatch{{A: "orders", B: "all", Overlap: 3}}
	if !reflect.DeepEqual(d.Matches, wantMatch) {
		t.Errorf("matches = %+v, want %+v", d.Matches, wantMatch)
	}
	var moved []string
	for _, m := range d.Moved {
		moved = append(moved, m.Table)
	}
	if !reflect.DeepEqual(moved, []string{"p1", "p2"}) {
		t.Errorf("moved = %v, want [p1 p2]", moved)
	}
}

// 実際の解析結果から展開する。hub は外れ、縮約は解かれ、全テーブルに所属が付く。
func TestCommunityViewOfExpandsUnitsToTables(t *testing.T) {
	a := Analyze(partitionFixture(), 5)
	v := CommunityViewOf("physical", a)

	if _, ok := v.Assign["users"]; ok {
		t.Errorf("hub(users)に所属が付いた")
	}
	for _, tb := range []string{"badges", "comments", "invoices", "items", "orders", "post_likes", "posts", "shipments"} {
		if _, ok := v.Assign[tb]; !ok {
			t.Errorf("%s に所属が無い(assign=%v)", tb, v.Assign)
		}
	}
	if v.Assign["items"] != v.Assign["orders"] || v.Assign["comments"] != v.Assign["posts"] {
		t.Errorf("塊が割れている: %v", v.Assign)
	}
	if v.Assign["orders"] == v.Assign["posts"] {
		t.Errorf("orders 系と posts 系が同じコミュニティ: %v", v.Assign)
	}
	for _, e := range v.Edges {
		if e.A == "users" || e.B == "users" {
			t.Errorf("hub への辺が入っている: %+v", e)
		}
	}
	// 自分自身との比較は差分なし
	d := DiffCommunities(v, CommunityViewOf("physical", Analyze(partitionFixture(), 5)))
	if d.ARI == nil || *d.ARI != 1 || len(d.Moved) != 0 || len(d.CutEdges) != 0 {
		t.Errorf("同じ入力の比較に差分: ari=%v moved=%v cut=%v", d.ARI, d.Moved, d.CutEdges)
	}
}

// FK を減らした入力(物理 FK が薄いスキーマ)と比べると、減らした分が
// 「孤立」として出て、ARI が 1 を割る。#24 の受け入れ条件の形。
func TestDiffCommunitiesThinnerSchema(t *testing.T) {
	full := partitionFixture()
	thin := partitionFixture()
	var kept []FK
	for _, fk := range thin.FKs {
		// posts 系の FK を全部落とす(posts / comments / post_likes が孤立する)
		if fk.ParentTable == "posts" || fk.ChildTable == "posts" || fk.ChildTable == "post_likes" {
			continue
		}
		kept = append(kept, fk)
	}
	thin.FKs = kept

	d := DiffCommunities(CommunityViewOf("A", Analyze(thin, 5)), CommunityViewOf("B", Analyze(full, 5)))
	if d.ARI == nil || *d.ARI >= 1 {
		t.Fatalf("ARI = %v, want < 1", d.ARI)
	}
	if d.A.Isolated <= d.B.Isolated {
		t.Errorf("isolated A=%d B=%d, want A > B", d.A.Isolated, d.B.Isolated)
	}
	for _, m := range d.Moved {
		if m.Kind == MoveReassigned {
			t.Errorf("FK を落としただけなのに reassigned が出た: %+v", m)
		}
	}
	// 孤立を除けば分割は一致している = 差は観測の薄さだけに由来する
	if d.ARIConnected == nil || *d.ARIConnected != 1 {
		t.Errorf("ARIConnected = %v, want 1", d.ARIConnected)
	}
}

// 出力は実行ごとに同じ(map 順に依存しない)。
func TestDiffCommunitiesDeterministic(t *testing.T) {
	run := func() string {
		full, thin := partitionFixture(), partitionFixture()
		thin.FKs = thin.FKs[:6]
		d := DiffCommunities(CommunityViewOf("A", Analyze(thin, 5)), CommunityViewOf("B", Analyze(full, 5)))
		b, err := json.Marshal(d)
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
