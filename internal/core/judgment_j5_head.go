package core

// このファイルは、J5 が PR の中身を読むための準備（#465）を持つ（コミットを一時の worktree へ取り出す口は、full_check.go も使う）。J5 の入力にしたチェックの取得で
// 得た PR の head の SHA を固定の値として、次の 2 つを用意する。
//
//   - 固定した SHA の PR の差分（データの区画）
//   - 固定した SHA を取り出した一時の git worktree（J5 の作業ディレクトリ）
//
// 一時の worktree は、宣言の worktree のスロットの元のクローン（clone のスロットは paths の先頭の
// クローン）から、ワークスペースの
// .flywheel/worktrees/.j5-heads/<リポジトリ名>/<pid>-<ホスト>-<乱数>/ へ作る。主クローンの checkout も委譲の子のスロットも
// 変えない。手元のクローンに head のコミットが無い PR は、head の 1 コミットだけを一時の ref
// （refs/flywheel/j5-heads/ の下）へ fetch して取り出す（#509。J5 の head を読むためだけの例外で、
// 他の口は fetch しない）。一時の ref は検証が終わったら消す。fetch に失敗したときは取り出さず、
// そのことを注記して差分だけを渡す。
//
// PR が複数あるときに取り出すのは 1 件（open を優先し、無ければマージ済みの最初の 1 件）で、
// 差分はすべての PR について渡す。作業ディレクトリは 1 つしか持てないため。

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxPullRequestDiffBytes は J5 の入力に含める PR の差分の合計の上限（バイト）。上流の入力の
// 上限（§J2・決定 M3P14）と同じ値・同じ扱い（超えた分を切り、切ったことを注記に書く）にする。
const maxPullRequestDiffBytes = maxUpstreamInputBytes

// PR の中身の区画のラベル（指示文はこの名前で区画を指す）。
const (
	labelPullRequestDiff       = "PR の差分"
	labelPullRequestHeadNotice = "PR の取り出しの注記"
)

// j5HeadsDirName は一時の worktree を置く、.flywheel/worktrees/ の下のディレクトリ。.flywheel の
// .gitignore の worktrees/ の行で追跡から外れる。
const j5HeadsDirName = ".j5-heads"

// j5HeadRemoveTimeout は一時の worktree を消す git に置く時間の上限。消すのは呼び出し元の ctx が
// 取り消された後でも行う。
const j5HeadRemoveTimeout = time.Minute

// j5HeadFetchTimeout は head の fetch（ネットワーク）に置く時間の上限。超えたら fetch できなかった扱いで、
// 差分の区画だけで検証する。
const j5HeadFetchTimeout = 5 * time.Minute

// j5HeadRefPrefix は head の fetch 先の一時の ref の接頭辞。名前は <pid>-<ホスト>-<sha>（sweepJ5Heads が
// 終了したプロセスのものを見分ける）。
const j5HeadRefPrefix = "refs/flywheel/j5-heads/"

// HeadWorktreeGit は J5 の一時の worktree と一時の ref を作って消す口。実装は internal/adapters/git。
// clone は元のクローンの絶対パス。`git clone` は実行しない。`git fetch` を実行してよいのは FetchHead
// だけ（どの PR を・いつ fetch するかは core が決める。J5 と、上げる前のフルの検査が使う）。
type HeadWorktreeGit interface {
	// HasCommit は clone に sha のコミットがあるかを返す（無いことはエラーではなく false）。
	HasCommit(ctx context.Context, clone, sha string) (bool, error)
	// FetchHead は clone の origin から sha の 1 コミットだけを ref へ fetch する（他のブランチ・タグは
	// 取らない）。失敗はエラーで返す。
	FetchHead(ctx context.Context, clone, sha, ref string) error
	// ListHeadRefs は clone の prefix の下の ref の完全な名前を返す。
	ListHeadRefs(ctx context.Context, clone, prefix string) ([]string, error)
	// DeleteHeadRef は clone の ref を消す（無ければ何もしない）。
	DeleteHeadRef(ctx context.Context, clone, ref string) error
	// AddHeadWorktree は path へ sha を detached で取り出した worktree を作る。
	AddHeadWorktree(ctx context.Context, clone, path, sha string) error
	// RemoveHeadWorktree は path の worktree を未コミットの変更ごと消し、clone の worktree の
	// 記録から実体の無いものを除く（`git worktree remove --force` と `git worktree prune`）。
	// path が clone の worktree でなくても path を消す。
	RemoveHeadWorktree(ctx context.Context, clone, path string) error
}

