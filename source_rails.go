// source_rails.go: Rails の app/models を静的に読み、関連宣言を FK 相当へ写す。
// DB 接続不要 — 「FK を張らない文化圏」で材料 1(宣言された関係)を回収するための入口。
//
// フル Ruby パーサは持たない。行単位の正規表現で belongs_to / has_many / has_one を
// 読む割り切り。外れたときの逃げ道は self.table_name と、DB スキャンとの併用。
//
// テーブル名は Rails と同じ規則で組み立てる: self.table_name > STI の親 >
// 名前空間(モジュールの table_name_prefix、または囲むモデルクラスの単数形)+
// demodulize + tableize。関連の相手は名前空間の内側から順に探し、見つからない
// 相手は FK にしない(既知の gem は対応表で引く)。
//
// 重みの写像(graph.go の fkWeight に合わせる):
//
//	dependent: :destroy / :delete_all / :destroy_async → DeleteRule=CASCADE(重み 3)
//	belongs_to(Rails 5+ は必須が既定)                 → AllNotNull=true(重み 2)
//	belongs_to ..., optional: true                      → AllNotNull=false(重み 1)
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type railsAssoc struct {
	kind        string // belongs_to / has_many / has_one
	name        string
	className   string // class_name: 指定(無ければ空)
	foreignKey  string // foreign_key: 指定(無ければ空)
	optional    bool
	polymorphic bool
	dependent   bool // destroy / delete_all / destroy_async
	through     bool
	as          string // as: :xxx(ポリモーフィックの受け手)
	inverseOf   string // inverse_of: :xxx(has_many の外部キーを相手の belongs_to から決める)
	file        string // 宣言のあるファイル(concern 経由なら concern のファイル)
	line        int    // 宣言の行番号(Evidence の Origin)
}

type railsModel struct {
	class       string
	parent      string
	tableName   string // self.table_name 指定(無ければ空)
	abstract    bool   // abstract_class = true / primary_abstract_class
	assocs      []railsAssoc
	hasCallback bool     // before_save / after_commit 等を持つ
	includes    []string // include した concern 名(callback と言及をここから伝播)
	fileSrc     string   // 定義ファイルの中身(宣言外言及の検査に使う)
}

// railsConcern は app/models/concerns 等の module。Mastodon はモデルの callback の
// 本体をほぼ concern に置くので、include 先のモデルへ伝播させないと素通しになる。
type railsConcern struct {
	name        string
	hasCallback bool
	fileSrc     string
	assocs      []railsAssoc // concern 内の belongs_to / has_many(included do の中)
}

// isARModel: 祖先を辿って ActiveRecord::Base / ApplicationRecord に到達する
// クラスだけをモデル扱いする。`class Error < StandardError` のような PORO を
// テーブルにしない(Mastodon 実測で幽霊テーブルが出た)。
func isARModel(models map[string]*railsModel, class string) bool {
	seen := map[string]bool{}
	for c := class; !seen[c]; {
		seen[c] = true
		m, ok := models[c]
		if !ok {
			return false
		}
		if m.parent == "ApplicationRecord" || m.parent == "ActiveRecord::Base" {
			return true
		}
		p, ok := resolveClass(models, c, m.parent)
		if !ok {
			return false
		}
		c = p
	}
	return false
}

// resolveClass は from クラスの中から書かれた定数名 name を、Rails と同じく
// 名前空間の内側から順に探す(Fasp::Subscription 内の Provider は
// Fasp::Subscription::Provider → Fasp::Provider → Provider)。
func resolveClass(models map[string]*railsModel, from, name string) (string, bool) {
	if strings.HasPrefix(name, "::") {
		name = strings.TrimPrefix(name, "::")
		_, ok := models[name]
		return name, ok
	}
	parts := strings.Split(from, "::")
	for i := len(parts); i >= 1; i-- {
		cand := strings.Join(parts[:i], "::") + "::" + name
		if _, ok := models[cand]; ok {
			return cand, true
		}
	}
	_, ok := models[name]
	return name, ok
}

