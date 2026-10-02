package core

import (
	"context"
	"strings"
)

// このファイルは、スロットの作業ツリーを検査し払い出す口（SlotGit）と、
// 「宣言の remote と origin の URL を比べる」規則を持つ（親要件チケット #98
// §実行スロット・§プロバイダ worktree。M3H8・M3P45・M3P47）。
//
// 規則は core、入出力（git の起動）は internal/adapters/git が持つ
// （internal/core/upstream.go と internal/adapters/github と同じ形）。core は
// internal/adapters を import せず、internal/cli が実装を組み立てて渡す。

// SlotTree は検査する作業ツリー。BaseClone が "" なら clone のスロット
// （作業ツリーそのものがクローン）、空でなければ worktree のスロットで、
// BaseClone は元のクローンの絶対パス。
type SlotTree struct {
	Path      string
	BaseClone string
}

// SlotTreeState は SlotGit.Inspect の結果。
//
//   - Exists: Path が存在するか。false のとき以降の値は意味を持たない。
//   - PointerOK: worktree のスロットで、作業ツリーの .git のポインタが元の
//     クローンのその作業ツリーを指しているか（clone では常に true）。false のとき
//     PointerProblem に理由を持ち、Dirty・OriginURL は読まない。
//   - Dirty: 未コミットの変更（追跡外のファイルを含む）があるか。
//   - OriginURL: origin の URL そのまま（正規化しない。origin が無ければ ""）。
type SlotTreeState struct {
	Exists         bool
	PointerOK      bool
	PointerProblem string
	Dirty          bool
	OriginURL      string
}

// WorktreeRequest は SlotGit.EnsureWorktree の入力。Ref は
// "refs/heads/<既定ブランチ>"。
type WorktreeRequest struct {
	BaseClone string
	Path      string
	Ref       string
}

// SlotGit はスロットの作業ツリーの検査と払い出しの口。実装は
// internal/adapters/git。どちらも `git fetch`・`git clone` を実行せず、作業ツリーの
// 変更を消さず、ネットワークを使わない。
type SlotGit interface {
	// Inspect は tree の状態を返す。Path が無ければ Exists=false（ディレクトリは
	// 作らない）。git が失敗したときは error を返す。
	Inspect(ctx context.Context, tree SlotTree) (SlotTreeState, error)

	// EnsureWorktree は req.Path が req.BaseClone の作業ツリーとして払い出し済み
	// でなければ `git worktree add --detach <Path> <Ref>` で作る。同じ元のクローン
	// に対する払い出しは、複数のプロセスの間でも直列に実行される。払い出し済みなら
	// 何も実行せず created=false を返す。失敗は error。
	EnsureWorktree(ctx context.Context, req WorktreeRequest) (created bool, err error)
}

// normalizeRemoteURL は origin の URL（https・scp 形式・ssh。`.git` の有無を
// 問わない）を "<owner>/<name>" に正規化する（M3P47）。ホストは見ず、ネット
// ワークも使わない。解釈できなければ ok=false。
func normalizeRemoteURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	var path string
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", false
		}
		path = rest[slash+1:]
	} else {
		colon := strings.Index(s, ":")
		if colon < 0 || strings.Contains(s[:colon], "/") {
			return "", false
		}
		path = s[colon+1:]
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

// remoteMatches は origin の URL が宣言の remote（"<owner>/<name>"）のクローンを
// 指すかを、大文字小文字を無視して返す。
func remoteMatches(originURL, declaredRemote string) bool {
	n, ok := normalizeRemoteURL(originURL)
	return ok && strings.EqualFold(n, declaredRemote)
}
