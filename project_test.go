package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestProjectFKs(t *testing.T) {
	sc := MergeScans(mergeFixture())

	t.Run("physical: DB が強制する FK だけ", func(t *testing.T) {
		idx := fkIndex(ProjectFKs(sc.FKs, GraphPhysical))
		if len(idx) != 2 {
			t.Fatalf("2 本を期待: %v", idx)
		}
		for key, fk := range idx {
			if !fk.Enforced() {
				t.Errorf("%s: 強制されていない FK が混ざった", key)
			}
		}
	})

	t.Run("logical: 宣言の全集合(DB にもある関係を含む)を、宣言の属性で", func(t *testing.T) {
		idx := fkIndex(ProjectFKs(sc.FKs, GraphLogical))
		if len(idx) != 3 {
			t.Fatalf("3 本を期待: %v", idx)
		}
		fk := idx["tbl_comment(post_id)→tbl_post"]
		// 合流後は physical の CASCADE / NOT NULL になっているが、宣言はそう言っていない
		if fk.DeleteRule != "NO ACTION" || fk.AllNotNull || fk.Nullability() != NullableUnknown {
			t.Errorf("DB の属性が宣言のグラフに漏れた: %+v", fk)
		}
		if fk.Enforced() || fk.Constraint != "yii1:comment→post" {
			t.Errorf("physical の証拠・constraint 名が残っている: %+v", fk)
		}
		if w, reason := weightOf(fk); w != 1 || reason != ReasonUnknown {
			t.Errorf("重み: %v %s", w, reason)
		}
	})

	t.Run("combined: 全部", func(t *testing.T) {
		if got := ProjectFKs(sc.FKs, GraphCombined); len(got) != 4 {
			t.Errorf("4 本を期待: %d", len(got))
		}
	})

	t.Run("射影は元の FK を書き換えない", func(t *testing.T) {
		before := fkIndex(sc.FKs)["tbl_comment(post_id)→tbl_post"]
		_ = ProjectFKs(sc.FKs, GraphLogical)
		after := fkIndex(sc.FKs)["tbl_comment(post_id)→tbl_post"]
		if !reflect.DeepEqual(before, after) {
			t.Errorf("元が変わった:\n%+v\n%+v", before, after)
		}
	})
}

// Logical の見方は DB の整備状況に依存しない。同じ宣言に対して、FK が
// 1 本も無い DB と一部に張ってある DB のどちらと合流させても、Logical 射影は同じ。
func TestLogicalProjectionIgnoresPhysicalState(t *testing.T) {
	physB, logic := mergeFixture()
	physA := &ScanResult{Schema: physB.Schema, Dialect: physB.Dialect, Tables: physB.Tables}
	a := ProjectFKs(MergeScans(physA, logic).FKs, GraphLogical)
	b := ProjectFKs(MergeScans(physB, logic).FKs, GraphLogical)
	if !reflect.DeepEqual(fkIndex(a), fkIndex(b)) {
		t.Errorf("Logical 射影が DB の状態で変わった:\nA: %+v\nB: %+v", a, b)
	}
}

func TestProjectScan(t *testing.T) {
	sc := MergeScans(mergeFixture())

	p := Project(sc, GraphPhysical, true)
	if !reflect.DeepEqual(p.Tables, sc.Tables) {
		t.Errorf("頂点集合が変わった: %v", p.Tables)
	}
	if len(p.Suspects) != 0 {
		t.Errorf("physical に静的ソース由来の疑いが残っている: %+v", p.Suspects)
	}
	if len(sc.Suspects) == 0 {
		t.Error("元の Suspects を消してしまった")
	}
	a := Analyze(p, 0)
	// tbl_post → tbl_user は宣言だけ。physical では tbl_user 側に audit の FK しか残らない
	want := "post_tag,tag"
	if got := strings.Join(a.Isolated, ","); got != want {
		t.Errorf("physical の孤立 = %s, want %s", got, want)
	}

	if l := Project(sc, GraphLogical, false); !l.CoocNoWeight {
		t.Error("includeObserved=false が共起の算入を止めていない")
	}
}

func TestProjectEmptyIsNotAbsence(t *testing.T) {
	sc, err := ScanYii1("testdata/yii1app")
	if err != nil {
		t.Fatal(err)
	}
	p := Project(sc, GraphPhysical, true)
	if len(p.FKs) != 0 {
		t.Fatalf("DB を読んでいないのに physical FK がある: %+v", p.FKs)
	}
	if !strings.Contains(strings.Join(p.Notes, "\n"), "「関係が無い」の確認ではない") {
		t.Errorf("空の physical に「見ていないだけ」の注が無い: %v", p.Notes)
	}
}

func TestCLIShowEvidence(t *testing.T) {
	plain := runCLI(t, "--yii1", "testdata/yii1app", "--json")
	if bytes.Contains(plain, []byte(`"evidences"`)) || bytes.Contains(plain, []byte(`"weight_reason"`)) {
		t.Error("既定の JSON に出自のフィールドが出ている")
	}
	ev := string(runCLI(t, "--yii1", "testdata/yii1app", "--json", "--show-evidence"))
	for _, want := range []string{
		`"nullable": "unknown"`, `"enforced": false`, `"weight_reason": "unknown_provisional"`,
		`"source": "logical:yii1"`, `"origin": "protected/models/Comment.php:8"`,
	} {
		if !strings.Contains(ev, want) {
			t.Errorf("--show-evidence の JSON に %s が無い", want)
		}
	}
}

func TestCLIGraphFlag(t *testing.T) {
	t.Setenv("HABAKIRI_DSN", "")
	var stdout, stderr bytes.Buffer
	if code := run("habakiri", []string{"--yii1", "testdata/yii1app", "--graph", "both"}, &stdout, &stderr); code != 2 {
		t.Errorf("不正な --graph は exit 2: got %d", code)
	}
	// 静的ソースだけなら、logical を明示しても FK の集合は既定と同じ
	def := Analyze(mustYii1(t), 0)
	out := string(runCLI(t, "--yii1", "testdata/yii1app", "--graph", "logical", "--json"))
	if !strings.Contains(out, `"fk_count": 4`) || def.FKCount != 4 {
		t.Errorf("logical 明示で FK 数が変わった:\n%s", out)
	}
}

func mustYii1(t *testing.T) *ScanResult {
	t.Helper()
	sc, err := ScanYii1("testdata/yii1app")
	if err != nil {
		t.Fatal(err)
	}
	return sc
}