// knownGemTables: app/models に居ない、よく使われる gem のモデル → テーブル名。
// これ以外の見つからない相手は、推測で名前を作らず FK にしない(偽エッジを作らない)。
var knownGemTables = map[string]string{
	"Doorkeeper::AccessGrant":      "oauth_access_grants",
	"Doorkeeper::AccessToken":      "oauth_access_tokens",
	"Doorkeeper::Application":      "oauth_applications",
	"ActiveStorage::Attachment":    "active_storage_attachments",
	"ActiveStorage::Blob":          "active_storage_blobs",
	"ActiveStorage::VariantRecord": "active_storage_variant_records",
	"ActionText::RichText":         "action_text_rich_texts",
	"ActionMailbox::InboundEmail":  "action_mailbox_inbound_emails",
}

var (
	// def self.table_name_prefix(次の行に文字列)/ 1 行の def / self.table_name_prefix =
	rePrefixDef    = regexp.MustCompile(`^\s*def\s+self\.table_name_prefix\b(?:\s*(?:=|;)\s*['"]([^'"]*)['"])?`)
	rePrefixAssign = regexp.MustCompile(`^\s*self\.table_name_prefix\s*=\s*['"]([^'"]*)['"]`)
	reStringLine   = regexp.MustCompile(`^\s*['"]([^'"]*)['"]\s*$`)
	reClass        = regexp.MustCompile(`^\s*class\s+([A-Z][A-Za-z0-9_:]*)\s*<\s*([A-Z][A-Za-z0-9_:]*)`)
	reTableName    = regexp.MustCompile(`self\.table_name\s*=\s*["']([^"']+)["']`)
	reAbstract     = regexp.MustCompile(`self\.abstract_class\s*=\s*true|primary_abstract_class`)
	reAssoc        = regexp.MustCompile(`^\s*(belongs_to|has_many|has_one)\s+:(\w+)(.*)$`)
	reClassName    = regexp.MustCompile(`class_name:\s*["']([\w:]+)["']`)
	reForeignKey   = regexp.MustCompile(`foreign_key:\s*["':](\w+)["']?`)
	reDependent    = regexp.MustCompile(`dependent:\s*:(destroy_async|destroy|delete_all)`)
	reAs           = regexp.MustCompile(`\bas:\s*:(\w+)`)
	reInverseOf    = regexp.MustCompile(`\binverse_of:\s*:(\w+)`)
	// ライフサイクル hook の全形。after_create_commit 等の shorthand(Rails 5+)は
	// 後置の _commit まで取らないと \b で弾かれてすべて素通しになる(実測)。
	reCallback = regexp.MustCompile(`^\s*(before|after|around)_(save|create|update|destroy|commit|rollback|validation|touch|find|initialize)(_commit)?\b`)
	reModule   = regexp.MustCompile(`^\s*module\s+([A-Z][A-Za-z0-9_:]*)`)
	reInclude  = regexp.MustCompile(`^\s*include\s+([A-Z][A-Za-z0-9_:]+)\s*$`)
	reConst    = regexp.MustCompile(`\b([A-Z][A-Za-z0-9]+)\b`)
	// with_options のオプションはブロック内の全宣言に効く(Mastodon が多用する形)。
	reWithOptions = regexp.MustCompile(`^\s*with_options\s+(.+?)\s+do\s*(\|[^|]*\|)?\s*$`)
	reBlockOpen   = regexp.MustCompile(`(\bdo\s*(\|[^|]*\|)?\s*$)|(^\s*(def|if|unless|case|while|until|begin|module|class)\b)`)
	reBlockEnd    = regexp.MustCompile(`^\s*end\b`)
)