// j5Head は prepareJ5Head の結果。
type j5Head struct {
	// WorkDir は J5 の作業ディレクトリ。空なら取り出していない（ワークスペースで起動する）。
	WorkDir  string
	Sections []JudgmentDataSection
	// DescriptionRead は、成果物の説明文（PR の本文）を 1 件でも取得できたか。
	DescriptionRead bool
	clone           string
	// ref は fetch した一時の ref。空なら fetch していない。
	ref string
	git HeadWorktreeGit
}

// removeJ5Head は一時の worktree を消す。呼び出し元の ctx が取り消された後でも、期限つきで行う。
func removeJ5Head(ctx context.Context, git HeadWorktreeGit, clone, path string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), j5HeadRemoveTimeout)
	defer cancel()
	_ = git.RemoveHeadWorktree(rctx, clone, path)
}

// removeJ5HeadRef は一時の ref を消す。removeJ5Head と同じく、取り消された後でも期限つきで行う。
func removeJ5HeadRef(ctx context.Context, git HeadWorktreeGit, clone, ref string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), j5HeadRemoveTimeout)
	defer cancel()
	_ = git.DeleteHeadRef(rctx, clone, ref)
}

// remove は一時の worktree と一時の ref を消す。消せなければ残りは次の取り出しの sweepJ5Heads が消すので、
// 失敗を返さない（判定の記録は済んでいる）。
func (h *j5Head) remove(ctx context.Context) {
	if h.WorkDir != "" {
		removeJ5Head(ctx, h.git, h.clone, h.WorkDir)
	}
	if h.ref != "" {
		removeJ5HeadRef(ctx, h.git, h.clone, h.ref)
	}
}

// readablePullRequests は PR の中身を読む対象（open かマージ済みで head を持つ PR）を返す。閉じられた
// 未マージの PR は pendingPullRequestURL と同じく対象にしない。
func readablePullRequests(prs []UpstreamPullRequestChecks) []UpstreamPullRequestChecks {
	var out []UpstreamPullRequestChecks
	for _, pr := range prs {
		if pr.HeadSHA != "" && (pr.State == string(artifactStateOpen) || pr.State == string(artifactStateMerged)) {
			out = append(out, pr)
		}
	}
	return out
}

// pullRequestToCheckOut は targets のうち取り出す 1 件を返す。targets は空でない。
func pullRequestToCheckOut(targets []UpstreamPullRequestChecks) UpstreamPullRequestChecks {
	for _, pr := range targets {
		if pr.State == string(artifactStateOpen) {
			return pr
		}
	}
	return targets[0]
}

// formatPullRequestDiffs は取得した差分のブロックを区画の本文へまとめ、上限に収める。
// 切ったときは omitted に元の合計のバイト数を返す（切っていなければ 0）。
func formatPullRequestDiffs(blocks []string) (content string, omitted int) {
	content = strings.Join(blocks, upstreamBlockSeparator)
	if len(content) <= maxPullRequestDiffBytes {
		return content, 0
	}
	return truncateUTF8(content, maxPullRequestDiffBytes), len(content)
}

// fetchPullRequestDiffs は targets の差分の区画の本文と、注記の行を返す。
func fetchPullRequestDiffs(ctx context.Context, src UpstreamDiffSource, targets []UpstreamPullRequestChecks) (content string, notes []string, err error) {
	var blocks []string
	for _, pr := range targets {
		repo, _, ok := parsePullRequestURL(pr.URL)
		if !ok {
			return "", nil, fmt.Errorf("core: malformed pull request url %q", pr.URL)
		}
		if pr.BaseSHA == "" {
			notes = append(notes, fmt.Sprintf("%s の比較の起点（base のコミット）が分からないので、差分はこの入力に無い。", pr.URL))
			continue
		}
		diff, err := src.GetCompareDiff(ctx, repo, pr.BaseSHA, pr.HeadSHA)
		if errors.Is(err, ErrUpstreamDiffUnavailable) {
			notes = append(notes, fmt.Sprintf("%s の差分は上流が返さなかった（大きすぎる等）ので、この入力に無い。", pr.URL))
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("core: fetch the diff of %s: %w", pr.URL, err)
		}
		if strings.TrimSpace(diff) == "" {
			diff = "（差分なし: base と head の間に変更が無い）"
		}
		blocks = append(blocks, fmt.Sprintf("PR: %s\nhead のコミット: %s\n\n%s", pr.URL, pr.HeadSHA, diff))
	}
	content, total := formatPullRequestDiffs(blocks)
	if total > 0 {
		notes = append(notes, fmt.Sprintf("差分は合計 %d バイトあり、先頭の %d バイトまでを入れて残りを省略した。", total, maxPullRequestDiffBytes))
	}
	return content, notes, nil
}

