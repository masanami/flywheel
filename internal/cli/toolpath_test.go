package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// このファイルは、偽の gh・claude の場所を「テストごと」に渡すための仕組み（flywheel#578）。
// t.Setenv("PATH") はプロセス全体の環境を書き換えるので t.Parallel と両立しない。parallelTools で
// 並列にしたテストは、偽のツールの置き場（ディレクトリ）をプロセスの PATH ではなく t ごとの一覧に持ち、
// run には lookPathFor(t) を渡す（本番側の差し込み口は Args.LookPath）。parallelTools を呼ばない
// テストは従来どおり t.Setenv で PATH を書き換える。

var testToolDirs sync.Map // *testing.T → *toolDirList

type toolDirList struct {
	mu   sync.Mutex
	dirs []string // 後から足したものが先
}

func (l *toolDirList) add(dir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dirs = append([]string{dir}, l.dirs...)
}

func (l *toolDirList) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.dirs...)
}

// parallelTools は t の偽のツールの置き場を t ごとの一覧に切り替え、needsTTY でなければ t.Parallel にする。
// 疑似端末の子プロセス（runConfirmedChild）を起動するテストは並列にしない（Linux で並列の
// pty.StartWithAttrs が fork/exec: operation not permitted で落ちた）。並列でないサブテストは、
// 親が並列のサブテストを解放する前に 1 つずつ直列に走る。
func parallelTools(t *testing.T, needsTTY bool) {
	t.Helper()
	if !needsTTY {
		t.Parallel()
	}
	testToolDirs.Store(t, &toolDirList{})
	t.Cleanup(func() { testToolDirs.Delete(t) })
}

// addToolDir は dir を t の偽のツールの置き場として先頭に足す。parallelTools のテストでは t ごとの一覧へ、
// そうでなければ t.Setenv で PATH の先頭へ足す。
func addToolDir(t *testing.T, dir string) {
	t.Helper()
	if l, ok := testToolDirs.Load(t); ok {
		l.(*toolDirList).add(dir)
		return
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// lookPathFor は run に渡す実行ファイルの探し方。parallelTools のテストでは t の置き場を先に探し、
// 無ければ PATH（番兵の gh・claude が先頭にある）。そうでなければ nil（exec.LookPath）。
func lookPathFor(t *testing.T) lookPathFunc {
	t.Helper()
	l, ok := testToolDirs.Load(t)
	if !ok {
		return nil
	}
	list := l.(*toolDirList)
	return func(file string) (string, error) {
		for _, dir := range list.snapshot() {
			p := filepath.Join(dir, file)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
				return p, nil
			}
		}
		return exec.LookPath(file)
	}
}

// childPathEnv は子プロセスへ足す PATH の環境変数（"PATH=…"）。parallelTools のテストでなければ空文字。
func childPathEnv(t *testing.T) string {
	t.Helper()
	l, ok := testToolDirs.Load(t)
	if !ok {
		return ""
	}
	dirs := l.(*toolDirList).snapshot()
	return "PATH=" + strings.Join(append(dirs, os.Getenv("PATH")), string(os.PathListSeparator))
}

// writeExecutable は偽のツールのスクリプトを書く。並列のテストが別の goroutine で子プロセスを起動していると、
// 書き込み中のファイルを子プロセスが（exec の前に）開いたまま持ち、実行が "text file busy" で失敗することが
// ある（Linux）。書いて閉じる間だけ fork を止めて避ける。
func writeExecutable(path string, data []byte) error {
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	return os.WriteFile(path, data, 0o755)
}
