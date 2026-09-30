package main

import (
	"strings"
	"testing"
)

func fkIndex(fks []FK) map[string]FK {
	m := map[string]FK{}
	for _, fk := range fks {
		m[fk.ChildTable+"→"+fk.ParentTable] = fk
	}
	return m
}

func TestScanRails(t *testing.T) {
	sc, err := ScanRails("testdata/railsapp")
	if err != nil {
		t.Fatal(err)
	}
	idx := fkIndex(sc.FKs)

	t.Run("belongs_to は NOT NULL 相当(Rails 5+ の既定)", func(t *testing.T) {
		fk, ok := idx["posts→users"]
		if !ok {
			t.Fatalf("posts→users が無い: %v", idx)
		}
		if !fk.AllNotNull {
			t.Error("必須 belongs_to が AllNotNull=false")
		}
		// User 側の has_many :posts, dependent: :destroy で CASCADE に昇格する
		if fk.DeleteRule != "CASCADE" {
			t.Errorf("dependent: :destroy が CASCADE に写っていない: %s", fk.DeleteRule)
		}
	})

	t.Run("optional: true は NULL可相当", func(t *testing.T) {
		fk, ok := idx["posts→taxonomy"] // Category は self.table_name = taxonomy
		if !ok {
			t.Fatalf("posts→taxonomy が無い(self.table_name が効いていない): %v", idx)
		}
		if fk.AllNotNull || fk.DeleteRule != "NO ACTION" {
			t.Errorf("optional が弱い結合に写っていない: %+v", fk)
		}
	})

	t.Run("class_name + foreign_key の明示", func(t *testing.T) {
		fk, ok := idx["comments→users"]
		if !ok {
			t.Fatalf("comments→users が無い: %v", idx)
		}
		if fk.ChildCols[0] != "author_id" {
			t.Errorf("foreign_key 指定が効いていない: %v", fk.ChildCols)
		}
	})

	t.Run("polymorphic は as: の受け手へ展開", func(t *testing.T) {
		if _, ok := idx["comments→posts"]; !ok {
			t.Errorf("commentable → Post が引けていない: %v", idx)
		}
	})

	t.Run("STI は親のテーブルに写る", func(t *testing.T) {
		for table := range idx {
			if strings.Contains(table, "admin_users") {
				t.Errorf("STI サブクラスが独自テーブルになっている: %s", table)
			}
		}
	})

	t.Run("callback の宣言外言及は注記(エッジではない)", func(t *testing.T) {
		if _, ok := idx["posts→search_entries"]; ok {
			t.Error("callback の言及がエッジになっている(注記に留めるべき)")
		}
		found := false
		for _, n := range sc.Notes {
			if strings.Contains(n, "Post") && strings.Contains(n, "SearchEntry") {
				found = true
			}
		}
		if !found {
			t.Errorf("callback 言及の注記が無い: %v", sc.Notes)
		}
	})
}

func TestScanYii1(t *testing.T) {
	sc, err := ScanYii1("testdata/yii1app")
	if err != nil {
		t.Fatal(err)
	}
	idx := fkIndex(sc.FKs)

	t.Run("BELONGS_TO / HAS_MANY の両側宣言は 1 本に畳む", func(t *testing.T) {
		fk, ok := idx["comment→post"] // {{comment}} → {{post}}(プレフィクス剥がし込み)
		if !ok {
			t.Fatalf("comment→post が無い: %v", idx)
		}
		if fk.ChildCols[0] != "post_id" {
			t.Errorf("FK 列: %v", fk.ChildCols)
		}
		n := 0
		for _, f := range sc.FKs {
			if f.ChildTable == "comment" && f.ParentTable == "post" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("両側宣言が重複した: %d 本", n)
		}
	})

	t.Run("tableName の tbl_ 直書きと {{}} の両対応", func(t *testing.T) {
		if _, ok := idx["post→tbl_user"]; !ok {
			t.Errorf("post→tbl_user が無い: %v", idx)
		}
	})

	t.Run("MANY_MANY は中間テーブルの FK 2 本を合成", func(t *testing.T) {
		if _, ok := idx["post_category→post"]; !ok {
			t.Errorf("post_category→post が無い: %v", idx)
		}
		if _, ok := idx["post_category→category"]; !ok {
			t.Errorf("post_category→category が無い: %v", idx)
		}
	})

	t.Run("beforeDelete の宣言外言及は注記", func(t *testing.T) {
		found := false
		for _, n := range sc.Notes {
			if strings.Contains(n, "Post") && strings.Contains(n, "Attachment") {
				found = true
			}
		}
		if !found {
			t.Errorf("beforeDelete の Attachment 言及が注記に無い: %v", sc.Notes)
		}
	})
}

func TestInflector(t *testing.T) {
	cases := map[string]string{
		"OrderItem": "order_items",
		"Person":    "people",
		"Category":  "categories",
		"Address":   "addresses",
		"User":      "users",
	}
	for model, want := range cases {
		if got := tableize(model); got != want {
			t.Errorf("tableize(%s) = %s, want %s", model, got, want)
		}
	}
	if classify("order_items") != "OrderItem" {
		t.Errorf("classify(order_items) = %s", classify("order_items"))
	}
}