// headClone は repo の PR の head を取り出す元のクローンの絶対パスと、宣言のリポジトリ名を返す。
// worktree のスロットは元のクローン（slots.base）、clone のスロットは paths の先頭のクローン。
// 取り出せないときは理由を返す。
func (s *Store) headClone(conn *ConnectorsDeclaration, repo string) (clone, name, reason string) {
	if conn != nil {
		for _, r := range conn.Repos {
			if !strings.EqualFold(r.Remote, repo) {
				continue
			}
			switch {
			case r.Slots.Provider == string(slotProviderWorktree):
				return filepath.Join(s.workspace, r.Slots.Base), r.Name, ""
			case r.Slots.Provider == string(slotProviderClone) && len(r.Slots.Paths) > 0:
				return filepath.Join(s.workspace, r.Slots.Paths[0]), r.Name, ""
			}
			return "", "", "このリポジトリのスロットに、head を取り出すクローンが無い"
		}
	}
	return "", "", "このリポジトリは接続の宣言に無い"
}

// j5HeadHostTag はこのホストの目印。ワークスペースを共有する別のホスト（コンテナ等）の PID を
// 確かめられないので、一時の worktree の名前に入れて、同じホストのものだけを sweepJ5Heads の対象にする。
func j5HeadHostTag() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(host)))
}

// sweepJ5Heads は、このホストの終了したプロセスの一時の worktree を消す（プロセスが強制終了されると、
// j5Head.remove が呼ばれないため）。実行中のプロセスのものは消さない。
func sweepJ5Heads(ctx context.Context, git HeadWorktreeGit, clone, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if j5HeadEntryOfDeadProcess(e.Name()) {
			removeJ5Head(ctx, git, clone, filepath.Join(dir, e.Name()))
		}
	}
}

// j5HeadEntryOfDeadProcess は、一時の worktree・一時の ref の名前（<pid>-<ホスト>-…）が、このホストの
// 終了したプロセスのものかを返す。
func j5HeadEntryOfDeadProcess(name string) bool {
	pidText, rest, _ := strings.Cut(name, "-")
	pid, err := strconv.ParseInt(pidText, 10, 64)
	if err != nil || !strings.HasPrefix(rest, j5HeadHostTag()+"-") {
		return false
	}
	host, _ := os.Hostname()
	return !isRunProcessAlive(host, pid)
}

// sweepJ5HeadRefs は、このホストの終了したプロセスの一時の ref を消す。
func sweepJ5HeadRefs(ctx context.Context, git HeadWorktreeGit, clone string) {
	refs, err := git.ListHeadRefs(ctx, clone, j5HeadRefPrefix)
	if err != nil {
		return
	}
	for _, ref := range refs {
		if j5HeadEntryOfDeadProcess(strings.TrimPrefix(ref, j5HeadRefPrefix)) {
			removeJ5HeadRef(ctx, git, clone, ref)
		}
	}
}

// addJ5HeadWorktree は sha を取り出した一時の worktree を作り、そのパスを返す。置き場はリポジトリごと
// に分ける（sweepJ5Heads が同じクローンの worktree だけを見るように）。
func (s *Store) addJ5HeadWorktree(ctx context.Context, git HeadWorktreeGit, clone, repoName, sha string) (string, error) {
	if err := ensureWorktreesIgnored(s.workspace); err != nil {
		return "", err
	}
	dir := filepath.Join(s.workspace, worktreeSlotsDir, j5HeadsDirName, repoName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("core: create %s: %w", dir, err)
	}
	sweepJ5Heads(ctx, git, clone, dir)
	path, err := os.MkdirTemp(dir, fmt.Sprintf("%d-%s-*", os.Getpid(), j5HeadHostTag()))
	if err != nil {
		return "", fmt.Errorf("core: create a directory under %s: %w", dir, err)
	}
	if err := git.AddHeadWorktree(ctx, clone, path, sha); err != nil {
		removeJ5Head(ctx, git, clone, path)
		return "", err
	}
	return path, nil
}

// j5Checkout は checkoutJ5Commit が取り出した一時の worktree と、fetch した一時の ref（fetch していなければ空）。
type j5Checkout struct {
	WorkDir, clone, ref string
}

