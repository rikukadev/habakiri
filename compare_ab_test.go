package main

import (
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

// CLI 経路: --compare-graphs は併用が前提。
func TestCLICompareGraphs(t *testing.T) {
	out := string(runCLI(t, "--schema-json", "testdata/multisource/b.scan.json",
		"--yii1", "testdata/multisource", "--compare-graphs"))
	for _, want := range []string{
		"■ グラフ比較(--compare-graphs)",
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
