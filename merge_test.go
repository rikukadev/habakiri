package main

import (
	"slices"
	"strings"
	"testing"
)

func physFK(name, child, col, parent, rule string, notNull bool) FK {
	fk := FK{Constraint: name, ChildTable: child, ChildCols: []string{col},
		ParentTable: parent, DeleteRule: rule, AllNotNull: notNull}
	stampPhysical(&fk)
	return fk
}

func yiiFK(child, col, parent, origin string) FK {
	name := "yii1:" + child + "→" + parent
	return FK{Constraint: name, ChildTable: child, ChildCols: []string{col},
		ParentTable: parent, DeleteRule: "NO ACTION", Nullable: NullableUnknown,
		Evidences: []Evidence{{Source: SourceYii1, Origin: origin, Constraint: name,
			DeleteRule: "NO ACTION", Nullable: NullableUnknown}}}
}

// mergeFixture: DB は tbl_ 接頭辞付き、宣言側は {{post}} 形式で接頭辞なし。
// DB には comment→post の FK だけがあり、post→user は宣言のみ。
func mergeFixture() (*ScanResult, *ScanResult) {
	phys := &ScanResult{Schema: "app", Dialect: "mysql",
		Tables: []string{"tbl_audit", "tbl_comment", "tbl_post", "tbl_user"},
		FKs: []FK{
			physFK("fk_comment_post", "tbl_comment", "post_id", "tbl_post", "CASCADE", true),
			physFK("fk_audit_user", "tbl_audit", "user_id", "tbl_user", "NO ACTION", false),
		}}
	logic := &ScanResult{Schema: "yii1:app",
		Tables: []string{"comment", "post", "post_tag", "tag", "user"},
		FKs: []FK{
			yiiFK("comment", "post_id", "post", "protected/models/Comment.php:8"),
			yiiFK("post", "author_id", "user", "protected/models/Post.php:8"),
			yiiFK("post_tag", "tag_id", "tag", "protected/models/Post.php:10"),
		},
		Suspects:   []Suspect{{FromTable: "post", ToTable: "comment", Strong: true}},
		FileTables: map[string]string{"protected/models/Post.php": "post"},
		Notes:      []string{yii1WeightNote},
	}
	return phys, logic
}

