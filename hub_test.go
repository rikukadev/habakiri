package main

import (
	"strconv"
	"strings"
	"testing"
)

// starFixture: master(次数 8)が閾値 9 に届かず、全体を 1 つの塊に糊付けする。
// 各 leaf は master にだけ繋がる(NOT NULL)。
func starFixture() *ScanResult {
	sc := &ScanResult{Schema: "star"}
	sc.Tables = append(sc.Tables, "master")
	for _, l := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		sc.Tables = append(sc.Tables, l)
		sc.FKs = append(sc.FKs, FK{Constraint: "fk_" + l, ChildTable: l, ChildCols: []string{"master_id"},
			ParentTable: "master", DeleteRule: "NO ACTION", AllNotNull: true})
	}
	return sc
}

func TestNearHubsAndGlueNote(t *testing.T) {
	a := Analyze(starFixture(), 9)
	if len(a.Hubs) != 0 {
		t.Fatalf("閾値 9 では hub にならないはず: %+v", a.Hubs)
	}
	if len(a.NearHubs) != 1 || a.NearHubs[0].Node != "master" || a.NearHubs[0].Degree != 8 {
		t.Fatalf("near_hubs: %+v", a.NearHubs)
	}
	note := ""
	for _, n := range a.Notes {
		if strings.Contains(n, "糊付け") {
			note = n
		}
	}
	if !strings.Contains(note, "master(8)") || !strings.Contains(note, "--hub 8") {
		t.Errorf("糊付けの注と --hub の目安が無い: %q", note)
	}

	// 閾値を下げれば master は hub になり、注は出ない
	b := Analyze(starFixture(), 8)
	if len(b.Hubs) != 1 || len(b.NearHubs) != 0 {
		t.Errorf("--hub 8: hubs=%+v near=%+v", b.Hubs, b.NearHubs)
	}
	for _, n := range b.Notes {
		if strings.Contains(n, "糊付け") {
			t.Errorf("hub を外した後も注が出ている: %s", n)
		}
	}
}

// 塊ができていない(最大グループが過半でない)ときは、高次数ノードがあっても注を出さない。
func TestNoGlueNoteWhenBalanced(t *testing.T) {
	a := Analyze(partitionFixture(), 5)
	for _, n := range a.Notes {
		if strings.Contains(n, "糊付け") {
			t.Errorf("塊が無いのに糊付けの注が出た: %s", n)
		}
	}
}

func TestHubScanThresholds(t *testing.T) {
	cases := map[int]string{149: "149,74,37,18,9", 16: "16,8", 6: "6", 3: "3", 1000: "1000,500,250,125,62,31"}
	for start, want := range cases {
		var parts []string
		for _, v := range HubScanThresholds(start) {
			parts = append(parts, strconv.Itoa(v))
		}
		if got := strings.Join(parts, ","); got != want {
			t.Errorf("HubScanThresholds(%d) = %s, want %s", start, got, want)
		}
	}
}

func TestCLIHubScan(t *testing.T) {
	out := string(runCLI(t, "--rails", "testdata/railsapp", "--hub-scan", "--hub", "12"))
	for _, want := range []string{"■ hub 閾値の比較(--hub-scan)", "      12", "       6"} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
	js := string(runCLI(t, "--rails", "testdata/railsapp", "--hub-scan", "--hub", "12", "--json"))
	if !strings.Contains(js, `"hub_scan"`) || !strings.Contains(js, `"best_modularity"`) {
		t.Errorf("JSON に hub_scan が無い")
	}
	if strings.Contains(string(runCLI(t, "--rails", "testdata/railsapp", "--json")), `"hub_scan"`) {
		t.Error("--hub-scan 無しの JSON に hub_scan が出ている")
	}
}
