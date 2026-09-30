package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 合成スキーマ A/B — 主張 → 実験 → 期待。
//
//	主張: DB の FK 整備状況の差は、FK グラフの分割結果を業務結合ではなく
//	      整備進捗の写しに歪める
//	実験: 同じ Yii1 の宣言(testdata/multisource/protected/models)に対して
//	      A = FK がほぼ無い DB、B = 注文まわりにだけ FK を張った DB
//	期待: Physical は A と B で分割が違う / Logical は同じ / Combined は
//	      B で足した FK の証拠を辿れる
//
// フィクスチャの *.scan.json は *.sql を実 Postgres に流して --dump-schema
// したもの(一致は e2e/postgres.sh が見る)。
func loadAB(t *testing.T, name string) *ComparisonInput {
	t.Helper()
	phys, err := LoadSchemaJSON("testdata/multisource/" + name + ".scan.json")
	if err != nil {
		t.Fatal(err)
	}
	logic, err := ScanYii1("testdata/multisource")
	if err != nil {
		t.Fatal(err)
	}
	return PrepareComparison(MergeScans(phys, logic), 0, 0, true)
}

func abDiff(a, b *ComparisonInput, kind GraphKind) CommunityDiff {
	return DiffCommunities(
		CommunityViewOf("A", a.View(kind).Analysis),
		CommunityViewOf("B", b.View(kind).Analysis))
}