// ScanRails は Rails アプリ(または app/models 直接)を読む。
func ScanRails(dir string) (*ScanResult, error) {
	modelsDir := dir
	if fi, err := os.Stat(filepath.Join(dir, "app", "models")); err == nil && fi.IsDir() {
		modelsDir = filepath.Join(dir, "app", "models")
	}

	var files []string
	err := filepath.WalkDir(modelsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".rb") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("models: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s に .rb が見つかりません(Rails アプリのルートか app/models を指定)", modelsDir)
	}
	sort.Strings(files)

	models := map[string]*railsModel{}
	concerns := map[string]*railsConcern{}
	prefixes := map[string]string{} // モジュールの完全名 → table_name_prefix
	fileOf := map[string]string{}   // class → file
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		before := len(models)
		parseRailsFile(string(raw), f, models, concerns, prefixes)
		if len(models) > before {
			for c, m := range models {
				if m.fileSrc == string(raw) && fileOf[c] == "" {
					fileOf[c] = f
				}
			}
		}
	}

	// concern の中身(関連宣言・callback・言及)を include 先のモデルへ伝播する。
	// Mastodon は Account の関連の大半を concern の included do に置くので、
	// これをやらないと FK ごと素通しになる(実測)。
	for _, m := range models {
		for _, inc := range m.includes {
			c, ok := concerns[inc]
			if !ok {
				continue
			}
			m.assocs = append(m.assocs, c.assocs...)
			if c.hasCallback {
				m.hasCallback = true
			}
			m.fileSrc += "\n" + c.fileSrc
		}
	}

	rs := &railsSchema{models: models, prefixes: prefixes}
	res := railsToScan(dir, rs)
	res.FileTables = map[string]string{}
	for c, f := range fileOf {
		if rs.isModel(c) {
			res.FileTables[f] = rs.tableOf(c)
		}
	}
	return res, nil
}

// joinContinuations: 行末カンマ(や開き括弧)で折り返された宣言を 1 行に畳む。
// Mastodon の belongs_to は
//
//	belongs_to :action_taken_by_account,
//	           class_name: 'Account',
//	           optional: true
//
// の形が多数派で、行単位の読みでは class_name を全部取りこぼす(実測)。
//
// 2 つ目の戻り値は畳んだ各行の開始行番号(1 始まり)。
func joinContinuations(src string) ([]string, []int) {
	raw := strings.Split(src, "\n")
	var out []string
	var starts []int
	var buf string
	start := 0
	for i, line := range raw {
		if buf != "" {
			buf += " " + strings.TrimSpace(line)
		} else {
			buf = line
			start = i + 1
		}
		t := strings.TrimSpace(buf)
		if strings.HasSuffix(t, ",") || strings.HasSuffix(t, "(") {
			continue
		}
		out = append(out, buf)
		starts = append(starts, start)
		buf = ""
	}
	if buf != "" {
		out = append(out, buf)
		starts = append(starts, start)
	}
	return out, starts
}

