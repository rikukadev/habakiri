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
		if na, nb := len(a.View(GraphPhysical).Scan.FKs), len(b.View(GraphPhysical).Scan.FKs); na != 1 || nb != 9 {
			t.Fatalf("物理 FK は A=1 / B=9 のはず: %d / %d", na, nb)
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
		// 宣言では customer と invoice は注文側。DB だけ読むと、purchase が hub で
		// 外れた後に account への FK だけが残り、account 側に引き寄せられる
		for _, tbl := range []string{"customer", "invoice"} {
			if phys.Assign[tbl] == logic.Assign[tbl] {
				t.Errorf("%s: physical と logical で同じ所属(%q)— フィクスチャが主張を示せていない", tbl, phys.Assign[tbl])
			}
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
		// ORM に宣言の無い物理 FK(Physical Only)と、DB に無い宣言(Logical Only)
		if fk := idxB["audit_log(account_id)→account"]; !fk.Enforced() || fk.Logical() {
			t.Errorf("audit_log → account は Physical Only: %+v", fk.Evidences)
		}
		if fk := idxB["comment(article_id)→article"]; fk.Enforced() || !fk.Logical() {
			t.Errorf("comment → article は Logical Only: %+v", fk.Evidences)
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
