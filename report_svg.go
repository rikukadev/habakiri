// report_svg.go: --svg の実体。D2(dagre)をライブラリとして埋め込み、
// WriteD2 が生成したスクリプトをその場で SVG に描画する。
// 外部コマンド(d2 / graphviz)への依存なし。1 バイナリで完結する。
//
// 手書きのレイアウトは v0.2 開発中に一度実装して捨てた — 橋ブロック木の
// 放射配置までは書けたが、ラベル衝突・交差最少化で実績あるエンジンに
// 敵わない。図の読みやすさはレイアウトがすべてなので、既製に任せる。
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"oss.terrastruct.com/d2/d2graph"
	"oss.terrastruct.com/d2/d2layouts/d2dagrelayout"
	"oss.terrastruct.com/d2/d2lib"
	"oss.terrastruct.com/d2/d2renderers/d2svg"
	"oss.terrastruct.com/d2/lib/log"
	"oss.terrastruct.com/d2/lib/textmeasure"
	"oss.terrastruct.com/util-go/go2"
)

// WriteSVG は D2 スクリプトを経由して SVG を書き出す。
func WriteSVG(w io.Writer, a *Analysis) error {
	var script bytes.Buffer
	WriteD2(&script, a)

	ruler, err := textmeasure.NewRuler()
	if err != nil {
		return fmt.Errorf("svg: %w", err)
	}
	ctx := log.WithDefault(context.Background())
	renderOpts := &d2svg.RenderOpts{Pad: go2.Pointer(int64(20))}
	diagram, _, err := d2lib.Compile(ctx, script.String(), &d2lib.CompileOptions{
		LayoutResolver: func(string) (d2graph.LayoutGraph, error) {
			return d2dagrelayout.DefaultLayout, nil
		},
		Ruler: ruler,
	}, renderOpts)
	if err != nil {
		return fmt.Errorf("svg: %w", err)
	}
	out, err := d2svg.Render(diagram, renderOpts)
	if err != nil {
		return fmt.Errorf("svg: %w", err)
	}
	_, err = w.Write(out)
	return err
}