func TestSchemaABPhysicalFollowsFKCoverage(t *testing.T) {
	a, b := loadAB(t, "a"), loadAB(t, "b")

	t.Run("前提: A と B は FK の有無だけが違う", func(t *testing.T) {
		if !reflect.DeepEqual(a.Common.Tables, b.Common.Tables) {
			t.Fatalf("テーブル集合が違う:\n%v\n%v", a.Common.Tables, b.Common.Tables)
		}
		if na, nb := len(a.View(GraphPhysical).Scan.FKs), len(b.View(GraphPhysical).Scan.FKs); na != 1 || nb != 10 {
			t.Fatalf("物理 FK は A=1 / B=10 のはず: %d / %d", na, nb)
		}
	})

	t.Run("Physical: 分割が違う(ARI < 1)", func(t *testing.T) {
		d := abDiff(a, b, GraphPhysical)
		if d.ARI == nil || *d.ARI >= 1 {
			t.Fatalf("ARI < 1 を期待: %v", d.ARI)
		}
		// A ではほぼ全部が孤立。B では FK を張った範囲だけ形が出る
		if d.A.Isolated <= d.B.Isolated {
			t.Errorf("A の方が孤立が多いはず: A=%d B=%d", d.A.Isolated, d.B.Isolated)
		}
	})

	t.Run("Logical: 同じ宣言なので同じ分割(ARI = 1)", func(t *testing.T) {
		d := abDiff(a, b, GraphLogical)
		if d.ARI == nil || *d.ARI != 1 {
			t.Fatalf("ARI = 1 を期待: %v(moved %+v)", d.ARI, d.Moved)
		}
		if len(d.Moved) != 0 || len(d.CutEdges) != 0 {
			t.Errorf("所属や跨ぐ辺に差がある: %+v %+v", d.Moved, d.CutEdges)
		}
		// 比較モードの共通条件を外した単独の Logical でも、FK の集合は同じ
		la := fkIndex(ProjectFKs(a.Combined.FKs, GraphLogical))
		lb := fkIndex(ProjectFKs(b.Combined.FKs, GraphLogical))
		if !reflect.DeepEqual(la, lb) {
			t.Errorf("Logical の FK 集合が DB の状態で変わった")
		}
	})

	t.Run("B の Physical は業務の塊ではなく整備済みの範囲を写す", func(t *testing.T) {
		phys := CommunityViewOf("physical", b.View(GraphPhysical).Analysis)
		logic := CommunityViewOf("logical", b.View(GraphLogical).Analysis)
		d := DiffCommunities(phys, logic)
		if d.ARI == nil || *d.ARI >= 1 {
			t.Fatalf("B の中で physical と logical の分割は一致しないはず: %v", d.ARI)
		}
		// 記事まわりは FK が無いので、Physical では「結合が弱い」のではなく見えていない
		for _, tbl := range []string{"article", "comment", "attachment"} {
			if _, ok := phys.Assign[tbl]; ok {
				t.Errorf("%s: FK の無い領域が physical でコミュニティに入っている", tbl)
			}
			if _, ok := logic.Assign[tbl]; !ok {
				t.Errorf("%s: logical では所属があるはず", tbl)
			}
		}
	})

	t.Run("Combined: B で足した FK の証拠を辿れる", func(t *testing.T) {
		idxA, idxB := fkIndex(a.Combined.FKs), fkIndex(b.Combined.FKs)
		key := "purchase(customer_id)→customer"
		if fk := idxA[key]; fk.Enforced() || !fk.Logical() || fk.Nullability() != NullableUnknown {
			t.Errorf("A: 宣言だけの関係のはず: %+v", fk)
		}
		fk := idxB[key]
		if !fk.Enforced() || !fk.Logical() || fk.Nullability() != NullableFalse {
			t.Fatalf("B: DB と宣言の両方にある関係のはず: %+v", fk)
		}
		if fk.Evidences[0].Source != SourcePhysical || fk.Evidences[0].Origin != "purchase_customer_id_fkey" {
			t.Errorf("B: physical の証拠位置: %+v", fk.Evidences)
		}
		if _, reason := weightOf(fk); reason != ReasonNotNull {
			t.Errorf("B: 重みの理由は DB の NOT NULL: %s", reason)
		}
	})

	t.Run("Edge Diff: Physical Only / Logical Only / 判定不能が正しく分かれる", func(t *testing.T) {
		class := func(ci *ComparisonInput, child, parent string) string {
			ed := BuildComparison(ci).EdgeDiff
			for _, r := range append(append([]EdgeDiffRow(nil), ed.Edges...), ed.HubEdges...) {
				if r.ChildTable == child && r.ParentTable == parent {
					return r.Class
				}
			}
			return "(行なし)"
		}
		cases := []struct {
			child, parent, inA, inB string
		}{
			// 宣言はあるが DB に FK が無い(記事まわりは A / B とも未整備)
			{"comment", "article", EdgeLogicalOnly, EdgeLogicalOnly},
			// B で FK を張った関係: A では Logical Only、B では一致
			{"purchase", "customer", EdgeLogicalOnly, EdgeBoth},
			// 両端にモデルがあるのに宣言が無い物理 FK(B にだけある)
			{"payment", "customer", "(行なし)", EdgePhysicalOnly},
			// audit_log にはモデルが無い。静的解析が見ていないテーブルについて
			// 「宣言が無い」とは言えないので、Physical Only ではなく判定不能
			{"audit_log", "account", "(行なし)", EdgeUndetermined},
		}
		for _, c := range cases {
			if got := class(a, c.child, c.parent); got != c.inA {
				t.Errorf("A: %s → %s = %s, want %s", c.child, c.parent, got, c.inA)
			}
			if got := class(b, c.child, c.parent); got != c.inB {
				t.Errorf("B: %s → %s = %s, want %s", c.child, c.parent, got, c.inB)
			}
		}
	})
}

