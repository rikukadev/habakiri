// contract.go: 橋の作業リストから FK DROP マイグレーションのスケルトンを生成する
// (--emit-contract)。分析 → 実施の人手転記(写し間違いの温床)を消す。
//
// 前提ゲート(参照ゼロの確認・実測共起の確認)は SQL コメントとして同梱する。
// 「文章で禁じたものは、機械でも禁じる」の逆版 — 手順書をコードに埋める。
package main

import (
	"fmt"
	"io"
	"strings"
)

// WriteContract は切断レベル順にマイグレーションのスケルトンを書き出す。
// dialect は "mysql" / "postgres"(静的ソース由来のときは mysql 形式 + 注記)。
func WriteContract(w io.Writer, a *Analysis, dialect string) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }

	p("-- habakiri --emit-contract: 橋の FK DROP スケルトン(%s / %d 本)", a.Schema, len(a.Bridges))
	p("-- 実行順はレベル昇順。各ゲートを通過してから流すこと。")
	p("")

	for _, b := range a.Bridges {
		p("-- ============================================================")
		p("-- 橋: %s × %s(レベル %d / 重み %.0f / 分離後 %d ↔ %d テーブル)", b.A, b.B, b.CutLevel, b.Weight, b.SideASize, b.SideBSize)
		p("-- 切断後に書くもの: %s", b.Pattern)
		for _, fk := range b.FKs {
			cols := strings.Join(fk.ChildCols, ", ")
			p("--")
			p("-- ゲート1: モノリス側の参照が残っていないこと(削除・更新経路の棚卸し)")
			p("-- ゲート2: 実測共起の確認 — --cooc で %s × %s の対が出ないこと(出るなら同一 tx 依存が残っている)", fk.ChildTable, fk.ParentTable)
			p("-- ゲート3: baseline 更新 — 実施後に --json を取り直して --baseline を差し替える")
			switch dialect {
			case "postgres":
				p("ALTER TABLE %s DROP CONSTRAINT %s;  -- %s(%s) → %s", fk.ChildTable, fk.Constraint, fk.ChildTable, cols, fk.ParentTable)
			default:
				p("ALTER TABLE %s DROP FOREIGN KEY `%s`,  -- %s(%s) → %s", fk.ChildTable, fk.Constraint, fk.ChildTable, cols, fk.ParentTable)
				p("  ALGORITHM=INPLACE, LOCK=NONE;  -- FK DROP はメタデータ操作。失敗したら指定を外して原因を見る")
			}
		}
		p("")
	}
	if dialect == "static" {
		p("-- 注意: 静的ソース(--rails/--yii1)由来のため制約名は ar:/yii1: の合成名。")
		p("-- 実 DB の constraint 名に読み替えてから流すこと(--dsn での再実行を推奨)。")
	}
}
