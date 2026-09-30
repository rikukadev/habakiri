package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCompareBaseline(t *testing.T) {
	prev := Analyze(partitionFixture(), 5)

	t.Run("同一なら逆行なし", func(t *testing.T) {
		cur := Analyze(partitionFixture(), 5)
		var buf bytes.Buffer
		if CompareBaseline(&buf, prev, cur) {
			t.Errorf("同一入力で逆行判定: %s", buf.String())
		}
		if !strings.Contains(buf.String(), "逆行なし") {
			t.Errorf("逆行なしの表示が無い: %s", buf.String())
		}
	})

	t.Run("新規 FK ペアは逆行", func(t *testing.T) {
		sc := partitionFixture()
		sc.FKs = append(sc.FKs, FK{Constraint: "new", ChildTable: "badges",
			ChildCols: []string{"post_id"}, ParentTable: "posts", DeleteRule: "NO ACTION"})
		cur := Analyze(sc, 5)
		var buf bytes.Buffer
		if !CompareBaseline(&buf, prev, cur) {
			t.Errorf("新規 FK が逆行にならない: %s", buf.String())
		}
		if !strings.Contains(buf.String(), "badges → posts") {
			t.Errorf("新規ペアが表示されない: %s", buf.String())
		}
	})

	t.Run("FK が減るのは前進(exit 0)", func(t *testing.T) {
		sc := partitionFixture()
		sc.FKs = sc.FKs[:len(sc.FKs)-1] // badges→orders の橋を切った世界
		cur := Analyze(sc, 5)
		var buf bytes.Buffer
		if CompareBaseline(&buf, prev, cur) {
			t.Errorf("FK 減少で逆行判定: %s", buf.String())
		}
		if !strings.Contains(buf.String(), "前進") {
			t.Errorf("前進の表示が無い: %s", buf.String())
		}
	})

	t.Run("hub 契約の増加は逆行", func(t *testing.T) {
		sc := partitionFixture()
		sc.FKs = append(sc.FKs, FK{Constraint: "new2", ChildTable: "badges",
			ChildCols: []string{"user_id"}, ParentTable: "users", DeleteRule: "NO ACTION"})
		cur := Analyze(sc, 5)
		var buf bytes.Buffer
		if !CompareBaseline(&buf, prev, cur) {
			t.Errorf("hub 契約増が逆行にならない: %s", buf.String())
		}
		if !strings.Contains(buf.String(), "hub 契約の増加: users") {
			t.Errorf("hub 契約増の表示が無い: %s", buf.String())
		}
	})
}
