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

// testToolDirs は t.Name() → *toolDirList。parallelTools を呼んだテストの名前で登録し、サブテストは
// 登録がなければ祖先のうち最も近い登録を使う（toolDirsFor）。
var testToolDirs sync.Map

type toolDirList struct {
	mu       sync.Mutex
	dirs     []string // 後から足したものが先
	restrict bool     // true なら dirs にある実行ファイルだけを見つけられる（PATH 上の他のものは見つからない）
	empty    string   // restrict のときに子プロセスの PATH へ足す空のディレクトリ
}

// restrictTo は一覧を先頭 keep 件だけにし、以後は一覧にある実行ファイルしか見つからなくする。
// 戻す関数を返す（祖先の一覧を絞ったサブテストが、終わるときに元へ戻すため）。
func (l *toolDirList) restrictTo(keep int, empty string) (restore func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	oldDirs, oldRestrict, oldEmpty := l.dirs, l.restrict, l.empty
	restore = func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.dirs, l.restrict, l.empty = oldDirs, oldRestrict, oldEmpty
	}
	if keep < len(l.dirs) {
		l.dirs = l.dirs[:keep:keep]
	}
	l.restrict = true
	l.empty = empty
	return restore
}

func (l *toolDirList) isRestricted() (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.restrict, l.empty
}

func (l *toolDirList) add(dir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dirs = append([]string{dir}, l.dirs...)
}

func (l *toolDirList) remove(dir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, d := range l.dirs {
		if d == dir {
			l.dirs = append(l.dirs[:i:i], l.dirs[i+1:]...)
			return
		}
	}
}

// 祖先の一覧を共有するサブテストは t.Parallel にしない（並走すると互いの偽のツールが見える。並列にしたいサブテストは
// 自分で parallelTools を呼ぶ）。
// toolDirsFor は t の偽のツールの置き場の一覧と、t 自身が登録したものか（false なら祖先のもの）を返す。
// 親が parallelTools を呼んだテストのサブテスト（表駆動のケースが t.Run の中で偽のツールを足す形）でも、
// t.Setenv に落ちずに親の一覧を使える。
func toolDirsFor(t *testing.T) (l *toolDirList, own, ok bool) {
	name := t.Name()
	for {
		if v, found := testToolDirs.Load(name); found {
			return v.(*toolDirList), name == t.Name(), true
		}
		i := strings.LastIndex(name, "/")
		if i < 0 {
			return nil, false, false
		}
		name = name[:i]
	}
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
	name := t.Name()
	testToolDirs.Store(name, &toolDirList{})
	t.Cleanup(func() { testToolDirs.Delete(name) })
}

// addToolDir は dir を t の偽のツールの置き場として先頭に足す。parallelTools のテストでは t ごとの一覧へ、
// そうでなければ t.Setenv で PATH の先頭へ足す。
func addToolDir(t *testing.T, dir string) {
	t.Helper()
	if l, own, ok := toolDirsFor(t); ok {
		l.add(dir)
		if !own {
			// 祖先の一覧へ足したサブテストは、終わったら外す（次のサブテストへ前のケースの偽のツールを残さない）。
			t.Cleanup(func() { l.remove(dir) })
		}
		return
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// restrictToolDirs は、偽のツール以外を PATH から見つけられなくする（gh が無い環境を作る）。keep は残す偽のツールの
// 置き場の数（新しい方から。0 なら全て外す）。parallelTools のテストでは t ごとの一覧だけを絞り、そうでなければ
// t.Setenv で PATH を空のディレクトリ（keep が 1 なら PATH の先頭 1 件）だけにする。
func restrictToolDirs(t *testing.T, keep int) {
	t.Helper()
	if l, own, ok := toolDirsFor(t); ok {
		restore := l.restrictTo(keep, t.TempDir())
		if !own {
			// 祖先の一覧を絞ったサブテストは、終わったら元へ戻す（兄弟のサブテストへ漏らさない）。
			t.Cleanup(restore)
		}
		return
	}
	if keep == 0 {
		t.Setenv("PATH", t.TempDir())
		return
	}
	if keep != 1 {
		t.Fatalf("restrictToolDirs: keep = %d, want 0 or 1", keep)
	}
	t.Setenv("PATH", strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0])
}

// lookPathFor は run に渡す実行ファイルの探し方。parallelTools のテストでは t の置き場を先に探し、
// 無ければ PATH（番兵の gh・claude が先頭にある）。そうでなければ nil（exec.LookPath）。
func lookPathFor(t *testing.T) lookPathFunc {
	t.Helper()
	list, _, ok := toolDirsFor(t)
	if !ok {
		return nil
	}
	return func(file string) (string, error) {
		for _, dir := range list.snapshot() {
			p := filepath.Join(dir, file)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
				return p, nil
			}
		}
		if restricted, _ := list.isRestricted(); restricted {
			return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
		}
		return exec.LookPath(file)
	}
}

// childPathEnv は子プロセスへ足す PATH の環境変数（"PATH=…"）。parallelTools のテストでなければ空文字。
func childPathEnv(t *testing.T) string {
	t.Helper()
	l, _, ok := toolDirsFor(t)
	if !ok {
		return ""
	}
	dirs := l.snapshot()
	if restricted, empty := l.isRestricted(); restricted {
		return "PATH=" + strings.Join(append(dirs, empty), string(os.PathListSeparator))
	}
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
