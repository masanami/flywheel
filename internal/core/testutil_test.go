package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"testing"
)

// testStoreActors は t ごとに、newStoreForTest が開いた Store と、fixedActor が固定した actor を覚える。
// fixedActor はパッケージ変数 actorSource を書き換えず、その t の Store ごとに actor を固定する
// （t.Parallel のテストでも使える）。
var testStoreActors = struct {
	mu     sync.Mutex
	stores map[*testing.T][]*Store
	actor  map[*testing.T]*actorSourceFuncs
}{stores: map[*testing.T][]*Store{}, actor: map[*testing.T]*actorSourceFuncs{}}

func registerTestStore(t *testing.T, s *Store) {
	testStoreActors.mu.Lock()
	defer testStoreActors.mu.Unlock()
	if _, ok := testStoreActors.stores[t]; !ok {
		t.Cleanup(func() {
			testStoreActors.mu.Lock()
			defer testStoreActors.mu.Unlock()
			delete(testStoreActors.stores, t)
			delete(testStoreActors.actor, t)
		})
	}
	testStoreActors.stores[t] = append(testStoreActors.stores[t], s)
	if a := testStoreActors.actor[t]; a != nil {
		s.actor = a
	}
}

// newStoreForTest は t.TempDir() に Init 済みのワークスペースを作り、開いた
// *Store を返す。t.Cleanup で Close する。internal/core/challenge_test.go・
// internal/core/activity_test.go・internal/core/actor_test.go が共有する。
func newStoreForTest(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	copyInitTemplate(t, dir)
	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace() setup error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	registerTestStore(t, s)
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

// combinedOutputRetrySignal は mk が作るコマンドを実行する。t.Parallel のテストが同時に子プロセスを起こす負荷の下で、
// macOS では子プロセス（git・sh）が稀に SIGSEGV で終わることがあるため、シグナルでの終了に限って作り直して数回やり直す。
// 終了コードでの失敗はやり直さない。
func combinedOutputRetrySignal(mk func() *exec.Cmd) ([]byte, error) {
	var out []byte
	var err error
	for i := 0; i < 3; i++ {
		out, err = mk().CombinedOutput()
		var ee *exec.ExitError
		if err == nil || !errors.As(err, &ee) {
			return out, err
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() {
			return out, err
		}
	}
	return out, err
}

// runTrueForDeadPID は終了して回収された子の pid を返す。終了コードやシグナルでの終了（*exec.ExitError）は
// 無視する（要るのは「終わって pid が空いた」ことだけ）。
func runTrueForDeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	var ee *exec.ExitError
	if err := cmd.Run(); err != nil && !errors.As(err, &ee) {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}
