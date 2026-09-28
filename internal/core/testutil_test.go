package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// newStoreForTest は t.TempDir() に Init 済みのワークスペースを作り、開いた
// *Store を返す。t.Cleanup で Close する。internal/core/challenge_test.go・
// internal/core/activity_test.go・internal/core/actor_test.go が共有する。
func newStoreForTest(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatalf("Init() setup error = %v", err)
	}
	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace() setup error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// snapshotDir はワークスペース dir 配下の相対パス・サイズ・更新時刻・内容の
// ハッシュ相当（ここではサイズ+mtime+パスの組で十分。宣言の検証失敗が
// 「何も起動・変更しない」ことを確かめるための軽量な比較に使う）を1つの
// 文字列にして返す。agent_declaration_test.go・connectors_declaration_test.go
// が共有する（LoadAgentDeclaration・LoadConnectorsDeclaration は宣言の検証に
// 失敗してもファイルシステムに書き込まないことを確かめる）。
func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%s|dir=%v|size=%d|mtime=%d", rel, info.IsDir(), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotDir(%s): %v", dir, err)
	}
	sort.Strings(lines)
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
