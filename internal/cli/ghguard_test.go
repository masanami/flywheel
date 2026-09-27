package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// realGHGuardExit は番兵の gh の終了コード。偽の gh のどのシナリオとも
// 重ならない値にする。
const realGHGuardExit = 97

// installRealGHGuard は、呼び出されると失敗するだけの番兵の gh を置いた
// ディレクトリを PATH の先頭に足す（m2-github-issue-ingest.md「go test ./...
// は本物の gh と GitHub を呼ばない」。#59 で ingest が gh を呼ぶようになった
// ため、偽の gh を置き忘れたテストが本物の gh へ届かないようにする）。
// 偽の gh を使うテストは t.Setenv でさらにその前へ足すので、番兵より先に
// 解決される。go など gh 以外のコマンドの解決は変えない。
func installRealGHGuard() (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "flywheel-cli-gh-guard-")
	if err != nil {
		return nil, fmt.Errorf("mkdir gh guard: %w", err)
	}
	script := fmt.Sprintf("#!/bin/sh\necho 'cli tests must not invoke the real gh (put a fake gh on PATH)' >&2\nexit %d\n", realGHGuardExit)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write gh guard: %w", err)
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("setenv PATH: %w", err)
	}
	return func() { _ = os.RemoveAll(dir) }, nil
}
