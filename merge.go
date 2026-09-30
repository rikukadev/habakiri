// merge.go: DB スキャン(Physical)と静的ソース(Logical)を 1 つの ScanResult に
// 合流させる。
//
// 規則:
//   - 同一関係 = 同じ (子テーブル, 子の列, 親テーブル)。列が違えば別の FK
//   - 両方で見つかった関係は FK 1 本のまま Evidence を足す。属性(DeleteRule・
//     NULL 許容)は physical を採る — DB が実際に強制している値だから
//   - 重みは統合後の属性から 1 回だけ出る(証拠が 2 件でも加算しない)
//   - DB 側に見つからなかった宣言は捨てずに残す(Logical Only)。
//     「DB に FK が無い」は観測結果で、「関係が無い」の確認ではない
package main

import (
	"fmt"
	"sort"
	"strings"
)

// yii1WeightNote は単独解析で出す注。合流時は「併用を推奨」が的外れになるので
// mergedUnknownNote に差し替える。
const (
	yii1WeightNote    = "yii1 ソース: relations() に必須性/カスケードの宣言が無いため重みは一律 1。DB スキャン(--dsn)との併用を推奨"
	mergedUnknownNote = "DB に FK が無く宣言だけの関係のうち、NULL 許容が判定不能なものは重み 1 を暫定で置いている — 「弱いと判明した」ではなく情報不足時の値(--show-evidence で unknown_provisional として見える)"
)

// tableResolver は静的ソース側のテーブル名を DB のテーブル名へ写す。
type tableResolver struct {
	exact  map[string]bool
	folded map[string][]string // 小文字 → DB 名(大文字小文字違いの候補)
	prefix string              // 推定した接頭辞(空 = 推定なし)
}

