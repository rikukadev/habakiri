package main

import (
	"strings"
	"testing"
)

// fkIndex: "child(col1,col2)→parent" で引く(同一テーブル対に複数 FK がありうる)。
func fkIndex(fks []FK) map[string]FK {
	m := map[string]FK{}
	for _, fk := range fks {
		m[fk.ChildTable+"("+strings.Join(fk.ChildCols, ",")+")→"+fk.ParentTable] = fk
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
		fk, ok := idx["posts(user_id)→users"]
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
		fk, ok := idx["posts(category_id)→taxonomy"] // Category は self.table_name = taxonomy
		if !ok {
			t.Fatalf("posts→taxonomy が無い(self.table_name が効いていない): %v", idx)
		}
		if fk.AllNotNull || fk.DeleteRule != "NO ACTION" {
			t.Errorf("optional が弱い結合に写っていない: %+v", fk)
		}
	})

	t.Run("class_name + foreign_key の明示", func(t *testing.T) {
		fk, ok := idx["comments(author_id)→users"]
		if !ok {
			t.Fatalf("comments→users が無い: %v", idx)
		}
		if fk.ChildCols[0] != "author_id" {
			t.Errorf("foreign_key 指定が効いていない: %v", fk.ChildCols)
		}
	})

	t.Run("polymorphic は as: の受け手へ展開", func(t *testing.T) {
		if _, ok := idx["comments(commentable_id)→posts"]; !ok {
			t.Errorf("commentable → Post が引けていない: %v", idx)
		}
	})

	t.Run("Mastodon 実測の回帰 4 種", func(t *testing.T) {
		// (1) abstract 基底(ApplicationRecord)はテーブルにならない・STI で貫通しない
		for _, tbl := range sc.Tables {
			if tbl == "application_records" {
				t.Error("abstract 基底がテーブルになっている")
			}
			// (2) 非 AR クラス(PORO)はテーブルにならない
			if tbl == "api_errors" {
				t.Error("StandardError 継承の PORO がテーブルになっている")
			}
		}
		// (3) 複数行宣言の class_name を読める(comments→users が幽霊 authors にならない)
		if _, ok := idx["comments(author_id)→authors"]; ok {
			t.Error("複数行の class_name を取りこぼして幽霊テーブルを作った")
		}
		// (4) 名前空間モデルは demodulize + tableize
		if _, ok := idx["comments(access_grant_id)→access_grants"]; !ok {
			t.Errorf("Doorkeeper::AccessGrant が demodulize されていない: %v", idx)
		}
	})

	t.Run("concern の関連宣言と callback が include 先へ伝播する", func(t *testing.T) {
		// Indexable(module)の has_many :index_entries, dependent: :destroy が
		// User に伝播 → IndexEntry の belongs_to :user が CASCADE に昇格する
		fk, ok := idx["index_entries(user_id)→users"]
		if !ok {
			t.Fatalf("concern 内の関連が読めていない: %v", idx)
		}
		if fk.DeleteRule != "CASCADE" {
			t.Errorf("concern の dependent: が CASCADE に写っていない: %s", fk.DeleteRule)
		}
		// after_create_commit(shorthand)+ concern 経由の SearchEntry 言及 → [強]
		found := false
		for _, n := range sc.Notes {
			if strings.Contains(n, "[強] User") && strings.Contains(n, "SearchEntry") {
				found = true
			}
		}
		if !found {
			t.Errorf("concern 経由の callback 言及が [強] 注記に無い: %v", sc.Notes)
		}
	})

	t.Run("メソッドのみの宣言外言及は [弱] 注記", func(t *testing.T) {
		found := false
		for _, n := range sc.Notes {
			if strings.Contains(n, "[弱] Category") && strings.Contains(n, "Post") {
				found = true
			}
		}
		if !found {
			t.Errorf("メソッド言及の [弱] 注記が無い: %v", sc.Notes)
		}
	})

	t.Run("with_options のオプションがブロック内の宣言に効く", func(t *testing.T) {
		fk, ok := idx["posts(edited_by_id)→users"]
		if !ok {
			t.Fatalf("with_options 内の belongs_to :editor が読めていない: %v", idx)
		}
		if fk.AllNotNull {
			t.Error("ブロックの optional: true が効いていない")
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
		if _, ok := idx["posts(search_entry_id)→search_entries"]; ok {
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
		fk, ok := idx["comment(post_id)→post"] // {{comment}} → {{post}}(プレフィクス剥がし込み)
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
		if _, ok := idx["post(author_id)→tbl_user"]; !ok {
			t.Errorf("post→tbl_user が無い: %v", idx)
		}
	})

	t.Run("MANY_MANY は中間テーブルの FK 2 本を合成", func(t *testing.T) {
		if _, ok := idx["post_category(post_id)→post"]; !ok {
			t.Errorf("post_category→post が無い: %v", idx)
		}
		if _, ok := idx["post_category(category_id)→category"]; !ok {
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
	// -us 語尾は単数のまま(Mastodon の belongs_to :status で statu 幽霊が出た)
	if classify("status") != "Status" || classify("statuses") != "Status" {
		t.Errorf("classify(status)=%s classify(statuses)=%s", classify("status"), classify("statuses"))
	}
	if tableize("Status") != "statuses" {
		t.Errorf("tableize(Status) = %s", tableize("Status"))
	}
}
