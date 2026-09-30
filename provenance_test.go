package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestYii1NullableUnknown(t *testing.T) {
	sc, err := ScanYii1("testdata/yii1app")
	if err != nil {
		t.Fatal(err)
	}
	idx := fkIndex(sc.FKs)

	for key, fk := range idx {
		if fk.Nullability() != NullableUnknown {
			t.Errorf("%s: Yii1 の relations() から NULL 許容は分からないはず: %v", key, fk.Nullability())
		}
		if fk.Enforced() || !fk.Logical() {
			t.Errorf("%s: 宣言だけの関係は Enforced=false / Logical=true: %+v", key, fk.Evidences)
		}
		w, reason := weightOf(fk)
		if w != 1 || reason != ReasonUnknown {
			t.Errorf("%s: 重み 1(暫定)を期待: %v %s", key, w, reason)
		}
	}

	t.Run("両側宣言は 1 FK に Evidence 2 件", func(t *testing.T) {
		fk := idx["comment(post_id)→post"]
		if len(fk.Evidences) != 2 {
			t.Fatalf("BELONGS_TO と HAS_MANY の 2 件を期待: %+v", fk.Evidences)
		}
		// クラス名順に走査するので Comment の BELONGS_TO が先
		want := []string{"protected/models/Comment.php:8", "protected/models/Post.php:9"}
		for i, ev := range fk.Evidences {
			if ev.Source != SourceYii1 || ev.Origin != want[i] {
				t.Errorf("evidence[%d] = %+v, want origin %s", i, ev, want[i])
			}
		}
	})
}

func TestRailsEvidence(t *testing.T) {
	sc, err := ScanRails("testdata/railsapp")
	if err != nil {
		t.Fatal(err)
	}
	idx := fkIndex(sc.FKs)
	cases := []struct {
		key      string
		weight   float64
		reason   string
		nullable Nullability
	}{
		// 数値は従来どおり。理由が「DB が強制」ではなく「宣言にそうある」になる。
		{"posts(user_id)→users", 3, ReasonLogicalCascade, NullableFalse},
		{"comments(author_id)→users", 2, ReasonLogicalRequired, NullableFalse},
		{"posts(category_id)→taxonomy", 1, ReasonLogicalOptional, NullableTrue},
	}
	for _, c := range cases {
		fk, ok := idx[c.key]
		if !ok {
			t.Fatalf("%s が無い", c.key)
		}
		w, reason := weightOf(fk)
		if w != c.weight || reason != c.reason || fk.Nullability() != c.nullable {
			t.Errorf("%s: got (%v, %s, %v) want (%v, %s, %v)",
				c.key, w, reason, fk.Nullability(), c.weight, c.reason, c.nullable)
		}
		if fk.Enforced() {
			t.Errorf("%s: 静的ソースだけで Enforced になっている", c.key)
		}
		for _, ev := range fk.Evidences {
			if ev.Source != SourceRails || ev.Origin == "" {
				t.Errorf("%s: evidence が不正: %+v", c.key, ev)
			}
		}
	}

	t.Run("CASCADE の根拠(親側の dependent:)が別の証拠として残る", func(t *testing.T) {
		fk := idx["posts(user_id)→users"]
		if len(fk.Evidences) != 2 || fk.Evidences[1].DeleteRule != "CASCADE" {
			t.Fatalf("belongs_to + has_many dependent の 2 件を期待: %+v", fk.Evidences)
		}
		if fk.Evidences[0].Origin == fk.Evidences[1].Origin {
			t.Errorf("証拠位置が同じ: %+v", fk.Evidences)
		}
	})

	t.Run("concern 経由の宣言は concern のファイルを指す", func(t *testing.T) {
		fk := idx["index_entries(user_id)→users"]
		found := false
		for _, ev := range fk.Evidences {
			if strings.Contains(ev.Origin, "concerns/indexable.rb:") {
				found = true
			}
		}
		if !found {
			t.Errorf("concern の位置が証拠に無い: %+v", fk.Evidences)
		}
	})
}

func TestPhysicalEvidence(t *testing.T) {
	sc, err := LoadSchemaJSON("testdata/postgres-fixture.scan.json")
	if err != nil {
		t.Fatal(err)
	}
	idx := fkIndex(sc.FKs)
	cases := []struct {
		key      string
		weight   float64
		reason   string
		nullable Nullability
	}{
		{"order_items(order_id)→orders", 3, ReasonCascade, NullableFalse},
		{"orders(customer_id)→customers", 2, ReasonNotNull, NullableFalse},
		{"customers(region_id)→regions", 1, ReasonNullable, NullableTrue},
		// 複合 FK: 片方の列が NULL 可なら NULL 可(参照が無い状態が存在できる)
		{"pair_refs(a,b)→pairs", 1, ReasonNullable, NullableTrue},
	}
	for _, c := range cases {
		fk, ok := idx[c.key]
		if !ok {
			t.Fatalf("%s が無い: %v", c.key, idx)
		}
		w, reason := weightOf(fk)
		if w != c.weight || reason != c.reason || fk.Nullability() != c.nullable {
			t.Errorf("%s: got (%v, %s, %v)", c.key, w, reason, fk.Nullability())
		}
		if !fk.Enforced() || fk.Logical() {
			t.Errorf("%s: DB スキャン由来は Enforced=true / Logical=false", c.key)
		}
		if len(fk.Evidences) != 1 || fk.Evidences[0].Origin != fk.Constraint {
			t.Errorf("%s: origin は constraint 名: %+v", c.key, fk.Evidences)
		}
	}
}

// Evidence 導入前の構築経路(テストや外部の JSON)では Evidences も Nullable も
// 空。その場合は AllNotNull だけで従来どおりの重みになること。
func TestWeightWithoutEvidence(t *testing.T) {
	cases := []struct {
		fk   FK
		want float64
	}{
		{FK{DeleteRule: "CASCADE"}, 3},
		{FK{AllNotNull: true, DeleteRule: "NO ACTION"}, 2},
		{FK{DeleteRule: "SET NULL"}, 1},
	}
	for _, c := range cases {
		if got := fkWeight(c.fk); got != c.want {
			t.Errorf("%+v: weight %v, want %v", c.fk, got, c.want)
		}
	}
}

func TestNullabilityJSON(t *testing.T) {
	raw, err := json.Marshal(Evidence{Source: SourceYii1, Origin: "a.php:1", Nullable: NullableUnknown})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"source":"logical:yii1","origin":"a.php:1","nullable":"unknown"}`; string(raw) != want {
		t.Errorf("got %s\nwant %s", raw, want)
	}
	var ev Evidence
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Nullable != NullableUnknown {
		t.Errorf("往復で Unknown が落ちた: %+v %v", ev, err)
	}
}