func parseRailsFile(src, path string, models map[string]*railsModel, concerns map[string]*railsConcern, prefixes map[string]string) {
	var cur *railsModel
	var curConcern *railsConcern
	// 名前空間の入れ子をインデントで追う(module Fasp の中の class BackfillRequest は
	// Fasp::BackfillRequest)。Ruby の構文を解くのではなく、慣習的な字下げに頼る近似。
	type scope struct {
		indent  int
		name    string
		isClass bool
	}
	var ns []scope
	qualify := func(name string) string {
		if strings.HasPrefix(name, "::") || len(ns) == 0 {
			return strings.TrimPrefix(name, "::")
		}
		return ns[len(ns)-1].name + "::" + name
	}
	// with_options ブロックのオプションを積む。ブロック境界は do/end の
	// 近似追跡(モデルファイルの平坦な構造が前提の割り切り)。
	var optStack []string
	lines, starts := joinContinuations(src)
	for li, line := range lines {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			indent := len(line) - len(strings.TrimLeft(line, " \t"))
			for len(ns) > 0 && ns[len(ns)-1].indent >= indent {
				top := ns[len(ns)-1]
				ns = ns[:len(ns)-1]
				if top.indent == indent && reBlockEnd.MatchString(line) {
					break // この end がスコープを閉じた
				}
			}
			// 入れ子のクラス(class PreservedMediaAttachment ... end)を抜けたら、
			// 以降の宣言は外側のクラスのもの。抜けたことに気づかないと、外側の
			// belongs_to を入れ子の(モデルでない)クラスに付けて捨ててしまう(実測)。
			if cur != nil {
				inScope := false
				for _, sc := range ns {
					inScope = inScope || (sc.isClass && sc.name == cur.class)
				}
				if !inScope {
					cur = nil
					for k := len(ns) - 1; k >= 0; k-- {
						if ns[k].isClass {
							cur = models[ns[k].name]
							break
						}
					}
				}
			}
			if m := reModule.FindStringSubmatch(line); m != nil {
				ns = append(ns, scope{indent, qualify(m[1]), false})
			} else if m := reClass.FindStringSubmatch(line); m != nil {
				ns = append(ns, scope{indent, qualify(m[1]), true})
			}
			owner := ""
			if len(ns) > 0 {
				owner = ns[len(ns)-1].name
			}
			if m := rePrefixDef.FindStringSubmatch(line); m != nil && owner != "" {
				val, ok := m[1], m[1] != ""
				if !ok && li+1 < len(lines) {
					if n := reStringLine.FindStringSubmatch(lines[li+1]); n != nil {
						val, ok = n[1], true
					}
				}
				if ok {
					prefixes[owner] = val
				}
			} else if m := rePrefixAssign.FindStringSubmatch(line); m != nil && owner != "" {
				prefixes[owner] = m[1]
			}
		}
		if m := reClass.FindStringSubmatch(line); m != nil {
			full := ns[len(ns)-1].name
			cur = &railsModel{class: full, parent: m[2], fileSrc: src}
			models[full] = cur
			optStack = optStack[:0]
			continue
		}
		// class より前の module 行 = concern(名前空間モデルのファイルでは
		// class 行が現れた時点で以降は class に付く)。
		if cur == nil && curConcern == nil {
			if m := reModule.FindStringSubmatch(line); m != nil {
				curConcern = &railsConcern{name: m[1], fileSrc: src}
				concerns[m[1]] = curConcern
				continue
			}
		}
		if cur == nil && curConcern == nil {
			continue
		}
		switch {
		case reWithOptions.MatchString(line):
			optStack = append(optStack, reWithOptions.FindStringSubmatch(line)[1])
		case reBlockOpen.MatchString(line):
			optStack = append(optStack, "")
		case reBlockEnd.MatchString(line):
			if len(optStack) > 0 {
				optStack = optStack[:len(optStack)-1]
			}
		}
		if cur != nil {
			if m := reTableName.FindStringSubmatch(line); m != nil {
				cur.tableName = m[1]
			}
			if reAbstract.MatchString(line) {
				cur.abstract = true
			}
			if m := reInclude.FindStringSubmatch(line); m != nil {
				cur.includes = append(cur.includes, strings.TrimPrefix(m[1], "::"))
			}
		}
		if reCallback.MatchString(line) {
			if cur != nil {
				cur.hasCallback = true
			} else {
				curConcern.hasCallback = true
			}
		}
		m := reAssoc.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rest := m[3]
		for _, o := range optStack { // ブロックのオプションを行のオプションに合成
			if o != "" {
				rest += " " + o
			}
		}
		a := railsAssoc{kind: m[1], name: m[2], file: path, line: starts[li]}
		if c := reClassName.FindStringSubmatch(rest); c != nil {
			a.className = strings.TrimPrefix(c[1], "::")
		}
		if c := reForeignKey.FindStringSubmatch(rest); c != nil {
			a.foreignKey = c[1]
		}
		a.optional = strings.Contains(rest, "optional: true")
		a.polymorphic = strings.Contains(rest, "polymorphic: true")
		a.dependent = reDependent.MatchString(rest)
		a.through = strings.Contains(rest, "through:")
		if c := reAs.FindStringSubmatch(rest); c != nil {
			a.as = c[1]
		}
		if c := reInverseOf.FindStringSubmatch(rest); c != nil {
			a.inverseOf = c[1]
		}
		if cur != nil {
			cur.assocs = append(cur.assocs, a)
		} else {
			curConcern.assocs = append(curConcern.assocs, a)
		}
	}
}