// Coverage: A の物理 FK 保有率が B より低いことが数値で出る。宣言側の数字は同じ。
func TestSchemaABCoverage(t *testing.T) {
	ca, cb := BuildComparison(loadAB(t, "a")).Coverage, BuildComparison(loadAB(t, "b")).Coverage
	if ca.PhysicalFKRate == nil || cb.PhysicalFKRate == nil || *ca.PhysicalFKRate >= *cb.PhysicalFKRate {
		t.Fatalf("A の保有率 < B の保有率 を期待: %v / %v", ca.PhysicalFKRate, cb.PhysicalFKRate)
	}
	if ca.TablesWithPhysicalFK != 2 || cb.TablesWithPhysicalFK != 9 || ca.DBTables != 15 || cb.DBTables != 15 {
		t.Errorf("物理 FK に関わるテーブル: A=%d B=%d / DB テーブル: A=%d B=%d",
			ca.TablesWithPhysicalFK, cb.TablesWithPhysicalFK, ca.DBTables, cb.DBTables)
	}
	if ca.Relations != cb.Relations || ca.RelationsDeclared != cb.RelationsDeclared || ca.LogicalTables != cb.LogicalTables {
		t.Errorf("宣言側の数字が A と B で違う: %+v / %+v", ca.Coverage, cb.Coverage)
	}
	if ca.Both != 1 || cb.Both != 8 || cb.PhysicalOnly != 1 || cb.Undetermined != 1 {
		t.Errorf("分類の件数: A both=%d / B both=%d physical_only=%d undetermined=%d",
			ca.Both, cb.Both, cb.PhysicalOnly, cb.Undetermined)
	}

	// B: FK を張っていない記事側のグループにだけ「入力が薄い」旗が立つ
	thin := map[string]bool{}
	for _, g := range cb.Groups {
		thin[g.Name] = g.Thin
	}
	if len(cb.Groups) != 2 || !thin["account 圏"] || thin["purchase 圏"] {
		t.Errorf("B の旗: %+v", cb.Groups)
	}
}

