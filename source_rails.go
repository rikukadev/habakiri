// source_rails.go: Rails の app/models を静的に読み、関連宣言を FK 相当へ写す。
// DB 接続不要 — 「FK を張らない文化圏」で材料 1(宣言された関係)を回収するための入口。
//
// フル Ruby パーサは持たない。行単位の正規表現で belongs_to / has_many / has_one を
// 読む割り切り。外れたときの逃げ道は self.table_name と、DB スキャンとの併用。
//
// 重みの写像(graph.go の fkWeight に合わせる):
//   dependent: :destroy / :delete_all / :destroy_async → DeleteRule=CASCADE(重み 3)
//   belongs_to(Rails 5+ は必須が既定)                 → AllNotNull=true(重み 2)
//   belongs_to ..., optional: true                      → AllNotNull=false(重み 1)
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
	kind       string // belongs_to / has_many / has_one
	name       string
	className  string // class_name: 指定(無ければ空)
	foreignKey string // foreign_key: 指定(無ければ空)
	optional   bool
	polymorphic bool
	dependent  bool   // destroy / delete_all / destroy_async
	through    bool
	as         string // as: :xxx(ポリモーフィックの受け手)
}

type railsModel struct {
	class       string
	parent      string
	tableName   string // self.table_name 指定(無ければ空)
	abstract    bool   // abstract_class = true / primary_abstract_class
	assocs      []railsAssoc
	hasCallback bool   // before_save / after_save / before_destroy 等を持つ
	fileSrc     string // 定義ファイルの中身(callback の言及検査に使う)
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
		c = m.parent
	}
	return false
}

var (
	reClass     = regexp.MustCompile(`^\s*class\s+([A-Z][A-Za-z0-9_:]*)\s*<\s*([A-Z][A-Za-z0-9_:]*)`)
	reTableName = regexp.MustCompile(`self\.table_name\s*=\s*["']([^"']+)["']`)
	reAbstract  = regexp.MustCompile(`self\.abstract_class\s*=\s*true|primary_abstract_class`)
	reAssoc     = regexp.MustCompile(`^\s*(belongs_to|has_many|has_one)\s+:(\w+)(.*)$`)
	reClassName = regexp.MustCompile(`class_name:\s*["']([\w:]+)["']`)
	reForeignKey = regexp.MustCompile(`foreign_key:\s*["':](\w+)["']?`)
	reDependent = regexp.MustCompile(`dependent:\s*:(destroy_async|destroy|delete_all)`)
	reAs        = regexp.MustCompile(`\bas:\s*:(\w+)`)
	reCallback  = regexp.MustCompile(`^\s*(before|after|around)_(save|create|update|destroy|commit|validation)\b`)
	reConst     = regexp.MustCompile(`\b([A-Z][A-Za-z0-9]+)\b`)
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
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		parseRailsFile(string(raw), models)
	}

	return railsToScan(dir, models), nil
}

// joinContinuations: 行末カンマ(や開き括弧)で折り返された宣言を 1 行に畳む。
// Mastodon の belongs_to は
//
//	belongs_to :action_taken_by_account,
//	           class_name: 'Account',
//	           optional: true
//
// の形が多数派で、行単位の読みでは class_name を全部取りこぼす(実測)。
func joinContinuations(src string) []string {
	raw := strings.Split(src, "\n")
	var out []string
	var buf string
	for _, line := range raw {
		if buf != "" {
			buf += " " + strings.TrimSpace(line)
		} else {
			buf = line
		}
		t := strings.TrimSpace(buf)
		if strings.HasSuffix(t, ",") || strings.HasSuffix(t, "(") {
			continue
		}
		out = append(out, buf)
		buf = ""
	}
	if buf != "" {
		out = append(out, buf)
	}
	return out
}