// demodulize: "Doorkeeper::AccessToken" → "AccessToken"。
// ActiveRecord の名前空間モデルの既定テーブル名は demodulize + tableize
// (module 側の table_name_prefix は読まない割り切り)。
func demodulize(class string) string {
	if i := strings.LastIndex(class, "::"); i >= 0 {
		return class[i+2:]
	}
	return class
}

// railsSchema はテーブル名の解決に要る材料(モデルとモジュールの接頭辞)。
type railsSchema struct {
	models   map[string]*railsModel
	prefixes map[string]string
}

// isModel: テーブルを持つ AR モデルか(abstract 基底・PORO を除く)。
func (rs *railsSchema) isModel(class string) bool {
	m, ok := rs.models[class]
	return ok && isARModel(rs.models, class) && !m.abstract && class != "ApplicationRecord"
}

// target は関連の相手クラスをテーブルに解決する。app/models に居なければ
// 既知の gem の対応表を引き、それでも無ければ ok=false(FK にしない)。
func (rs *railsSchema) target(from, name string) (class, table string, ok bool) {
	if c, found := resolveClass(rs.models, from, name); found && rs.isModel(c) {
		return c, rs.tableOf(c), true
	}
	if t, known := knownGemTables[strings.TrimPrefix(name, "::")]; known {
		return strings.TrimPrefix(name, "::"), t, true
	}
	return name, "", false
}

// tableOf はモデルのテーブル名(STI は親のテーブル)。
// abstract な親(ApplicationRecord 等)は STI の対象にしない — ここを貫通させると
// 全モデルが application_records に融合する(Mastodon 実測)。
func (rs *railsSchema) tableOf(class string) string {
	if t, known := knownGemTables[class]; known {
		return t
	}
	seen := map[string]bool{}
	for c := class; ; {
		m, ok := rs.models[c]
		if !ok || seen[c] {
			return rs.computeTableName(class)
		}
		seen[c] = true
		if m.tableName != "" {
			return m.tableName
		}
		if pc, found := resolveClass(rs.models, c, m.parent); found {
			if p := rs.models[pc]; !p.abstract && pc != "ApplicationRecord" {
				c, class = pc, pc
				continue
			}
		}
		return rs.computeTableName(m.class)
	}
}

// computeTableName は Rails の compute_table_name と同じ組み立て:
// 最も内側の囲みがモデルクラスなら「その単数形_」、table_name_prefix を持つ
// モジュールならその接頭辞を、demodulize + tableize の前に付ける。
func (rs *railsSchema) computeTableName(class string) string {
	base := tableize(demodulize(class))
	parts := strings.Split(class, "::")
	for i := len(parts) - 1; i >= 1; i-- {
		enc := strings.Join(parts[:i], "::")
		if rs.isModel(enc) {
			return singularize(rs.tableOf(enc)) + "_" + base
		}
		if p, ok := rs.prefixes[enc]; ok {
			return p + base
		}
	}
	return base
}

