package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git log -M --name-status(新しい順)。app/old.rb → app/mid.rb → app/models/new.rb と
// 2 回改名されたファイルの履歴は、全部今の名前に寄る(#51)。
func TestParseChurnLogFollowsRenames(t *testing.T) {
	log := strings.Join([]string{
		"M\tapp/models/new.rb",
		"",
		"R100\tapp/mid.rb\tapp/models/new.rb",
		"",
		"M\tapp/mid.rb",
		"M\tapp/other.rb",
		"",
		"R095\tapp/old.rb\tapp/mid.rb",
		"",
		"A\tapp/old.rb",
	}, "\n")
	got := parseChurnLog(log)
	// 変更 1 + 改名 1 + 旧名での変更 1 + 改名 1 + 追加 1 = 5
	if got["app/models/new.rb"] != 5 || got["app/other.rb"] != 1 {
		t.Errorf("counts = %v", got)
	}
	for _, old := range []string{"app/old.rb", "app/mid.rb"} {
		if got[old] != 0 {
			t.Errorf("旧パス %s に数が残っている: %v", old, got)
		}
	}
}

// 実際の git で -M が効くこと(移動前の履歴を落とさない)。
func TestLoadChurnRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git が無い")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com",
			"-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("models/actions.php", "<?php\nclass Actions extends CActiveRecord {}\n// 1\n")
	git("add", ".")
	git("commit", "-q", "-m", "add")
	write("models/actions.php", "<?php\nclass Actions extends CActiveRecord {}\n// 2\n")
	git("commit", "-q", "-am", "edit")
	if err := os.MkdirAll(filepath.Join(dir, "modules/actions/models"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("mv", "models/actions.php", "modules/actions/models/Actions.php")
	git("commit", "-q", "-m", "move to module")
	write("modules/actions/models/Actions.php", "<?php\nclass Actions extends CActiveRecord {}\n// 3\n")
	git("commit", "-q", "-am", "edit after move")

	counts, err := LoadChurn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := counts["modules/actions/models/Actions.php"]; got != 4 {
		t.Errorf("移動前の変更を含めて 4 回のはず: %d(%v)", got, counts)
	}
}

// モデルファイルを対応づけられないユニットは churn 0 ではなく未計測(#51)。
func TestCandidatesUnmeasuredChurn(t *testing.T) {
	a := Analyze(partitionFixture(), 5)
	// posts と badges だけモデルファイルがある。badges は変更 0 回(計測済みの 0)
	cands := BuildCandidates(a,
		map[string]int{"posts": 50},
		map[string]int{"posts": 1, "badges": 1},
		map[string]float64{})
	var sawUnmeasured bool
	for _, c := range cands {
		switch {
		case c.Churn == nil:
			sawUnmeasured = true
			if c.ChurnFiles != 0 {
				t.Errorf("%s: 未計測なのにファイル数 %d", c.Unit, c.ChurnFiles)
			}
		case sawUnmeasured:
			t.Errorf("計測できた %s が未計測のユニットより後ろにいる: %+v", c.Unit, cands)
		}
		if c.Unit == "badges" && (c.Churn == nil || *c.Churn != 0) {
			t.Errorf("badges は計測済みの 0: %+v", c)
		}
	}
	if !sawUnmeasured {
		t.Fatalf("未計測のユニットが無い(フィクスチャが前提を満たしていない): %+v", cands)
	}

	var b strings.Builder
	WriteCandidates(&b, cands, 50)
	out := b.String()
	if !strings.Contains(out, "churn を測れたユニット") || !strings.Contains(out, "churn -") {
		t.Errorf("表示に計測数 / 未計測の印が無い:\n%s", out)
	}

	// --churn 無し(--criticality だけ)なら全ユニット未計測
	for _, c := range BuildCandidates(a, nil, nil, map[string]float64{"orders": 9}) {
		if c.Churn != nil {
			t.Errorf("--churn 無しで churn が入っている: %+v", c)
		}
	}
}
