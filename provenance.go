// provenance.go: FK の出自(Evidence)と NULL 許容の三値。
//
// 用語:
//
//	Physical — DB スキーマから得た物理 FK。DB が制約を強制している
//	Logical  — ORM の宣言(Yii1 relations() / Rails associations)から得た関係。
//	           物理 FK の存在を意味しない
//
// 同じ関係が複数のソースで見つかっても FK は 1 本で、Evidence が増えるだけ
// (重みは加算しない)。「DB で強制されているか」と「NULL を許すか」は独立の軸で、
// 静的ソースからは後者が分からないことがある — それを NULL可と同じ箱に入れず、
// Unknown として持つ。
package main

import (
	"fmt"
	"path/filepath"
)

// Evidence のソース種別。
const (
	SourcePhysical = "physical"
	SourceYii1     = "logical:yii1"
	SourceRails    = "logical:rails"
)

// Evidence は「この関係がある」と言っている根拠 1 件。
type Evidence struct {
	Source string `json:"source"`
	// Origin: 人間が辿れる位置。physical は constraint 名、logical は ファイル:行。
	Origin string `json:"origin"`
	// 以下はこの証拠自身の主張。統合後の FK の属性は physical 優先で決まるが、
	// 証拠ごとの主張を残しておくと「DB は SET NULL、宣言は dependent: :destroy」
	// のような食い違いを後から読める。
	Constraint string      `json:"-"`
	DeleteRule string      `json:"delete_rule,omitempty"`
	Nullable   Nullability `json:"nullable"`
}

// Nullability は子側の列が NULL を許すか。Unknown を一級の値として持つ。
type Nullability int

const (
	// nullableUnset: 未設定。AllNotNull から導く(Evidence 導入前の構築経路との互換)。
	nullableUnset Nullability = iota
	NullableTrue
	NullableFalse
	// NullableUnknown: ソースから判定できない(Yii1 の relations() は必須性を
	// 宣言できない)。「NULL可と判明した」のではなく「分からない」。
	NullableUnknown
)

func (n Nullability) String() string {
	switch n {
	case NullableTrue:
		return "true"
	case NullableFalse:
		return "false"
	case NullableUnknown:
		return "unknown"
	}
	return ""
}

// MarshalText / UnmarshalText: JSON では "true" / "false" / "unknown" の文字列。
func (n Nullability) MarshalText() ([]byte, error) { return []byte(n.String()), nil }

func (n *Nullability) UnmarshalText(b []byte) error {
	switch string(b) {
	case "true":
		*n = NullableTrue
	case "false":
		*n = NullableFalse
	case "unknown":
		*n = NullableUnknown
	case "":
		*n = nullableUnset
	default:
		return fmt.Errorf("nullable: %q は true / false / unknown のいずれでもない", b)
	}
	return nil
}

// Nullability は三値の NULL 許容。未設定なら AllNotNull から導く。
func (fk FK) Nullability() Nullability {
	if fk.Nullable != nullableUnset {
		return fk.Nullable
	}
	if fk.AllNotNull {
		return NullableFalse
	}
	return NullableTrue
}

// Enforced: DB が制約を強制しているか。physical の証拠があるときだけ true。
func (fk FK) Enforced() bool { return fk.hasSource(SourcePhysical) }

// Logical: ORM の宣言に現れているか。
func (fk FK) Logical() bool {
	for _, ev := range fk.Evidences {
		if len(ev.Source) > 8 && ev.Source[:8] == "logical:" {
			return true
		}
	}
	return false
}

func (fk FK) hasSource(src string) bool {
	for _, ev := range fk.Evidences {
		if ev.Source == src {
			return true
		}
	}
	return false
}

// 重みの理由。数値が同じでも「DB が強制している」のか「宣言にそう書いてある
// だけ」なのか「分からないので暫定」なのかを区別する。
const (
	ReasonCascade         = "cascade"                   // DB の ON DELETE CASCADE
	ReasonNotNull         = "not_null"                  // DB の列が NOT NULL
	ReasonNullable        = "nullable"                  // DB の列が NULL 可
	ReasonLogicalCascade  = "logical_cascade_declared"  // dependent: :destroy 等(DB は強制していない)
	ReasonLogicalRequired = "logical_required_declared" // 必須 belongs_to(検証であって制約ではない)
	ReasonLogicalOptional = "logical_optional_declared" // optional: true
	ReasonUnknown         = "unknown_provisional"       // 判定不能 — 重み 1 は情報不足時の暫定値
)

// weightOf は FK 1 本の結合強度とその理由。
// CASCADE(ライフサイクル共有)> NOT NULL(存在依存)> NULLABLE(弱い参照)。
// Unknown は最弱の 1 に置くが、「弱いと判明した」わけではない。
func weightOf(fk FK) (float64, string) {
	logicalOnly := len(fk.Evidences) > 0 && !fk.Enforced()
	pick := func(physical, logical string) string {
		if logicalOnly {
			return logical
		}
		return physical
	}
	switch {
	case fk.DeleteRule == "CASCADE":
		return 3, pick(ReasonCascade, ReasonLogicalCascade)
	case fk.AllNotNull:
		return 2, pick(ReasonNotNull, ReasonLogicalRequired)
	case fk.Nullability() == NullableUnknown:
		return 1, ReasonUnknown
	default:
		return 1, pick(ReasonNullable, ReasonLogicalOptional)
	}
}

// stampPhysical は DB スキャン由来の FK に physical の証拠を付ける。
func stampPhysical(fk *FK) {
	fk.Nullable = NullableTrue
	if fk.AllNotNull {
		fk.Nullable = NullableFalse
	}
	fk.Evidences = []Evidence{{
		Source: SourcePhysical, Origin: fk.Constraint, Constraint: fk.Constraint,
		DeleteRule: fk.DeleteRule, Nullable: fk.Nullable,
	}}
}

// sourceOrigin は静的ソースの証拠位置(スキャン対象ルートからの相対パス:行)。
// 絶対パスを出さないのは、出力を実行環境に依存させないため。
func sourceOrigin(root, path string, line int) string {
	rel := path
	if r, err := filepath.Rel(root, path); err == nil {
		rel = filepath.ToSlash(r)
	}
	if line > 0 {
		return fmt.Sprintf("%s:%d", rel, line)
	}
	return rel
}