func TestMergeScans(t *testing.T) {
	sc := MergeScans(mergeFixture())
	idx := fkIndex(sc.FKs)

	t.Run("両方にある関係は FK 1 本・Evidence 2 件・属性は physical", func(t *testing.T) {
		fk, ok := idx["tbl_comment(post_id)→tbl_post"]
		if !ok {
			t.Fatalf("合流後の FK が無い: %v", idx)
		}
		n := 0
		for _, f := range sc.FKs {
			if f.ChildTable == "tbl_comment" && f.ParentTable == "tbl_post" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("同一関係が %d 本に分かれた", n)
		}
		if len(fk.Evidences) != 2 || fk.Evidences[0].Source != SourcePhysical || fk.Evidences[1].Source != SourceYii1 {
			t.Fatalf("evidence は physical + logical の 2 件: %+v", fk.Evidences)
		}
		if !fk.Enforced() || !fk.Logical() {
			t.Error("Enforced かつ Logical のはず")
		}
		if fk.Constraint != "fk_comment_post" || fk.DeleteRule != "CASCADE" || fk.Nullability() != NullableFalse {
			t.Errorf("属性が physical 優先になっていない: %+v", fk)
		}
		// 宣言側は「分からない」と言っていた、という記録は証拠に残る
		if fk.Evidences[1].Nullable != NullableUnknown {
			t.Errorf("logical 側の主張が消えた: %+v", fk.Evidences[1])
		}
	})

	t.Run("重みは加算しない", func(t *testing.T) {
		w, reason := weightOf(idx["tbl_comment(post_id)→tbl_post"])
		if w != 3 || reason != ReasonCascade {
			t.Errorf("physical の CASCADE = 3 のまま: %v %s", w, reason)
		}
		e := BuildEdges(sc.FKs)[mkPair("tbl_comment", "tbl_post")]
		if e == nil || e.Weight != 3 || len(e.FKs) != 1 {
			t.Errorf("束ねた辺の重みが 3 でない(証拠 2 件を足していないか): %+v", e)
		}
	})

	t.Run("宣言だけの関係は Logical Only として残る(テーブル名は DB 名に写す)", func(t *testing.T) {
		fk, ok := idx["tbl_post(author_id)→tbl_user"]
		if !ok {
			t.Fatalf("接頭辞の推定で突合できていない: %v", idx)
		}
		if fk.Enforced() || fk.Nullability() != NullableUnknown {
			t.Errorf("Logical Only は Enforced=false / Nullable=Unknown: %+v", fk)
		}
		if _, reason := weightOf(fk); reason != ReasonUnknown {
			t.Errorf("理由は unknown_provisional: %s", reason)
		}
	})

	t.Run("DB に無いテーブルの宣言も捨てない", func(t *testing.T) {
		if _, ok := idx["post_tag(tag_id)→tag"]; !ok {
			t.Fatalf("突合できない宣言が消えた: %v", idx)
		}
		found := false
		for _, n := range sc.Notes {
			if strings.Contains(n, "DB に見つからない") && strings.Contains(n, "post_tag, tag") {
				found = true
			}
		}
		if !found {
			t.Errorf("見つからないテーブルの注が無い: %v", sc.Notes)
		}
	})

	t.Run("DB にしか無い FK はそのまま", func(t *testing.T) {
		fk := idx["tbl_audit(user_id)→tbl_user"]
		if !fk.Enforced() || fk.Logical() {
			t.Errorf("Physical Only: %+v", fk.Evidences)
		}
	})

	t.Run("テーブル集合・疑い・ファイル対応も DB 名に写る", func(t *testing.T) {
		want := "post_tag,tag,tbl_audit,tbl_comment,tbl_post,tbl_user"
		if got := strings.Join(sc.Tables, ","); got != want {
			t.Errorf("tables = %s, want %s", got, want)
		}
		if s := sc.Suspects[0]; s.FromTable != "tbl_post" || s.ToTable != "tbl_comment" {
			t.Errorf("suspect が写っていない: %+v", s)
		}
		if sc.FileTables["protected/models/Post.php"] != "tbl_post" {
			t.Errorf("FileTables が写っていない: %v", sc.FileTables)
		}
	})

	t.Run("注: 単独解析向けの文言を合流向けに差し替える", func(t *testing.T) {
		joined := strings.Join(sc.Notes, "\n")
		if strings.Contains(joined, "併用を推奨") {
			t.Error("併用中に「併用を推奨」が出ている")
		}
		if !strings.Contains(joined, "unknown_provisional") || !strings.Contains(joined, `接頭辞 "tbl_"`) {
			t.Errorf("暫定値の注 / 接頭辞推定の注が無い: %v", sc.Notes)
		}
	})
}

func TestMergeDifferentColumnsStaySeparate(t *testing.T) {
	phys := &ScanResult{Schema: "app", Tables: []string{"posts", "users"},
		FKs: []FK{physFK("fk_posts_author", "posts", "author_id", "users", "NO ACTION", true)}}
	logic := &ScanResult{Schema: "yii1:app", Tables: []string{"posts", "users"},
		FKs: []FK{yiiFK("posts", "editor_id", "users", "Post.php:9")}}
	sc := MergeScans(phys, logic)
	if len(sc.FKs) != 2 {
		t.Fatalf("列が違う関係は別の FK: %+v", sc.FKs)
	}
	for _, fk := range sc.FKs {
		if len(fk.Evidences) != 1 {
			t.Errorf("証拠が混ざった: %+v", fk)
		}
	}
	// 同じテーブル対なので辺は 1 本に束ねられ、重みは FK 2 本分(NOT NULL 2 + 暫定 1)
	if e := BuildEdges(sc.FKs)[mkPair("posts", "users")]; e == nil || e.Weight != 3 {
		t.Errorf("束ね: %+v", e)
	}
}

// 1 件だけの語尾一致は偶然でありうる(item と order_item)。接頭辞とみなさない。
func TestMergeNoPrefixFromSingleCoincidence(t *testing.T) {
	phys := &ScanResult{Schema: "app", Tables: []string{"order_item", "orders"}}
	logic := &ScanResult{Schema: "yii1:app", Tables: []string{"item", "orders"},
		FKs: []FK{yiiFK("item", "order_id", "orders", "Item.php:8")}}
	sc := MergeScans(phys, logic)
	if _, ok := fkIndex(sc.FKs)["item(order_id)→orders"]; !ok {
		t.Errorf("item が order_item に写された(偽の突合): %+v", sc.FKs)
	}
}