// checkoutJ5Commit は repo（"<owner>/<name>"）の sha のコミットを、一時の worktree へ取り出す。手元のクローンに無ければ、
// その 1 コミットだけを一時の ref へ fetch する（J5 が中身を読むためだけの例外）。取り出せない理由が、読み手に伝えて続けてよいもの
// （接続の宣言にリポジトリが無い・fetch ができない）なら reason に入れ、err は nil（呼び出し元は作業ディレクトリをワークスペースのままにして、
// 取り出せなかったことを注記する）。git の起動や worktree の作成の失敗は err。
func (s *Store) checkoutJ5Commit(ctx context.Context, in J5AutoInput, repo, sha string) (co j5Checkout, reason string, err error) {
	clone, repoName, reason := s.headClone(in.Connectors, repo)
	if reason != "" {
		return co, reason, nil
	}
	sweepJ5HeadRefs(ctx, in.HeadGit, clone)
	has, err := in.HeadGit.HasCommit(ctx, clone, sha)
	if err != nil {
		return co, "", err
	}
	var ref string
	if !has {
		ref = fmt.Sprintf("%s%d-%s-%s", j5HeadRefPrefix, os.Getpid(), j5HeadHostTag(), sha)
		fctx, cancel := context.WithTimeout(ctx, j5HeadFetchTimeout)
		err := in.HeadGit.FetchHead(fctx, clone, sha, ref)
		cancel()
		if err != nil {
			removeJ5HeadRef(ctx, in.HeadGit, clone, ref)
			return co, fmt.Sprintf("手元のクローンに head のコミットが無く、fetch もできなかった（%v）", err), nil
		}
	}
	path, err := s.addJ5HeadWorktree(ctx, in.HeadGit, clone, repoName, sha)
	if err != nil {
		if ref != "" {
			removeJ5HeadRef(ctx, in.HeadGit, clone, ref)
		}
		return co, "", err
	}
	return j5Checkout{WorkDir: path, clone: clone, ref: ref}, "", nil
}

// prepareJ5Head は prs の head（J5 の入力にしたチェックの取得で得た値。ここで取り直さない）の
// 差分の区画と一時の worktree を用意する。PR が無ければ何もしない。差分の取得・worktree の作成に
// 失敗したら NotStarted（upstream_fetch_failed）を返し、run を作らない。呼び出し元は、NotStarted で
// なければ必ず remove を呼ぶ。
func (s *Store) prepareJ5Head(ctx context.Context, in J5AutoInput, challengeID string, prs []UpstreamPullRequestChecks) (*j5Head, *NotStarted) {
	head := &j5Head{git: in.HeadGit}
	targets := readablePullRequests(prs)
	if len(targets) == 0 {
		return head, nil
	}
	failed := func(err error) (*j5Head, *NotStarted) {
		return nil, &NotStarted{ChallengeID: challengeID, Reason: NotStartedUpstreamFetchFailed, Detail: err.Error()}
	}

	diffs, notes, err := fetchPullRequestDiffs(ctx, in.Diffs, targets)
	if err != nil {
		return failed(err)
	}

	pick := pullRequestToCheckOut(targets)
	repo, _, ok := parsePullRequestURL(pick.URL)
	if !ok {
		return failed(fmt.Errorf("core: malformed pull request url %q", pick.URL))
	}
	co, reason, err := s.checkoutJ5Commit(ctx, in, repo, pick.HeadSHA)
	if err != nil {
		return failed(err)
	}
	var checkout []string
	if reason != "" {
		checkout = append(checkout, fmt.Sprintf("%s の head（%s）を取り出せなかった（%s）。作業ディレクトリはワークスペースで、PR の中身は差分の区画でしか読めない。", pick.URL, pick.HeadSHA, reason))
	} else {
		head.WorkDir, head.clone, head.ref = co.WorkDir, co.clone, co.ref
		checkout = append(checkout, fmt.Sprintf("作業ディレクトリは %s の head（%s）を取り出した作業ツリーである。", pick.URL, pick.HeadSHA))
		for _, pr := range targets {
			if pr.URL != pick.URL {
				checkout = append(checkout, fmt.Sprintf("%s の head は取り出していない。中身は差分の区画でしか読めない。", pr.URL))
			}
		}
	}

	if diffs != "" {
		head.Sections = append(head.Sections, JudgmentDataSection{Label: labelPullRequestDiff, Content: diffs})
	}
	if in.Descriptions != nil {
		var content string
		content, head.DescriptionRead = fetchArtifactDescriptions(ctx, in.Descriptions, targets)
		head.Sections = append(head.Sections, JudgmentDataSection{Label: labelArtifactDescription, Content: content})
	}
	head.Sections = append(head.Sections, JudgmentDataSection{Label: labelPullRequestHeadNotice, Content: strings.Join(append(checkout, notes...), "\n")})
	return head, nil
}
