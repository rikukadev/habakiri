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
	"regexp"
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

// bundleFamilies は、静的ソースのテーブル族に当たる DB のテーブル
// (<接頭辞><数字><後ろ>)を族の頂点 1 つに束ねた DB スキャンを返す(#53)。
//
// 束ねるのは一意に決まるときだけ:
//   - 当たる DB のテーブルが 1 つ以上ある
//   - それが静的ソースの具体的なモデルのテーブルと重ならない
//   - 2 つの族に同時に当たらない
//
// 決まらなければ束ねず、族の頂点は DB に見えないまま残る(Edge Diff では判定不能)。
// 束ねた DB の FK は、同じ関係(子・列・親)なら 1 本に畳んで証拠を足す。
func bundleFamilies(phys, logic *ScanResult) (*ScanResult, []string) {
	if len(logic.Families) == 0 {
		return phys, nil
	}
	vertices := map[string]bool{}
	for _, f := range logic.Families {
		vertices[f.Vertex()] = true
	}
	var static []string
	for _, t := range logic.Tables {
		if !vertices[t] {
			static = append(static, t)
		}
	}
	r := newTableResolver(phys.Tables, static)
	claimed := map[string]bool{} // 静的なモデルのテーブル(DB 名に写した後)
	for _, t := range static {
		if db, ok := r.resolve(t); ok {
			claimed[db] = true
		}
	}

	var notes []string
	hits := map[string][]string{} // DB のテーブル → 当たった族の頂点
	members := map[string][]string{}
	for _, f := range logic.Families {
		v := f.Vertex()
		// 接頭辞は宣言のまま、または DB から推定した接頭辞を前置したもの
		prefixes := []string{f.Prefix}
		if r.prefix != "" {
			prefixes = append(prefixes, r.prefix+f.Prefix)
		}
		var found []string
		matched := 0
		for _, p := range prefixes {
			re := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(p) + `\d+` + regexp.QuoteMeta(f.Suffix) + `$`)
			var cand []string
			for _, t := range phys.Tables {
				if re.MatchString(t) {
					cand = append(cand, t)
				}
			}
			if len(cand) > 0 {
				matched++
				found = cand
			}
		}
		switch {
		case matched == 0:
			notes = append(notes, fmt.Sprintf(
				"[判定不能] テーブル族 %s: DB に %s<数字>%s のテーブルが無い — 族の頂点は DB から見えないまま", v, f.Prefix, f.Suffix))
			continue
		case matched > 1:
			notes = append(notes, fmt.Sprintf(
				"[判定不能] テーブル族 %s: DB に接頭辞の有無で 2 通りの候補がある — 束ねていない", v))
			continue
		}
		var clash []string
		for _, t := range found {
			if claimed[t] {
				clash = append(clash, t)
			}
		}
		if len(clash) > 0 {
			notes = append(notes, fmt.Sprintf(
				"[判定不能] テーブル族 %s: 当たる DB のテーブルが具体的なモデルのテーブルと重なる(%s)— 束ねていない",
				v, strings.Join(clash, ", ")))
			continue
		}
		members[v] = found
		for _, t := range found {
			hits[t] = append(hits[t], v)
		}
	}
	rename := map[string]string{}
	var order []string
	for v := range members {
		order = append(order, v)
	}
	sort.Strings(order)
	for _, v := range order {
		ok := true
		for _, t := range members[v] {
			if len(hits[t]) > 1 {
				ok = false
			}
		}
		if !ok {
			notes = append(notes, fmt.Sprintf(
				"[判定不能] テーブル族 %s: 当たる DB のテーブルが別の族にも当たる — 束ねていない", v))
			continue
		}
		for _, t := range members[v] {
			rename[t] = v
		}
		shown := members[v]
		if len(shown) > 5 {
			shown = append(append([]string(nil), shown[:5]...), fmt.Sprintf("他 %d 個", len(members[v])-5))
		}
		notes = append(notes, fmt.Sprintf(
			"テーブル族 %s: DB のテーブル %d 個(%s)を 1 頂点に束ねた", v, len(members[v]), strings.Join(shown, ", ")))
	}
	if len(rename) == 0 {
		return phys, notes
	}

	out := *phys
	name := func(t string) string {
		if v, ok := rename[t]; ok {
			return v
		}
		return t
	}
	seenT := map[string]bool{}
	out.Tables = nil
	for _, t := range phys.Tables {
		if n := name(t); !seenT[n] {
			seenT[n] = true
			out.Tables = append(out.Tables, n)
		}
	}
	sort.Strings(out.Tables)
	out.FKs = nil
	index := map[string]int{}
	for _, fk := range phys.FKs {
		selfRef := fk.ChildTable == fk.ParentTable
		fk.ChildTable, fk.ParentTable = name(fk.ChildTable), name(fk.ParentTable)
		fk.Evidences = append([]Evidence(nil), fk.Evidences...)
		if !selfRef && fk.ChildTable == fk.ParentTable {
			continue // 族の中のテーブル同士(束ねると頂点の内側)
		}
		bundled := rename[fk.ChildTable] != "" || vertices[fk.ChildTable] || vertices[fk.ParentTable]
		if bundled {
			if i, ok := index[relationKey(fk)]; ok {
				out.FKs[i].Evidences = append(out.FKs[i].Evidences, fk.Evidences...)
				continue
			}
			index[relationKey(fk)] = len(out.FKs)
		}
		out.FKs = append(out.FKs, fk)
	}
	out.CrossFKs = nil
	for _, fk := range phys.CrossFKs {
		fk.ChildTable = name(fk.ChildTable)
		out.CrossFKs = append(out.CrossFKs, fk)
	}
	return &out, notes
}

// MergeScans は DB スキャンと静的ソースのスキャンを合流させる。
func MergeScans(phys, logic *ScanResult) *ScanResult {
	phys, famNotes := bundleFamilies(phys, logic)
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
	modelTables := map[string]bool{}
	if logic.ModelTables != nil {
		for _, t := range logic.ModelTables {
			modelTables[t] = true
		}
	}
	for _, t := range logic.Tables {
		db, ok := r.resolve(t)
		rename[t] = db
		tables[db] = true
		if logic.ModelTables == nil || modelTables[t] {
			res.LogicalTables = append(res.LogicalTables, db)
		}
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
		selfRef := fk.ChildTable == fk.ParentTable
		fk.ChildTable, fk.ParentTable = name(fk.ChildTable), name(fk.ParentTable)
		fk.Evidences = append([]Evidence(nil), fk.Evidences...)
		if !selfRef && fk.ChildTable == fk.ParentTable {
			continue // 名前の写しで自己参照になってしまったもの(元の宣言は自己参照ではない)
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
	res.Notes = append(res.Notes, famNotes...)
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