func parseRailsFile(src string, models map[string]*railsModel) {
	var cur *railsModel
	for _, line := range joinContinuations(src) {
		if m := reClass.FindStringSubmatch(line); m != nil {
			cur = &railsModel{class: m[1], parent: m[2], fileSrc: src}
			models[m[1]] = cur
			continue
		}
		if cur == nil {
			continue
		}
		if m := reTableName.FindStringSubmatch(line); m != nil {
			cur.tableName = m[1]
		}
		if reAbstract.MatchString(line) {
			cur.abstract = true
		}
		if reCallback.MatchString(line) {
			cur.hasCallback = true
		}
		m := reAssoc.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rest := m[3]
		a := railsAssoc{kind: m[1], name: m[2]}
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
		cur.assocs = append(cur.assocs, a)
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

// tableOf はモデルのテーブル名(STI は親のテーブル)。
// abstract な親(ApplicationRecord 等)は STI の対象にしない — ここを貫通させると
// 全モデルが application_records に融合する(Mastodon 実測)。
func tableOf(models map[string]*railsModel, class string) string {
	seen := map[string]bool{}
	for c := class; ; {
		m, ok := models[c]
		if !ok || seen[c] {
			return tableize(demodulize(class))
		}
		seen[c] = true
		if m.tableName != "" {
			return m.tableName
		}
		if p, isModel := models[m.parent]; isModel && !p.abstract && m.parent != "ApplicationRecord" {
			c, class = m.parent, m.parent
			continue
		}
		return tableize(demodulize(m.class))
	}
}

func railsToScan(dir string, models map[string]*railsModel) *ScanResult {
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

	// dependent: を宣言している側(親)を先に索引化: 子モデル名 → 親モデル名
	cascadeParents := map[string]map[string]bool{} // child class → set(parent class)
	for _, pc := range classes {
		p := models[pc]
		for _, a := range p.assocs {
			if (a.kind != "has_many" && a.kind != "has_one") || !a.dependent || a.through {
				continue
			}
			child := a.className
			if child == "" {
				child = classify(a.name)
			}
			if cascadeParents[child] == nil {
				cascadeParents[child] = map[string]bool{}
			}
			cascadeParents[child][pc] = true
		}
	}

	addFK := func(childClass, parentClass, col string, notNull bool, note string) {
		child := tableOf(models, childClass)
		parent := tableOf(models, parentClass)
		if child == parent {
			return // 自己参照はグラフ層でも落とすが、ここでも作らない
		}
		rule := "NO ACTION"
		if cascadeParents[childClass][parentClass] {
			rule = "CASCADE"
		}
		name := fmt.Sprintf("ar:%s.%s", underscore(childClass), col)
		if note != "" {
			name += "(" + note + ")"
		}
		res.FKs = append(res.FKs, FK{
			Constraint: name, ChildTable: child, ChildCols: []string{col},
			ParentTable: parent, DeleteRule: rule, AllNotNull: notNull,
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
						target := pa.className
						if target == "" {
							target = classify(pa.name)
						}
						if target == cc {
							addFK(cc, pc, col, !a.optional, "polymorphic")
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
			target := a.className
			if target == "" {
				target = classify(a.name)
			}
			addFK(cc, target, col, !a.optional, "")
		}
	}

	// callback を持つモデルの他モデル言及は、宣言外の結合の疑いとして注記だけする。
	// エッジにはしない — ファイル粒度のヒューリスティックで、偽エッジは信頼を壊すため。
	for _, cc := range classes {
		m := models[cc]
		if !m.hasCallback {
			continue
		}
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
			if _, isModel := models[c]; isModel && !declared[c] && !seen[c] {
				mentions = append(mentions, c)
				seen[c] = true
			}
		}
		if len(mentions) > 0 {
			sort.Strings(mentions)
			res.Notes = append(res.Notes,
				fmt.Sprintf("%s は callback を持ち、宣言外の %s への言及がある(結合の疑い — ファイル粒度のヒント)",
					cc, strings.Join(mentions, ", ")))
		}
	}

	for t := range tables {
		res.Tables = append(res.Tables, t)
	}
	sort.Strings(res.Tables)
	return res
}
