// report_html.go: 図(SVG)と切断計画を 1 ファイルにまとめた自己完結 HTML。
// 外部 CDN・ネットワーク・JS 依存なし。オフラインで開けて、そのまま共有できる。
package main

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"sort"
	"strings"
)

// WriteHTML はレポートページを書き出す。
func WriteHTML(w io.Writer, a *Analysis) {
	var svg, svgCut bytes.Buffer
	if err := WriteSVG(&svg, a); err != nil {
		svg.Reset()
		svg.WriteString("<p>図の生成に失敗: " + html.EscapeString(err.Error()) + "</p>")
	}
	if err := WriteSVGCut(&svgCut, a); err != nil {
		svgCut.Reset()
		svgCut.WriteString("<p>図の生成に失敗: " + html.EscapeString(err.Error()) + "</p>")
	}

	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format+"\n", args...) }
	esc := html.EscapeString

	p(`<!doctype html><html lang="ja"><head><meta charset="utf-8">`)
	p(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	p(`<title>habakiri — %s</title>`, esc(a.Schema))
	p(`<style>
:root { --ground:#f7f6f3; --surface:#fff; --ink:#1d1c1a; --muted:#6e6a63; --rule:#e3e0da;
  --accent:#a83232; --accent-soft:#f6e8e6; --ok:#3d7a4f; }
@media (prefers-color-scheme: dark) {
  :root { --ground:#17161a; --surface:#1f1e23; --ink:#e8e6e1; --muted:#9a968e;
    --rule:#34323a; --accent:#d96459; --accent-soft:#3a2422; --ok:#7fbf93; }
}
body { background:var(--ground); color:var(--ink); margin:0; padding:2.5rem 1.2rem 4rem;
  font-family:"Hiragino Kaku Gothic ProN",system-ui,sans-serif; font-size:15px; line-height:1.8; }
.wrap { max-width:60rem; margin:0 auto; display:flex; flex-direction:column; gap:1.6rem; }
h1 { font-size:1.5rem; margin:0; }
h2 { font-size:1.1rem; border-top:1px solid var(--rule); padding-top:1.2rem; margin:0.6rem 0 -0.6rem; }
.sub { color:var(--muted); font-size:.85rem; }
figure { margin:0; background:var(--surface); border:1px solid var(--rule); border-radius:6px;
  padding:.8rem; overflow:auto; }
figure svg { display:block; width:100%%; height:auto; min-width:640px; }
table { width:100%%; border-collapse:collapse; font-size:.85rem; }
th,td { text-align:left; padding:.45rem .6rem; border-bottom:1px solid var(--rule); vertical-align:top; }
th { font-size:.7rem; text-transform:uppercase; letter-spacing:.08em; color:var(--muted); }
.tw { overflow-x:auto; background:var(--surface); border:1px solid var(--rule); border-radius:6px; padding:.2rem .4rem; }
code { font-family:ui-monospace,Menlo,monospace; font-size:.85em; }
.w3 { color:var(--accent); font-weight:700; }
.note { background:var(--surface); border:1px solid var(--rule); border-radius:6px;
  padding:.6rem .9rem; font-size:.84rem; }
.note.strong { border-left:3px solid var(--accent); }
.note.weak { border-left:3px solid var(--rule); color:var(--muted); }
.groups { display:grid; grid-template-columns:repeat(auto-fill,minmax(15rem,1fr)); gap:.5rem; }
.group { background:var(--surface); border:1px solid var(--rule); border-left:3px solid var(--ok);
  border-radius:4px; padding:.5rem .7rem; font-size:.8rem; }
</style></head><body><div class="wrap">`)

	p(`<header><h1>habakiri — %s</h1>
<p class="sub">%d テーブル / %d FK · hub %d(次数閾値 %d)· CASCADE 集約 %d 群 · 橋 %d 本 · 孤立 %d</p></header>`,
		esc(a.Schema), a.TableCount, a.FKCount, len(a.Hubs), a.HubThreshold,
		len(a.CascadeGroups), len(a.Bridges), len(a.Isolated))

	p(`<h2>切る前</h2>
<figure>%s<figcaption class="sub">現状の E-R 図(D2/dagre で機械生成)。箱 = テーブル(CASCADE 集約はメンバーを行で列挙、
単独テーブルは FK 列を行で列挙)。実線矢印 = FK(子 → 親、ラベルは FK 列名)、<b>太線 ✂ = 橋(切ると良い場所)</b>、
破線紫 = 宣言外の疑い。色 = 重み(グレー NULL可 / 青 NOT NULL / 朱 CASCADE 級)。</figcaption></figure>`, svg.String())

	p(`<h2>切った後</h2>
<figure>%s<figcaption class="sub">橋をすべて切った世界。離れて浮かぶ塊 = 独立できる単位(ブロック)。
紫の破線が残っていれば、それが切断後もアプリ層に残る結合(API 化の対象)。</figcaption></figure>`, svgCut.String())

	if len(a.Bridges) > 0 {
		p(`<h2>橋 = 切断点(%d 本)</h2><div class="tw"><table>
<tr><th>橋</th><th>重み</th><th>分離後</th><th>FK</th><th>難易度</th></tr>`, len(a.Bridges))
		for _, b := range a.Bridges {
			var fks []string
			for _, fk := range b.FKs {
				nn := "NULL可"
				if fk.AllNotNull {
					nn = "NOT NULL"
				}
				fks = append(fks, esc(fmt.Sprintf("%s.%s(%s) → %s [%s]",
					fk.ChildTable, strings.Join(fk.ChildCols, ","), nn, fk.ParentTable, fk.DeleteRule)))
			}
			p(`<tr><td><code>%s × %s</code></td><td class="w3">%.0f</td><td>%d ↔ %d</td><td><code>%s</code></td><td>%s</td></tr>`,
				esc(b.A), esc(b.B), b.Weight, b.SideASize, b.SideBSize,
				strings.Join(fks, "<br>"), esc(b.Difficulty))
		}
		p(`</table></div>`)
	}

	if len(a.CascadeGroups) > 0 {
		p(`<h2>CASCADE 集約(切らない塊)</h2><div class="groups">`)
		keys := make([]string, 0, len(a.CascadeGroups))
		for k := range a.CascadeGroups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ms := append([]string(nil), a.CascadeGroups[k]...)
			sort.Strings(ms)
			p(`<div class="group"><b>%s</b>(%d)<br><span class="sub">%s</span></div>`,
				esc(k), len(ms), esc(strings.Join(ms, ", ")))
		}
		p(`</div>`)
	}

	if len(a.Islands) > 0 {
		p(`<h2>hub 経由のみで繋がる島(%d)</h2>
<p class="sub">hub 除去後にエッジが残らない塊。hub との参照を値化すれば独立できる — 孤立の次に自由度が高い。</p><div class="groups">`, len(a.Islands))
		for _, is := range a.Islands {
			label := esc(is.Name)
			if is.Tables > 1 {
				label = fmt.Sprintf("%s(+%d)", esc(is.Name), is.Tables-1)
			}
			p(`<div class="group"><b>%s</b></div>`, label)
		}
		p(`</div>`)
	}

	if len(a.Isolated) > 0 {
		p(`<h2>孤立テーブル(%d)</h2><p class="sub">FK なし — 今日でも動かせる。</p>
<p><code>%s</code></p>`, len(a.Isolated), esc(strings.Join(a.Isolated, ", ")))
	}

	if len(a.Notes) > 0 {
		p(`<h2>注</h2>`)
		for _, n := range a.Notes {
			cls := "note"
			if strings.HasPrefix(n, "[強]") {
				cls = "note strong"
			} else if strings.HasPrefix(n, "[弱]") {
				cls = "note weak"
			}
			p(`<div class="%s">%s</div>`, cls, esc(n))
		}
	}

	p(`</div></body></html>`)
}
