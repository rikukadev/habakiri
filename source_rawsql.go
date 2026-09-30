// source_rawsql.go: 静的ソース(Rails / Yii1)のモデルコードから、
// ORM 宣言を経由しない生 SQL・コマンドビルダの書き込み先テーブルを拾う。
//
// createCommand("UPDATE ...") や ->update('tbl', ...) は relations/belongs_to に
// 一切現れないのに実務の結合そのもの。書き込みは[強]の疑いとして
// Suspect に載せる(偽エッジは作らない哲学のまま、確度は注記で明示)。
package main

import (
	"regexp"
	"sort"
	"strings"
)

var (
	// SQL 文字列中の書き込み文(UPDATE\s+ は updateAll にマッチしない)
	reRawWrite = regexp.MustCompile("(?i)\\b(?:INSERT(?:\\s+IGNORE)?\\s+INTO|UPDATE|DELETE\\s+FROM|REPLACE\\s+INTO)\\s+[`\"']?\\{?\\{?([a-zA-Z0-9_$]+)\\}?\\}?")
	// Yii1 CDbCommand ビルダ: ->insert('tbl'/->update('{{tbl}}'/->delete('tbl'
	reYiiBuilder = regexp.MustCompile(`->\s*(?:insert|update|delete)\(\s*['"]\{?\{?([a-zA-Z0-9_$]+)\}?\}?['"]`)
	// 読み取り: FROM / JOIN の直後のテーブル。DELETE FROM は書き込みなので除く
	reRawRead = regexp.MustCompile("(?i)\\b(DELETE\\s+)?(?:FROM|JOIN)\\s+[`\"']?\\{?\\{?([a-zA-Z0-9_$]+)\\}?\\}?")
	// Yii1 CDbCommand ビルダの読み取り: ->from('tbl') / ->join('tbl', ...)
	reYiiBuilderRead = regexp.MustCompile(`->\s*(?:from|join|leftJoin|rightJoin)\(\s*['"]\{?\{?([a-zA-Z0-9_$]+)\}?\}?`)
)

// extractRawReadTables は生 SQL / コマンドビルダの読み取り先(FROM / JOIN)。
// コメントは読まない。英語の地の文の from にも当たりうるので、呼び出し側で
// モデルのあるテーブルに絞る(extractRawWriteTables と同じ規律)。
func extractRawReadTables(src string) []string {
	src = maskPHPComments(src)
	seen := map[string]bool{}
	for _, m := range reRawRead.FindAllStringSubmatch(src, -1) {
		if m[1] != "" {
			continue // DELETE FROM は書き込み
		}
		seen[strings.ToLower(m[2])] = true
	}
	for _, m := range reYiiBuilderRead.FindAllStringSubmatch(src, -1) {
		seen[strings.ToLower(m[1])] = true
	}
	list := make([]string, 0, len(seen))
	for t := range seen {
		list = append(list, t)
	}
	sort.Strings(list)
	return list
}

// sqlKeyword: reRawWrite の誤爆(コード中の英単語)を減らすため、
// SQL らしさの弱い UPDATE 単独マッチは引用符か {{ }} 付きのみ採用する。
func extractRawWriteTables(src string) []string {
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			seen[t] = true
		}
	}
	for _, m := range reRawWrite.FindAllStringSubmatch(src, -1) {
		head := strings.ToUpper(m[0][:6])
		if strings.HasPrefix(head, "UPDATE") {
			// UPDATE は自然言語にも現れる。テーブル側にクォート/{{}}/アンダースコアの
			// いずれかが無ければ SQL とみなさない(誤爆抑制)。
			if !strings.ContainsAny(m[0], "`\"'{") && !strings.Contains(m[1], "_") {
				continue
			}
		}
		add(m[1])
	}
	for _, m := range reYiiBuilder.FindAllStringSubmatch(src, -1) {
		add(m[1])
	}
	list := make([]string, 0, len(seen))
	for t := range seen {
		list = append(list, t)
	}
	sort.Strings(list)
	return list
}

// braceBody: 開き波括弧位置から対応する閉じまでの本文(近似 — 文字列中の
// 波括弧は数えるが、モデルコードの callback では実用上問題にならない)。
func braceBody(src string, open int) string {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open+1 : i]
			}
		}
	}
	return src[open+1:]
}
