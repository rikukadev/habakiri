// inflect.go: Rails のテーブル名規約(tableize)のための最小インフレクタ。
// Rails 本家の ActiveSupport::Inflector の完全再現はしない — よくある語形と
// 不規則形だけを持ち、外したときは「モデルに self.table_name を書けば勝つ」
// という逃げ道(source_rails.go 側で対応)がある前提の割り切り。
package main

import (
	"strings"
	"unicode"
)

var irregularPlural = map[string]string{
	"person": "people",
	"child":  "children",
	"man":    "men",
	"woman":  "women",
	"mouse":  "mice",
	"foot":   "feet",
	"tooth":  "teeth",
	"goose":  "geese",
	"datum":  "data",
	"medium": "media",
}

var irregularSingular = func() map[string]string {
	m := map[string]string{}
	for s, p := range irregularPlural {
		m[p] = s
	}
	return m
}()

func pluralize(s string) string {
	if p, ok := irregularPlural[s]; ok {
		return p
	}
	switch {
	case strings.HasSuffix(s, "y") && len(s) > 1 && !isVowel(rune(s[len(s)-2])):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(s, "s"), strings.HasSuffix(s, "x"), strings.HasSuffix(s, "z"),
		strings.HasSuffix(s, "ch"), strings.HasSuffix(s, "sh"):
		return s + "es"
	default:
		return s + "s"
	}
}

func singularize(s string) string {
	if sg, ok := irregularSingular[s]; ok {
		return sg
	}
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ches"), strings.HasSuffix(s, "shes"),
		strings.HasSuffix(s, "xes"), strings.HasSuffix(s, "zes"), strings.HasSuffix(s, "ses"):
		return s[:len(s)-2]
	// "status" / "bonus" / "analysis" のような -us / -ss / -is 語尾は
	// 末尾 s を剥がすと壊れる(Mastodon 実測: status → statu の幽霊化)
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") &&
		!strings.HasSuffix(s, "us") && !strings.HasSuffix(s, "is"):
		return s[:len(s)-1]
	default:
		return s
	}
}

func isVowel(r rune) bool {
	return strings.ContainsRune("aeiou", r)
}

// underscore: "OrderItem" → "order_item"、"HTTPRequest" のような連続大文字は
// 厳密に扱わない(Rails 規約のモデル名は普通の CamelCase が前提)。
func underscore(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// classify: 関連名(複数形スネーク)→ モデル名。"order_items" → "OrderItem"
func classify(assoc string) string {
	parts := strings.Split(assoc, "_")
	parts[len(parts)-1] = singularize(parts[len(parts)-1])
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

// tableize: モデル名 → テーブル名。"OrderItem" → "order_items"
func tableize(model string) string {
	u := underscore(model)
	parts := strings.Split(u, "_")
	parts[len(parts)-1] = pluralize(parts[len(parts)-1])
	return strings.Join(parts, "_")
}