func TestMergeCaseInsensitiveTables(t *testing.T) {
	phys := &ScanResult{Schema: "app", Tables: []string{"Post", "User"},
		FKs: []FK{physFK("fk1", "Post", "Author_ID", "User", "NO ACTION", true)}}
	logic := &ScanResult{Schema: "yii1:app", Tables: []string{"post", "user"},
		FKs: []FK{yiiFK("post", "author_id", "user", "Post.php:8")}}
	sc := MergeScans(phys, logic)
	if len(sc.FKs) != 1 || len(sc.FKs[0].Evidences) != 2 {
		t.Errorf("大文字小文字違いで突合できていない: %+v", sc.FKs)
	}
}

// CLI 経路: --schema-json(DB スキャンの写し)と --yii1 を併用できる。
// testdata/yii1app.scan.json は yii1app に対応する DB を想定した手書きのスキーマで、
// comment→post だけが物理 FK(CASCADE)、audit_log→tbl_user は宣言に無い物理 FK。
func TestCLIMergedSources(t *testing.T) {
	out := string(runCLI(t, "--schema-json", "testdata/yii1app.scan.json", "--yii1", "testdata/yii1app"))
	for _, want := range []string{
		"スキーマ blog+yii1:yii1app: 7 テーブル / 5 FK", // 宣言 4 + 物理 2 − 重複 1
		"[comment] comment, post",               // 物理 CASCADE が縮約に効く(宣言だけでは起きない)
		"unknown_provisional",                   // 暫定値の注
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
	if strings.Contains(out, "併用を推奨") {
		t.Errorf("併用中に「併用を推奨」が出ている:\n%s", out)
	}
}

func TestCLISourceExclusion(t *testing.T) {
	t.Setenv("HABAKIRI_DSN", "")
	var stdout, stderr strings.Builder
	if code := run("habakiri", []string{"--rails", "testdata/railsapp", "--yii1", "testdata/yii1app"}, &stdout, &stderr); code != 2 {
		t.Errorf("同じ系統のソース 2 つは exit 2: got %d", code)
	}
	if code := run("habakiri", []string{"--yii1", "testdata/yii1app", "--dump-schema", "x.json"}, &stdout, &stderr); code != 2 {
		t.Errorf("静的ソースだけの --dump-schema は exit 2: got %d", code)
	}
}

// famFixture: 静的ソースには族 resp_*(resp_ + ID)があり、DB にはその実体が
// resp_1・resp_2 として 2 つある。どちらも forms への FK を持つ。
func famFixture() (*ScanResult, *ScanResult) {
	phys := &ScanResult{Schema: "app", Dialect: "mysql",
		Tables: []string{"forms", "resp_1", "resp_2", "old_resp_1_2024", "resp_log"},
		FKs: []FK{
			physFK("fk_r1_form", "resp_1", "form_id", "forms", "CASCADE", true),
			physFK("fk_r2_form", "resp_2", "form_id", "forms", "CASCADE", true),
		}}
	logic := &ScanResult{Schema: "yii1:app",
		Tables:   []string{"forms", "resp_*"},
		Families: []TableFamily{{Prefix: "resp_", Models: []string{"Resp"}}},
		FKs: []FK{
			yiiFK("resp_*", "form_id", "forms", "protected/models/Resp.php:8"),
		}}
	return phys, logic
}

func TestMergeBundlesFamily(t *testing.T) {
	sc := MergeScans(famFixture())
	idx := fkIndex(sc.FKs)

	t.Run("DB の resp_<数字> を族の頂点に束ねる", func(t *testing.T) {
		for _, tbl := range sc.Tables {
			if tbl == "resp_1" || tbl == "resp_2" {
				t.Errorf("束ねたテーブル %s が残った: %v", tbl, sc.Tables)
			}
		}
		// 数字だけでない名前(退避テーブル・別の表)は束ねない
		for _, want := range []string{"old_resp_1_2024", "resp_log", "resp_*"} {
			if !slices.Contains(sc.Tables, want) {
				t.Errorf("%s が無い: %v", want, sc.Tables)
			}
		}
		if !slices.Contains(sc.PhysicalTables, "resp_*") {
			t.Errorf("族の頂点が DB から見えていない: %v", sc.PhysicalTables)
		}
	})

	t.Run("同じ関係の DB の FK は 1 本に畳み、宣言と突き合う", func(t *testing.T) {
		fk, ok := idx["resp_*(form_id)→forms"]
		if !ok {
			t.Fatalf("族の FK が無い: %v", idx)
		}
		var phys, logic int
		for _, ev := range fk.Evidences {
			if isLogicalSource(ev.Source) {
				logic++
			} else {
				phys++
			}
		}
		if phys != 2 || logic != 1 || !fk.Enforced() {
			t.Errorf("証拠 physical=%d logical=%d enforced=%v, want 2 1 true", phys, logic, fk.Enforced())
		}
		n := 0
		for _, f := range sc.FKs {
			if f.ChildTable == "resp_*" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("族の FK が %d 本(1 本に畳んでいない)", n)
		}
	})

	t.Run("束ねたことを注記する", func(t *testing.T) {
		if !strings.Contains(strings.Join(sc.Notes, "\n"), "テーブル族 resp_*: DB のテーブル 2 個(resp_1, resp_2)を 1 頂点に束ねた") {
			t.Errorf("注: %v", sc.Notes)
		}
	})
}

// 束ね方が一意に決まらなければ束ねない(族の頂点は DB から見えないまま)。
func TestMergeFamilyUndetermined(t *testing.T) {
	notesOf := func(phys, logic *ScanResult) (*ScanResult, string) {
		sc := MergeScans(phys, logic)
		return sc, strings.Join(sc.Notes, "\n")
	}

	t.Run("DB に当たるテーブルが無い", func(t *testing.T) {
		phys, logic := famFixture()
		phys.Tables, phys.FKs = []string{"forms"}, nil
		sc, notes := notesOf(phys, logic)
		if !strings.Contains(notes, "テーブル族 resp_*: DB に resp_<数字> のテーブルが無い") {
			t.Errorf("注: %s", notes)
		}
		if slices.Contains(sc.PhysicalTables, "resp_*") {
			t.Error("DB に無い族の頂点を DB が見たことにした")
		}
	})

	t.Run("具体的なモデルのテーブルと重なる", func(t *testing.T) {
		phys, logic := famFixture()
		logic.Tables = append(logic.Tables, "resp_2") // resp_2 は別のモデルが持つ
		sc, notes := notesOf(phys, logic)
		if !strings.Contains(notes, "具体的なモデルのテーブルと重なる(resp_2)") {
			t.Errorf("注: %s", notes)
		}
		if !slices.Contains(sc.Tables, "resp_1") {
			t.Error("決められないのに束ねた")
		}
	})

	t.Run("接頭辞の有無で 2 通りに当たる", func(t *testing.T) {
		// DB の接頭辞 tbl_ は静的なテーブルから推定される。resp_1 と tbl_resp_1 の両方がある
		phys := &ScanResult{Schema: "app", Dialect: "mysql",
			Tables: []string{"resp_1", "tbl_forms", "tbl_resp_1", "tbl_users"}}
		logic := &ScanResult{Schema: "yii1:app",
			Tables:   []string{"forms", "resp_*", "users"},
			Families: []TableFamily{{Prefix: "resp_", Models: []string{"Resp"}}}}
		_, notes := notesOf(phys, logic)
		if !strings.Contains(notes, "接頭辞の有無で 2 通りの候補がある") {
			t.Errorf("注: %s", notes)
		}
	})
}

// 推定した接頭辞付きの DB 名(tbl_resp_1)も族に束ねる。
func TestMergeFamilyWithInferredPrefix(t *testing.T) {
	phys := &ScanResult{Schema: "app", Dialect: "mysql",
		Tables: []string{"tbl_forms", "tbl_resp_1", "tbl_resp_2", "tbl_users"}}
	logic := &ScanResult{Schema: "yii1:app",
		Tables:   []string{"forms", "resp_*", "users"},
		Families: []TableFamily{{Prefix: "resp_", Models: []string{"Resp"}}}}
	sc := MergeScans(phys, logic)
	if slices.Contains(sc.Tables, "tbl_resp_1") || !slices.Contains(sc.PhysicalTables, "resp_*") {
		t.Errorf("tables=%v physical=%v", sc.Tables, sc.PhysicalTables)
	}
}