func railsToScan(dir string, rs *railsSchema) *ScanResult {
	models := rs.models
	tableOf := func(_ map[string]*railsModel, class string) string { return rs.tableOf(class) }
	res := &ScanResult{Schema: "rails:" + filepath.Base(strings.TrimRight(dir, "/"))}
	tables := map[string]bool{}
	// AR の系譜に乗るクラスだけがモデル。abstract 基底はテーブルを持たない。
	classes := make([]string, 0, len(models))
	for c, m := range models {
		if isARModel(models, c) && !m.abstract && c != "ApplicationRecord" {
			classes = append(classes, c)
		}
	}
	sort.Strings(classes)
	for _, c := range classes {
		tables[tableOf(models, c)] = true
	}

	// 相手のモデルが見つからない関連。推測でテーブル名を作ると、DB に無い
	// テーブルへの偽の関係になる(Doorkeeper::AccessToken → access_tokens)ので
	// FK にせず、注記にまとめる。
	var unresolved []string
	// parent は解決済みのテーブル名。自己参照(木構造・返信の連鎖)も作る —
	// DB スキャンは自己参照 FK を持つので、捨てると Edge Diff で「DB にだけ
	// ある関係」に見える。分割に効かないのはグラフ層(BuildEdges)が落とすため。
	addFK := func(childClass, parentClass, parent, col string, notNull bool, note string, decl railsAssoc) {
		child := tableOf(models, childClass)
		name := fmt.Sprintf("ar:%s.%s", underscorePath(childClass), col)
		if note != "" {
			name += "(" + note + ")"
		}
		// 必須性は belongs_to の宣言値(Rails 5+ は既定で必須)。DB の NOT NULL を
		// 確認したわけではないので、重みの理由は logical_* になる(provenance.go)。
		nullable := NullableTrue
		if notNull {
			nullable = NullableFalse
		}
		evs := []Evidence{{Source: SourceRails, Origin: sourceOrigin(dir, decl.file, decl.line),
			Constraint: name, DeleteRule: "NO ACTION", Nullable: nullable}}
		res.FKs = append(res.FKs, FK{
			Constraint: name, ChildTable: child, ChildCols: []string{col},
			ParentTable: parent, DeleteRule: "NO ACTION", AllNotNull: notNull,
			Nullable: nullable, Evidences: evs,
		})
		tables[child], tables[parent] = true, true
	}

	for _, cc := range classes {
		m := models[cc]
		for _, a := range m.assocs {
			if a.kind != "belongs_to" || a.through {
				continue
			}
			col := a.foreignKey
			if col == "" {
				col = a.name + "_id"
			}
			if a.polymorphic {
				// 受け手(has_many ..., as: :name で、関連名がこのモデルに解決されるもの)へ展開
				found := false
				for _, pc := range classes {
					for _, pa := range models[pc].assocs {
						if pa.as != a.name {
							continue
						}
						name := pa.className
						if name == "" {
							name = classify(pa.name)
						}
						if target, _, ok := rs.target(pc, name); ok && target == cc {
							addFK(cc, pc, rs.tableOf(pc), col, !a.optional, "polymorphic", a)
							found = true
						}
					}
				}
				if !found {
					res.Notes = append(res.Notes,
						fmt.Sprintf("%s.%s は polymorphic だが as: :%s の受け手が見つからない(エッジ化していない)",
							cc, a.name, a.name))
				}
				continue
			}
			name := a.className
			if name == "" {
				name = classify(a.name)
			}
			target, table, ok := rs.target(cc, name)
			if !ok {
				unresolved = append(unresolved, fmt.Sprintf("%s.%s → %s", cc, a.name, name))
				continue
			}
			addFK(cc, target, table, col, !a.optional, "", a)
		}
	}

	// 親側の has_many / has_one。同じ関係の belongs_to があれば証拠を足すだけ、
	// 無ければ FK を作る(関係が親側にしか書かれていないモデルは珍しくない —
	// Mastodon の WebauthnCredential は User の has_many にしか現れない)。
	// dependent: の CASCADE は、その has_many が実際に使う列の FK にだけ付ける
	// (同じ 2 モデル間の別の belongs_to に付けない)。
	fkAt := map[string]int{}
	for i, fk := range res.FKs {
		fkAt[relationKey(fk)] = i
	}
	for _, pc := range classes {
		for _, a := range models[pc].assocs {
			if (a.kind != "has_many" && a.kind != "has_one") || a.through {
				continue
			}
			name := a.className
			if name == "" {
				name = classify(a.name)
			}
			target, table, ok := rs.target(pc, name)
			if !ok {
				unresolved = append(unresolved, fmt.Sprintf("%s.%s → %s", pc, a.name, name))
				continue
			}
			// 外部キーは Rails 7.1 の derive_foreign_key と同じ優先順位:
			// foreign_key: > as: の <名前>_id > inverse_of: の相手の belongs_to の列 >
			// 持ち主のクラス名_id
			backCols := map[string]bool{} // 相手が持ち主へ向けている belongs_to の列
			inverseCol := ""
			if tm, ok := models[target]; ok {
				for _, b := range tm.assocs {
					if b.kind != "belongs_to" {
						continue
					}
					bcol := b.foreignKey
					if bcol == "" {
						bcol = b.name + "_id"
					}
					if b.name == a.inverseOf {
						inverseCol = bcol
					}
					bn := b.className
					if bn == "" {
						bn = classify(b.name)
					}
					if bt, _, ok := rs.target(target, bn); ok && bt == pc {
						backCols[bcol] = true
					}
				}
			}
			col := a.foreignKey
			switch {
			case col != "":
			case a.as != "":
				col = a.as + "_id"
			case inverseCol != "":
				col = inverseCol
			default:
				col = underscore(demodulize(pc)) + "_id"
			}
			parent := rs.tableOf(pc)
			rule := "NO ACTION"
			if a.dependent {
				rule = "CASCADE"
			}
			probe := FK{ChildTable: table, ChildCols: []string{col}, ParentTable: parent}
			if i, ok := fkAt[relationKey(probe)]; ok {
				fk := &res.FKs[i]
				fk.Evidences = append(fk.Evidences, Evidence{Source: SourceRails,
					Origin: sourceOrigin(dir, a.file, a.line), Constraint: fk.Constraint,
					DeleteRule: rule, Nullable: NullableUnknown})
				if a.dependent {
					fk.DeleteRule = "CASCADE"
				}
				continue
			}
			if a.as != "" {
				continue // ポリモーフィックは belongs_to 側の展開でだけ作る(型の列が要るため)
			}
			if len(backCols) > 0 && !backCols[col] {
				// 相手は持ち主への belongs_to を別の列で持っている。推定した列は
				// 外れている可能性が高いので作らない(迷ったら偽エッジを作らない側へ)
				continue
			}
			// 親側の宣言だけでは NULL 許容は分からない(unknown)
			cname := fmt.Sprintf("ar:%s.%s(%s)", underscorePath(target), col, a.kind)
			fkAt[relationKey(probe)] = len(res.FKs)
			res.FKs = append(res.FKs, FK{
				Constraint: cname, ChildTable: table, ChildCols: []string{col},
				ParentTable: parent, DeleteRule: rule, Nullable: NullableUnknown,
				Evidences: []Evidence{{Source: SourceRails, Origin: sourceOrigin(dir, a.file, a.line),
					Constraint: cname, DeleteRule: rule, Nullable: NullableUnknown}},
			})
			tables[table], tables[parent] = true, true
		}
	}

	if len(unresolved) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"[判定不能] 相手のモデルが見つからない関連 %d 件は FK にしていない(app/models に居ない gem のモデル等。推測でテーブル名を作ると偽の関係になる): %s",
			len(unresolved), strings.Join(unresolved, ", ")))
	}

	// 宣言外の他モデル言及は、結合の疑いとして注記だけする(全モデル対象)。
	// エッジにはしない — ファイル粒度のヒューリスティックで、偽エッジは信頼を壊すため。
	// 確度は 2 段階: callback(concern 経由含む)持ち = 強(ライフサイクルに乗った
	// 書き込みの可能性大)、無し = 弱(メソッド・スコープからの参照)。強を先に出す。
	var strongNotes, weakNotes []string
	for _, cc := range classes {
		m := models[cc]
		declared := map[string]bool{cc: true}
		for _, a := range m.assocs {
			t := a.className
			if t == "" {
				t = classify(a.name)
			}
			declared[t] = true
		}
		var mentions []string
		seen := map[string]bool{}
		for _, c := range reConst.FindAllString(m.fileSrc, -1) {
			// 実テーブルを持つモデルへの言及だけがヒントになる
			// (ApplicationRecord / PORO はノイズ)
			if mm, ok := models[c]; ok && isARModel(models, c) && !mm.abstract &&
				c != "ApplicationRecord" && !declared[c] && !seen[c] {
				mentions = append(mentions, c)
				seen[c] = true
			}
		}
		if len(mentions) == 0 {
			continue
		}
		sort.Strings(mentions)
		for _, mc := range mentions {
			res.Suspects = append(res.Suspects, Suspect{
				FromTable: tableOf(models, cc), ToTable: tableOf(models, mc), Strong: m.hasCallback})
		}
		if m.hasCallback {
			strongNotes = append(strongNotes,
				fmt.Sprintf("[強] %s: callback(concern 含む)+ 宣言外の %s への言及 — 書き込み結合の疑い",
					cc, strings.Join(mentions, ", ")))
		} else {
			weakNotes = append(weakNotes,
				fmt.Sprintf("[弱] %s: メソッド/スコープから宣言外の %s への言及",
					cc, strings.Join(mentions, ", ")))
		}
	}
	// joins/includes の宣言外参照(read 側の暗黙結合、[弱])。
	// 宣言済み関連への joins は FK 側で既に見えているので、未宣言のみ拾う。
	reJoins := regexp.MustCompile(`(?:joins|includes|eager_load|preload)\(\s*:(\w+)`)
	for _, cc := range classes {
		m := models[cc]
		declared := map[string]bool{cc: true}
		for _, a2 := range m.assocs {
			t := a2.className
			if t == "" {
				t = classify(a2.name)
			}
			declared[t] = true
		}
		var hits []string
		seen := map[string]bool{}
		for _, j := range reJoins.FindAllStringSubmatch(m.fileSrc, -1) {
			target := classify(j[1])
			if declared[target] || seen[target] {
				continue
			}
			if _, ok := models[target]; !ok || !isARModel(models, target) {
				continue
			}
			seen[target] = true
			hits = append(hits, target)
			res.Suspects = append(res.Suspects, Suspect{
				FromTable: tableOf(models, cc), ToTable: tableOf(models, target), Strong: false})
		}
		if len(hits) > 0 {
			sort.Strings(hits)
			weakNotes = append(weakNotes,
				fmt.Sprintf("[弱] %s: joins/includes で宣言外の %s を読む(read 側の暗黙結合)", cc, strings.Join(hits, ", ")))
		}
	}

	// 生 SQL(execute / sanitize 済み文字列)の書き込み先([強])
	tableSet := map[string]bool{}
	for t := range tables {
		tableSet[t] = true
	}
	for _, cc := range classes {
		m := models[cc]
		own := tableOf(models, cc)
		var hits []string
		for _, t := range extractRawWriteTables(m.fileSrc) {
			if t == own || !tableSet[t] {
				continue
			}
			hits = append(hits, t)
			res.Suspects = append(res.Suspects, Suspect{FromTable: own, ToTable: t, Strong: true})
		}
		if len(hits) > 0 {
			strongNotes = append(strongNotes,
				fmt.Sprintf("[強] %s: 生SQLで %s へ書き込み — 宣言に現れない実結合", cc, strings.Join(hits, ", ")))
		}
	}

	res.Notes = append(res.Notes, strongNotes...)
	res.Notes = append(res.Notes, weakNotes...)

	for t := range tables {
		res.Tables = append(res.Tables, t)
	}
	sort.Strings(res.Tables)
	// モデル定義を読んだテーブル(gem の対応表で引いた相手は含めない)
	modelSeen := map[string]bool{}
	for _, c := range classes {
		modelSeen[tableOf(models, c)] = true
	}
	for t := range modelSeen {
		res.ModelTables = append(res.ModelTables, t)
	}
	sort.Strings(res.ModelTables)
	return res
}

// underscorePath は Rails の underscore と同じく名前空間を / で区切る
// (Web::PushSubscription → web/push_subscription)。
func underscorePath(class string) string {
	parts := strings.Split(class, "::")
	for i, p := range parts {
		parts[i] = underscore(p)
	}
	return strings.Join(parts, "/")
}
