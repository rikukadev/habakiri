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
		// (4) app/models に居ない gem のモデルは対応表で引く。demodulize して
		// access_grants という実在しないテーブルを作らない(#44)
		if _, ok := idx["comments(access_grant_id)→oauth_access_grants"]; !ok {
			t.Errorf("Doorkeeper::AccessGrant が oauth_access_grants に写っていない: %v", idx)
		}
		if _, ok := idx["comments(access_grant_id)→access_grants"]; ok {
			t.Error("gem のモデルから偽のテーブル名を作った")
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

	t.Run("名前空間: table_name_prefix と囲むモデルクラス(#44)", func(t *testing.T) {
		for key, why := range map[string]string{
			// module Fasp; def self.table_name_prefix; 'fasp_'; end の配下。
			// 入れ子の class Subscription と、暗黙の belongs_to :provider(→ Fasp::Provider)
			"fasp_subscriptions(fasp_provider_id)→fasp_providers": "接頭辞 + 入れ子のクラス + 名前空間内の相対解決",
			// def self.table_name_prefix = 'web_'(1 行の def)+ まとめ書きの class Web::PushSubscription
			"web_push_subscriptions(user_id)→users": "1 行の def の接頭辞",
			// class User::Preference(囲みがモデルクラス)は「親テーブルの単数形_」
			"user_preferences(user_id)→users": "モデルクラスの中の名前空間",
		} {
			if _, ok := idx[key]; !ok {
				t.Errorf("%s が無い(%s): %v", key, why, idx)
			}
		}
		// 親側の has_many :subscriptions, dependent: も名前空間の中で解決される
		if fk := idx["fasp_subscriptions(fasp_provider_id)→fasp_providers"]; fk.DeleteRule != "CASCADE" {
			t.Errorf("名前空間内の dependent: が CASCADE に写っていない: %+v", fk)
		}
		for _, tbl := range sc.Tables {
			if tbl == "subscriptions" || tbl == "providers" || tbl == "push_subscriptions" || tbl == "preferences" {
				t.Errorf("接頭辞の無いテーブル名 %s が残っている: %v", tbl, sc.Tables)
			}
		}
	})

	t.Run("自己参照を捨てない(#44)", func(t *testing.T) {
		// DB スキャンは自己参照 FK を持つ。静的ソースで捨てると Edge Diff で
		// 「DB にだけある関係」に見える
		fk, ok := idx["taxonomy(parent_id)→taxonomy"]
		if !ok {
			t.Fatalf("belongs_to :parent, class_name: 'Category' が落ちた: %v", idx)
		}
		if fk.AllNotNull {
			t.Error("optional: true が効いていない")
		}
	})

	t.Run("相手が見つからない関連は FK にせず注記(#44)", func(t *testing.T) {
		for key := range idx {
			if strings.Contains(key, "reviewer") {
				t.Errorf("見つからない相手への FK を作った: %s", key)
			}
		}
		found := false
		for _, n := range sc.Notes {
			if strings.Contains(n, "[判定不能]") && strings.Contains(n, "Comment.reviewer → Moderation::Reviewer") {
				found = true
			}
		}
		if !found {
			t.Errorf("判定不能の注記が無い: %v", sc.Notes)
		}
	})

	t.Run("has_many 側にしか無い関係も FK にする(NULL 許容は unknown)", func(t *testing.T) {
		fk, ok := idx["webauthn_credentials(user_id)→users"]
		if !ok {
			t.Fatalf("User has_many :webauthn_credentials が FK になっていない: %v", idx)
		}
		if fk.Nullability() != NullableUnknown || fk.DeleteRule != "CASCADE" {
			t.Errorf("親側の宣言だけでは NULL 許容は分からない / dependent: は CASCADE: %+v", fk)
		}
	})

	t.Run("dependent: の CASCADE は has_many が使う列の FK にだけ付く", func(t *testing.T) {
		// User has_many :posts, dependent: :destroy が消すのは user_id で紐づく投稿。
		// 同じ 2 モデル間の別の belongs_to(edited_by_id)には付けない
		if fk := idx["posts(user_id)→users"]; fk.DeleteRule != "CASCADE" {
			t.Errorf("posts.user_id: %s", fk.DeleteRule)
		}
		if fk := idx["posts(edited_by_id)→users"]; fk.DeleteRule != "NO ACTION" {
			t.Errorf("posts.edited_by_id に CASCADE が漏れた: %s", fk.DeleteRule)
		}
		// inverse_of: の相手の列を使う(provider_id という偽の列を作らない)
		fk := idx["fasp_subscriptions(fasp_provider_id)→fasp_providers"]
		if fk.DeleteRule != "CASCADE" || len(fk.Evidences) != 2 {
			t.Errorf("inverse_of 経由の dependent: %+v", fk)
		}
		if _, ok := idx["fasp_subscriptions(provider_id)→fasp_providers"]; ok {
			t.Error("inverse_of を無視して既定の列名で偽の FK を作った")
		}
	})

	t.Run("入れ子クラスの後の宣言は外側のクラスのもの", func(t *testing.T) {
		if _, ok := idx["status_edits(post_id)→posts"]; !ok {
			t.Errorf("入れ子クラスの end の後の belongs_to が落ちた: %v", idx)
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

func TestRawSQLExtraction(t *testing.T) {
	sc, err := ScanYii1("testdata/yii1app")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, s := range sc.Suspects {
		if s.Strong && s.FromTable == "post" {
			found[s.ToTable] = true
		}
	}
	// createCommand の DELETE FROM {{attachment}} と ビルダ ->update('tbl_user')
	if !found["attachment"] || !found["tbl_user"] {
		t.Errorf("生SQL書き込み先が Suspect に無い: %+v", sc.Suspects)
	}
	noteOK := false
	for _, n := range sc.Notes {
		if strings.Contains(n, "生SQL") && strings.Contains(n, "attachment") {
			noteOK = true
		}
	}
	if !noteOK {
		t.Errorf("生SQL注記が無い: %v", sc.Notes)
	}
}

func TestYii1CascadeAndWith(t *testing.T) {
	sc, err := ScanYii1("testdata/yii1app")
	if err != nil {
		t.Fatal(err)
	}
	cascadeNote, withNote := false, false
	for _, n := range sc.Notes {
		if strings.Contains(n, "手書きカスケード") && strings.Contains(n, "Comment") {
			cascadeNote = true
		}
		if strings.Contains(n, "with で") && strings.Contains(n, "Comment") {
			withNote = true
		}
	}
	if !cascadeNote {
		t.Errorf("beforeDelete の deleteAll が手書きカスケードとして出ない: %v", sc.Notes)
	}
	if !withNote {
		t.Errorf("scopes の with が read 結合として出ない: %v", sc.Notes)
	}
	strong, weak := false, false
	for _, s := range sc.Suspects {
		if s.FromTable == "post" && s.ToTable == "comment" {
			if s.Strong {
				strong = true
			} else {
				weak = true
			}
		}
	}
	if !strong || !weak {
		t.Errorf("post→comment の 強(カスケード)/弱(with) Suspect: strong=%v weak=%v", strong, weak)
	}
}

// Yii1 の同種の解決漏れ(#44 の Yii1 版)。
func TestScanYii1Resolution(t *testing.T) {
	sc, err := ScanYii1("testdata/yii1modules")
	if err != nil {
		t.Fatal(err)
	}
	idx := fkIndex(sc.FKs)

	t.Run("tablePrefix を {{...}} にだけ前置する", func(t *testing.T) {
		want := "tbl_post,tbl_post_tag,tbl_user,tbl_user_stat"
		if got := strings.Join(sc.Tables, ","); got != want {
			t.Errorf("tables = %s, want %s", got, want)
		}
		// 生 SQL の DELETE FROM {{user_stat}} も接頭辞付きの名前に解決される
		found := false
		for _, s := range sc.Suspects {
			if s.FromTable == "tbl_post" && s.ToTable == "tbl_user_stat" && s.Strong {
				found = true
			}
		}
		if !found {
			t.Errorf("生 SQL の書き込み先が接頭辞付きで引けていない: %+v", sc.Suspects)
		}
	})

	t.Run("modules/*/models のモデルも読む", func(t *testing.T) {
		fk, ok := idx["tbl_post(author_id)→tbl_user"]
		if !ok {
			t.Fatalf("モジュール内の User への関係が無い: %v", idx)
		}
		// Post の BELONGS_TO と、モジュール内 User の HAS_MANY の 2 件
		if len(fk.Evidences) != 2 || !strings.Contains(fk.Evidences[1].Origin, "modules/account/models/User.php") {
			t.Errorf("証拠: %+v", fk.Evidences)
		}
		for _, tbl := range sc.Tables {
			if tbl == "tbl_helper" {
				t.Error("models ディレクトリの外(components)をモデルとして読んだ")
			}
		}
	})

	t.Run("自己参照を捨てない", func(t *testing.T) {
		fk, ok := idx["tbl_post(parent_id)→tbl_post"]
		if !ok {
			t.Fatalf("自己参照が落ちた: %v", idx)
		}
		if len(fk.Evidences) != 2 {
			t.Errorf("BELONGS_TO と HAS_MANY の両側宣言は 1 本に 2 件: %+v", fk.Evidences)
		}
	})

	t.Run("相手が見つからない関連は FK にせず注記", func(t *testing.T) {
		for key := range idx {
			if strings.Contains(key, "Tag") || strings.Contains(key, "AuditTrail") || strings.Contains(key, "tag_id") {
				t.Errorf("見つからない相手への FK を作った: %s", key)
			}
		}
		// MANY_MANY の自分側(中間テーブル → 自モデル)は宣言から確かなので残す
		if _, ok := idx["tbl_post_tag(post_id)→tbl_post"]; !ok {
			t.Errorf("MANY_MANY の自分側が落ちた: %v", idx)
		}
		found := false
		for _, n := range sc.Notes {
			if strings.Contains(n, "[判定不能]") && strings.Contains(n, "Post.tags → Tag") && strings.Contains(n, "Post.audit → AuditTrail") {
				found = true
			}
		}
		if !found {
			t.Errorf("判定不能の注記が無い: %v", sc.Notes)
		}
	})
}
