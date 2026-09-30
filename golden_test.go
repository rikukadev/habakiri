package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// ゴールデン回帰テスト。
//
// testdata/golden/ は v0.5.0 の実バイナリが出した出力そのもの(Evidence 導入前に
// 固定した)。既存 CLI の出力 — 単独ソースと --cooc 併用の text / JSON / SVG — は
// 以後バイト単位で変えない、という約束をここで機械的に守る。
//
// 意図して出力を変えるときだけ `go test -run TestGolden -update` で取り直す。
var updateGolden = flag.Bool("update", false, "testdata/golden を現在の出力で上書きする")

// goldenCases の postgres は実 DB の代わりに、実 Postgres をスキャンした結果の
// ダンプ(--dump-schema)を食わせる。ダンプと実 DB の一致は e2e/postgres.sh が見る。
var goldenCases = []struct {
	name string
	args []string
}{
	{"rails", []string{"--rails", "testdata/railsapp"}},
	{"yii1", []string{"--yii1", "testdata/yii1app"}},
	{"rails-cooc", []string{"--rails", "testdata/railsapp", "--cooc", "testdata/cooc-rails.txt"}},
	{"yii1-cooc", []string{"--yii1", "testdata/yii1app", "--cooc", "testdata/cooc-yii1.txt"}},
	{"postgres", []string{"--schema-json", "testdata/postgres-fixture.scan.json"}},
}

func runCLI(t *testing.T, args ...string) []byte {
	t.Helper()
	t.Setenv("HABAKIRI_DSN", "")
	var stdout, stderr bytes.Buffer
	if code := run("habakiri", args, &stdout, &stderr); code != 0 {
		t.Fatalf("habakiri %v: exit %d\n%s", args, code, stderr.String())
	}
	return stdout.Bytes()
}

func checkGolden(t *testing.T, file string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", file)
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		// 差分の先頭だけ出す(SVG は 1 行が長いので位置で示す)
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		lo, hiG, hiW := max(0, i-80), min(len(got), i+80), min(len(want), i+80)
		t.Errorf("%s がゴールデンと一致しない(%d バイト目)\n got: …%s…\nwant: …%s…",
			file, i, got[lo:hiG], want[lo:hiW])
	}
}

func TestGolden(t *testing.T) {
	for _, c := range goldenCases {
		t.Run(c.name, func(t *testing.T) {
			checkGolden(t, c.name+".txt", runCLI(t, c.args...))
			checkGolden(t, c.name+".json", runCLI(t, append(c.args[:len(c.args):len(c.args)], "--json")...))
			for flagName, suffix := range map[string]string{
				"--svg": ".svg", "--svg-cut": ".cut.svg", "--svg-partition": ".partition.svg",
			} {
				out := filepath.Join(t.TempDir(), "out.svg")
				runCLI(t, append(c.args[:len(c.args):len(c.args)], flagName, out)...)
				got, err := os.ReadFile(out)
				if err != nil {
					t.Fatal(err)
				}
				checkGolden(t, c.name+suffix, got)
			}
		})
	}
}
