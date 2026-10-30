package core

import "context"

// このファイルは、ワークスペース（flywheel の運用状態を置くディレクトリ）の Git を
// 扱う口（WorkspaceGit）を定義する（親仕様 m5-tom-migration-and-switchover.md
// §書き出しのコミット・M5P3）。`cycle` の最後の段 `backup` が、書き出しと
// `memory_dir` だけをコミットするために使う。
//
// スロットの作業ツリーを扱う SlotGit（slot_git.go）とは別の型である。規則（どのパスを
// コミットするか・outcome の決め方）は呼ぶ側が持ち、実装（internal/adapters/git）は
// git の起動だけを担う。実装は push・`git add -A`・`git fetch`・`git clone` をしない。
// core は internal/adapters を import しない。

// WorkspaceGit はワークスペースの Git の口。dir はワークスペースのディレクトリ、
// paths は絶対パス。ワークスペースが親のディレクトリのリポジトリの中にあるときは、
// git が見つけたそのリポジトリを対象にする。
type WorkspaceGit interface {
	// IsWorkTree は dir が Git の作業ツリーの中かを返す。作業ツリーでないと
	// git が答えたときは (false, nil)。git が実行できない・ほかの理由で失敗したときは error。
	//
	// 実装は、ここで突き止めた作業ツリーを同じ dir への続く呼び出しで使い回してよい。呼ぶ側は、
	// 1 回の backup の最初にこれを呼ぶ。
	IsWorkTree(ctx context.Context, dir string) (bool, error)

	// InterruptedState は、コミットしてはいけない途中の状態（HEAD がブランチを指さない
	// detached HEAD・rebase の途中・merge の途中）なら、その理由（`detached HEAD`・
	// `rebase in progress`・`merge in progress`）を返す。通常の状態なら空文字列。
	InterruptedState(ctx context.Context, dir string) (reason string, err error)

	// IsIgnored は path が Git の除外の対象（`git check-ignore` が一致する）かを返す。
	IsIgnored(ctx context.Context, dir, path string) (bool, error)

	// HasChanges は paths に前のコミットからの差（`git status --porcelain -- <パス>` が
	// 空でない。追跡外のファイルを含む）があるかを返す。
	HasChanges(ctx context.Context, dir string, paths []string) (bool, error)

	// CommitPaths は paths だけを message でコミットし、作ったコミットの SHA を返す
	// （`git add -- <パス>` の後に `git commit --only -m <message> -- <パス>`）。ほかの
	// ファイルのステージの状態・未コミットの変更は変えない。コミットできるものが無い（実在しない・
	// 空のディレクトリ・除外されたファイルだけ）パスは対象から外す（すべて外れたら error）。パスはリテラルで、作業ツリーのルート自体は
	// 受け付けない。フックの失敗などは、
	// 空でない理由を持つ error で返す。
	CommitPaths(ctx context.Context, dir, message string, paths []string) (sha string, err error)
}