// CLI 経路: --compare-graphs は併用が前提。
func TestCLICompareGraphs(t *testing.T) {
	out := string(runCLI(t, "--schema-json", "testdata/multisource/b.scan.json",
		"--yii1", "testdata/multisource", "--compare-graphs"))
	for _, want := range []string{
		"■ グラフ比較(--compare-graphs)",
		"■ Observation Coverage",
		"物理 FK に関わるテーブル   9(保有率 60%)",
		"⚠ 入力が薄い",
		"hub = account, purchase",
		"physical × logical: ARI",
		"Physical Only(DB の制約はあるが ORM に宣言が無い",
		"payment.customer_id → customer",
		"(physical では孤立)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
}

func TestCLICompareGraphsNeedsBothSources(t *testing.T) {
	t.Setenv("HABAKIRI_DSN", "")
	var stdout, stderr strings.Builder
	code := run("habakiri", []string{"--yii1", "testdata/multisource", "--compare-graphs"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "併用が前提") {
		t.Errorf("片方だけの --compare-graphs は exit 2: got %d %s", code, stderr.String())
	}
}

// 共起(Observed)付きの比較。宣言にも DB にも無い結合が Observed Only に出る。
func TestCLICompareGraphsWithCooc(t *testing.T) {
	args := []string{"--schema-json", "testdata/multisource/b.scan.json", "--yii1", "testdata/multisource",
		"--compare-graphs", "--cooc", "testdata/multisource/cooc.txt"}
	out := string(runCLI(t, args...))
	for _, want := range []string{
		"Observed Only(実行時に共起したが宣言が無い",
		"article × product  [共起 ×3 npmi=0.42]",
		// FK は無いが、実行時には一緒に書かれている(Logical Only + 共起)
		"article_tag.article_id → article  [共起 ×2 npmi=0.32]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}

	// JSON は既存構造へのフィールド追加のみ(comparison が増えるだけ)
	var got struct {
		FKCount    int `json:"fk_count"`
		Comparison struct {
			Graphs   map[string]json.RawMessage `json:"graphs"`
			EdgeDiff struct {
				Counts EdgeDiffCounts `json:"counts"`
			} `json:"edge_diff"`
			CommunityDiff struct {
				Pairs []CommunityDiff `json:"pairs"`
			} `json:"community_diff"`
		} `json:"comparison"`
	}
	if err := json.Unmarshal(runCLI(t, append(args, "--json")...), &got); err != nil {
		t.Fatal(err)
	}
	if got.FKCount != 20 || len(got.Comparison.Graphs) != 3 || len(got.Comparison.CommunityDiff.Pairs) != 3 {
		t.Errorf("comparison の形: fk=%d graphs=%d pairs=%d",
			got.FKCount, len(got.Comparison.Graphs), len(got.Comparison.CommunityDiff.Pairs))
	}
	if c := got.Comparison.EdgeDiff.Counts; c.ObservedOnly != 2 || c.PhysicalOnly != 1 || c.LogicalOnly != 5 {
		t.Errorf("edge_diff.counts: %+v", c)
	}

	// 比較を付けない JSON には comparison が出ない
	plain := runCLI(t, "--schema-json", "testdata/multisource/b.scan.json", "--yii1", "testdata/multisource", "--json")
	if strings.Contains(string(plain), `"comparison"`) {
		t.Error("--compare-graphs 無しの JSON に comparison が出ている")
	}
}

// --html の比較節。自己完結(外部参照・script なし)を保つこと。
func TestCLICompareGraphsHTML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.html")
	runCLI(t, "--schema-json", "testdata/multisource/b.scan.json", "--yii1", "testdata/multisource",
		"--compare-graphs", "--html", path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	for _, want := range []string{"グラフ比較", "Edge Diff", "Community Diff", "payment.customer_id → customer"} {
		if !strings.Contains(page, want) {
			t.Errorf("HTML に %q が無い", want)
		}
	}
	// xmlns の URL は名前空間であって取得先ではないので、読みに行く属性だけを見る
	for _, banned := range []string{"<script", `src="http`, `href="http`, "@import"} {
		if strings.Contains(page, banned) {
			t.Errorf("HTML に %q がある(自己完結・JS なしの方針に反する)", banned)
		}
	}
}

// #50: その見方の入力がテーブル自体を見ていないときは「孤立」ではなく「未観測」。
func TestCommunityDiffUnobserved(t *testing.T) {
	t.Run("DB に無いテーブルは physical で未観測", func(t *testing.T) {
		// mergeFixture: 宣言側の post_tag / tag は DB に無い
		c := BuildComparison(PrepareComparison(MergeScans(mergeFixture()), 0, 0, true))
		g := c.Graphs["physical"]
		if strings.Join(g.Unobserved, ",") != "post_tag,tag" {
			t.Errorf("physical の未観測: %v(孤立: %v)", g.Unobserved, g.Isolated)
		}
		for _, tbl := range g.Isolated {
			if tbl == "post_tag" || tbl == "tag" {
				t.Errorf("DB が見ていない %s を孤立と数えた", tbl)
			}
		}
		for _, d := range c.CommunityDiff.Pairs {
			for _, m := range d.Moved {
				if (m.Table == "post_tag" || m.Table == "tag") && d.A.Kind == "physical" && m.Kind != MoveUnobservedInA {
					t.Errorf("%s × %s: %s の移動の種類 = %s", d.A.Kind, d.B.Kind, m.Table, m.Kind)
				}
			}
			if d.A.Kind == "physical" && d.A.Unobserved != 2 {
				t.Errorf("%s × %s: physical の未観測数 = %d", d.A.Kind, d.B.Kind, d.A.Unobserved)
			}
		}
	})

	t.Run("モデルの無いテーブルは logical で未観測、DB が見た FK の無いテーブルは physical で孤立", func(t *testing.T) {
		c := BuildComparison(loadAB(t, "b"))
		if l := c.Graphs["logical"]; strings.Join(l.Unobserved, ",") != "audit_log" || len(l.Isolated) != 0 {
			t.Errorf("logical: 未観測 %v / 孤立 %v", l.Unobserved, l.Isolated)
		}
		if p := c.Graphs["physical"]; len(p.Unobserved) != 0 || len(p.Isolated) != 6 {
			t.Errorf("physical: 未観測 %v / 孤立 %v", p.Unobserved, p.Isolated)
		}
		out := string(runCLI(t, "--schema-json", "testdata/multisource/b.scan.json", "--yii1", "testdata/multisource", "--compare-graphs"))
		if !strings.Contains(out, "audit_log: (logical では未観測) → account 圏") {
			t.Errorf("text に未観測の移動が無い:\n%s", out)
		}
	})
}
