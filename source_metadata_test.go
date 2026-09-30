package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMetadataRelations(t *testing.T) {
	rels, err := LoadMetadataRelations("testdata/metadata/relations.tsv")
	if err != nil {
		t.Fatal(err)
	}
	// 見出し・注釈・空行は飛ばす。行番号はファイルの行
	if len(rels) != 4 {
		t.Fatalf("%d 行, want 4: %+v", len(rels), rels)
	}
	if r := rels[2]; r.Child != "answers" || strings.Join(r.Cols, ",") != "form_id,form_rev" || r.Line != 5 {
		t.Errorf("複合列の行 = %+v", r)
	}

	bad := filepath.Join(t.TempDir(), "bad.tsv")
	if err := os.WriteFile(bad, []byte("a\tb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMetadataRelations(bad); err == nil || !strings.Contains(err.Error(), "bad.tsv:1") {
		t.Errorf("3 列でない行のエラー = %v", err)
	}
}

func TestAddMetadataRelations(t *testing.T) {
	sc, err := ScanYii1("testdata/yii1inherit")
	if err != nil {
		t.Fatal(err)
	}
	before := len(sc.FKs)
	rels, err := LoadMetadataRelations("testdata/metadata/relations.tsv")
	if err != nil {
		t.Fatal(err)
	}
	sc = AddMetadataRelations(sc, "relations.tsv", rels)
	idx := fkIndex(sc.FKs)
	notes := strings.Join(sc.Notes, "\n")

	t.Run("クラス名とテーブル名の両方で書ける", func(t *testing.T) {
		for key, why := range map[string]string{
			"contacts(manager_id)→contacts":   "小文字のクラス名 contact",
			"answers(form_id,form_rev)→forms": "テーブル名と複合列",
		} {
			fk, ok := idx[key]
			if !ok {
				t.Errorf("%s が無い(%s): %v", key, why, idx)
				continue
			}
			if fk.Evidences[0].Source != SourceMetadata || fk.Evidences[0].Origin == "" {
				t.Errorf("%s の証拠 = %+v", key, fk.Evidences)
			}
		}
		if len(sc.FKs) != before+2 {
			t.Errorf("FK %d → %d, want +2", before, len(sc.FKs))
		}
	})

	t.Run("宣言と同じ関係には証拠を足す", func(t *testing.T) {
		fk := idx["contacts(account_id)→accounts"]
		var srcs []string
		for _, ev := range fk.Evidences {
			srcs = append(srcs, ev.Source)
		}
		if got := strings.Join(srcs, ","); got != SourceYii1+","+SourceMetadata {
			t.Errorf("証拠の出どころ = %s", got)
		}
	})

	t.Run("解決できない名前は FK にしない", func(t *testing.T) {
		for key := range idx {
			if strings.Contains(strings.ToLower(key), "ghost") {
				t.Errorf("解決できない名前の FK を作った: %s", key)
			}
		}
		if !strings.Contains(notes, "1 行は、テーブルにもモデルのクラスにも解決できず") ||
			!strings.Contains(notes, "6 行目 Ghost → Account") {
			t.Errorf("注: %s", notes)
		}
		if !strings.Contains(notes, "3 行を読み、2 本を新しい関係として足した") {
			t.Errorf("注: %s", notes)
		}
	})

	t.Run("Coverage はメタデータ由来を分けて数える", func(t *testing.T) {
		c := BuildCoverage(sc, DiffEdges(sc, nil))
		if c.MetadataRelations != 3 || c.MetadataOnly != 2 {
			t.Errorf("metadata_relations=%d metadata_only=%d, want 3 2", c.MetadataRelations, c.MetadataOnly)
		}
	})
}

// 静的ソース無しで一覧だけを渡したときは、名前をテーブル名として扱う。
func TestAddMetadataRelationsStandalone(t *testing.T) {
	sc := AddMetadataRelations(nil, "r.tsv", []MetadataRelation{
		{Child: "orders", Parent: "users", Cols: []string{"user_id"}, Line: 1},
	})
	if _, ok := fkIndex(sc.FKs)["orders(user_id)→users"]; !ok || strings.Join(sc.Tables, ",") != "orders,users" {
		t.Errorf("fks=%v tables=%v", sc.FKs, sc.Tables)
	}
}