func newTableResolver(dbTables, logicalTables []string) *tableResolver {
	r := &tableResolver{exact: map[string]bool{}, folded: map[string][]string{}}
	for _, t := range dbTables {
		r.exact[t] = true
		k := strings.ToLower(t)
		r.folded[k] = append(r.folded[k], t)
	}

	// 接頭辞の推定。Yii1 の {{post}} は設定ファイルの tablePrefix(tbl_ 等)を
	// 前置した名前が実テーブルになるが、設定は読んでいない。DB 側に
	// 「<接頭辞><論理名>」が揃って存在するときだけ、その接頭辞を採用する。
	// 1 件だけの一致は偶然(item と order_item)でありうるので採らない:
	// 未解決の論理テーブルの過半数かつ 2 件以上を解決できる接頭辞に限る。
	var unresolved []string
	for _, t := range logicalTables {
		if _, ok := r.direct(t); !ok {
			unresolved = append(unresolved, t)
		}
	}
	if len(unresolved) < 2 {
		return r
	}
	count := map[string]int{}
	for _, t := range unresolved {
		lt := strings.ToLower(t)
		seen := map[string]bool{}
		for _, db := range dbTables {
			ldb := strings.ToLower(db)
			if len(ldb) <= len(lt) || !strings.HasSuffix(ldb, lt) {
				continue
			}
			p := ldb[:len(ldb)-len(lt)]
			if !strings.HasSuffix(p, "_") || seen[p] {
				continue
			}
			seen[p] = true
			count[p]++
		}
	}
	var prefixes []string
	for p := range count {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	best := ""
	for _, p := range prefixes {
		if count[p] > count[best] {
			best = p
		}
	}
	if best != "" && count[best] >= 2 && count[best]*2 > len(unresolved) {
		r.prefix = best
	}
	return r
}

// direct: 完全一致、または大文字小文字だけが違う一意な候補。
func (r *tableResolver) direct(t string) (string, bool) {
	if r.exact[t] {
		return t, true
	}
	if c := r.folded[strings.ToLower(t)]; len(c) == 1 {
		return c[0], true
	}
	return "", false
}

// resolve は DB のテーブル名を返す。見つからなければ ok=false(元の名前のまま使う)。
func (r *tableResolver) resolve(t string) (string, bool) {
	if db, ok := r.direct(t); ok {
		return db, true
	}
	if r.prefix != "" {
		if c := r.folded[r.prefix+strings.ToLower(t)]; len(c) == 1 {
			return c[0], true
		}
	}
	return t, false
}

// relationKey は「同一関係」の鍵。列名は大文字小文字を区別しない
// (MySQL の列名は区別しない。Postgres は小文字に畳まれる)。
func relationKey(fk FK) string {
	return fk.ChildTable + "\x00" + strings.ToLower(strings.Join(fk.ChildCols, ",")) + "\x00" + fk.ParentTable
}

// MergeScans は DB スキャンと静的ソースのスキャンを合流させる。
func MergeScans(phys, logic *ScanResult) *ScanResult {
	res := &ScanResult{
		Schema:         phys.Schema + "+" + logic.Schema,
		Merged:         true,
		Dialect:        phys.Dialect,
		CrossFKs:       phys.CrossFKs,
		PhysicalTables: append([]string(nil), phys.Tables...),
	}
	r := newTableResolver(phys.Tables, logic.Tables)

	tables := map[string]bool{}
	for _, t := range phys.Tables {
		tables[t] = true
	}
	var missing []string
	matched := 0
	rename := map[string]string{}
	for _, t := range logic.Tables {
		db, ok := r.resolve(t)
		rename[t] = db
		tables[db] = true
		res.LogicalTables = append(res.LogicalTables, db)
		if ok {
			matched++
		} else {
			missing = append(missing, t)
		}
	}
	sort.Strings(res.LogicalTables)
	name := func(t string) string {
		if db, ok := rename[t]; ok {
			return db
		}
		db, _ := r.resolve(t)
		return db
	}

	// physical を土台にし、logical は鍵が合えば証拠として付くだけ。
	res.FKs = make([]FK, 0, len(phys.FKs)+len(logic.FKs))
	index := map[string]int{}
	for _, fk := range phys.FKs {
		fk.Evidences = append([]Evidence(nil), fk.Evidences...)
		if _, dup := index[relationKey(fk)]; !dup {
			index[relationKey(fk)] = len(res.FKs)
		}
		res.FKs = append(res.FKs, fk)
	}
	for _, fk := range logic.FKs {
		fk.ChildTable, fk.ParentTable = name(fk.ChildTable), name(fk.ParentTable)
		fk.Evidences = append([]Evidence(nil), fk.Evidences...)
		if fk.ChildTable == fk.ParentTable {
			continue // 名前の写しで自己参照になったもの(静的ソース側は元から作らない)
		}
		key := relationKey(fk)
		if i, ok := index[key]; ok {
			res.FKs[i].Evidences = append(res.FKs[i].Evidences, fk.Evidences...)
			continue
		}
		index[key] = len(res.FKs)
		res.FKs = append(res.FKs, fk)
		tables[fk.ChildTable], tables[fk.ParentTable] = true, true
	}

	for t := range tables {
		res.Tables = append(res.Tables, t)
	}
	sort.Strings(res.Tables)

	for _, s := range logic.Suspects {
		s.FromTable, s.ToTable = name(s.FromTable), name(s.ToTable)
		res.Suspects = append(res.Suspects, s)
	}
	if logic.FileTables != nil {
		res.FileTables = map[string]string{}
		for f, t := range logic.FileTables {
			res.FileTables[f] = name(t)
		}
	}

	res.Notes = append(res.Notes, phys.Notes...)
	for _, n := range logic.Notes {
		if n != yii1WeightNote {
			res.Notes = append(res.Notes, n)
		}
	}
	if r.prefix != "" {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"テーブル接頭辞 %q を推定して静的ソースのテーブル名を DB と突合した(DB 側に接頭辞付きの名前が揃っていたため)",
			r.prefix))
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		res.Notes = append(res.Notes, fmt.Sprintf(
			"静的ソースのテーブル %d 個が DB に見つからない: %s — 宣言側の名前のまま残した。存在しないと確認したわけではない(別スキーマ・接頭辞違い・未マイグレーションの可能性)",
			len(missing), strings.Join(missing, ", ")))
	}
	unknown := false
	for _, fk := range res.FKs {
		if !fk.Enforced() && fk.Nullability() == NullableUnknown {
			unknown = true
			break
		}
	}
	if unknown {
		res.Notes = append(res.Notes, mergedUnknownNote)
	}
	return res
}
