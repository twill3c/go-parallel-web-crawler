// Package tests はリポジトリ全体に対する検査(依存の数・文書とゲートの対応など)を置く。
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// T-002 / N-01: go.mod の直接依存は golang.org/x/net だけ(golang.org/x/mod はこの検査自身の
// ためのテスト専用依存として許す)。DB・外部 API・課金経路を持ち込まないことの機械検査。
// 期待値の出所: SPEC §3 N-01
func TestGoModDirectDependencies(t *testing.T) {
	path := filepath.Join("..", "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		t.Fatalf("parse go.mod: %v", err)
	}
	allowed := map[string]bool{
		"golang.org/x/net": true, // D-04: HTML トークナイザ
		"golang.org/x/mod": true, // この検査のため(テスト専用)
	}
	var offenders []string
	for _, r := range f.Require {
		if r.Indirect {
			continue
		}
		if !allowed[r.Mod.Path] {
			offenders = append(offenders, r.Mod.Path)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("N-01 違反: 許可外の直接依存 %s", strings.Join(offenders, ", "))
	}
	// 陽性対照: 検査が require を実際に読んでいること(空の require 一覧で緑になっていないこと)
	if len(f.Require) == 0 {
		t.Fatalf("go.mod に require が無い — 検査対象が空(HC-041)")
	}
}
