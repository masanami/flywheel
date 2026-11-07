package core

// このファイルは、J5 が PR の中身を読むための準備（#465）を持つ。J5 の入力にしたチェックの取得で
// 得た PR の head の SHA を固定の値として、次の 2 つを用意する。
//
//   - 固定した SHA の PR の差分（データの区画）
//   - 固定した SHA を取り出した一時の git worktree（J5 の作業ディレクトリ）
//
// 一時の worktree は、宣言の worktree のスロットの元のクローンから、ワークスペースの
// .flywheel/worktrees/.j5-heads/<リポジトリ名>/<pid>-<ホスト>-<乱数>/ へ作る。主クローンの checkout も委譲の子のスロットも
// 変えない。クローンのスロット（元のクローンを持たない）のリポジトリと、手元のクローンに
// head のコミットが無い PR（flywheel は fetch しない）は取り出さず、そのことを注記して差分だけを渡す。
//
// PR が複数あるときに取り出すのは 1 件（open を優先し、無ければマージ済みの最初の 1 件）で、
// 差分はすべての PR について渡す。作業ディレクトリは 1 つしか持てないため。

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// ErrUpstreamDiffUnavailable は、上流がその差分を返さない（大きすぎる・コミットが見つからない）ことを
// 表す。やり直しても変わらないので、J5 を止めずに注記する。
var ErrUpstreamDiffUnavailable = errors.New("the upstream does not return the diff")

// UpstreamDiffSource は J5 の起動の前に core が呼ぶ、差分の取得 IF。実装は
// internal/adapters/github が `gh api` の GET だけで行う。
type UpstreamDiffSource interface {
	// GetCompareDiff は repo（"<owner>/<name>"）の baseSHA と headSHA の共通の祖先から headSHA までの
	// 差分（unified diff）を返す。PR の番号でもブランチでもなくコミットを指すので、取得の間に PR へ
	// push されても、マージ済みでも、base のブランチが消えていても、固定した版の差分になる。
	// 上流が返さないときは errors.Is(err, ErrUpstreamDiffUnavailable) が true のエラーを返す。
	GetCompareDiff(ctx context.Context, repo, baseSHA, headSHA string) (string, error)
}

// HeadWorktreeGit は J5 の一時の worktree を作って消す口。実装は internal/adapters/git。clone は元の
// クローンの絶対パス。どれも `git fetch`・`git clone` を実行しない。
type HeadWorktreeGit interface {
	// HasCommit は clone に sha のコミットがあるかを返す（無いことはエラーではなく false）。
	HasCommit(ctx context.Context, clone, sha string) (bool, error)
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
	clone    string
	git      HeadWorktreeGit
}

// removeJ5Head は一時の worktree を消す。呼び出し元の ctx が取り消された後でも、期限つきで行う。
func removeJ5Head(ctx context.Context, git HeadWorktreeGit, clone, path string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), j5HeadRemoveTimeout)
	defer cancel()
	_ = git.RemoveHeadWorktree(rctx, clone, path)
}

// remove は一時の worktree を消す。消せなければ残りは次の取り出しの sweepJ5Heads が消すので、失敗を
// 返さない（判定の記録は済んでいる）。
func (h *j5Head) remove(ctx context.Context) {
	if h.WorkDir != "" {
		removeJ5Head(ctx, h.git, h.clone, h.WorkDir)
	}
}

// raiseJ5Signal は、受け取った終了のシグナルを自分へ送り直す（テストが差し替える）。
var raiseJ5Signal = func(sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		_ = syscall.Kill(os.Getpid(), s)
	}
}

// guard は一時の worktree を、中断（SIGINT・SIGTERM）でも消せるようにする。シグナルを受けたら返す
// ctx を取り消し（claude が止まって run が記録される）、返す関数で worktree を消してから同じ
// シグナルを送り直す（プロセスの終わり方は変えない）。返す関数は必ず呼ぶ。
func (h *j5Head) guard(ctx context.Context) (context.Context, func()) {
	if h.WorkDir == "" {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	done, stopped := make(chan struct{}), make(chan struct{})
	var got os.Signal
	go func() {
		defer close(stopped)
		select {
		case got = <-sigs:
			cancel()
		case <-done:
		}
	}()
	return ctx, func() {
		h.remove(ctx)
		close(done)
		<-stopped
		cancel()
		signal.Stop(sigs)
		if got == nil {
			select {
			case got = <-sigs:
			default:
			}
		}
		if got != nil {
			raiseJ5Signal(got)
		}
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
		diff, err := src.GetCompareDiff(ctx, repo, pr.BaseSHA, pr.HeadSHA)
		if errors.Is(err, ErrUpstreamDiffUnavailable) {
			notes = append(notes, fmt.Sprintf("%s の差分は上流が返さなかった（大きすぎる等）ので、この入力に無い。", pr.URL))
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("core: fetch the diff of %s: %w", pr.URL, err)
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
// 取り出せないときは理由を返す。
func (s *Store) headClone(conn *ConnectorsDeclaration, repo string) (clone, name, reason string) {
	if conn != nil {
		for _, r := range conn.Repos {
			if !strings.EqualFold(r.Remote, repo) {
				continue
			}
			if r.Slots.Provider != string(slotProviderWorktree) {
				return "", "", "このリポジトリのスロットは元のクローンを持たない（委譲の子の作業用クローンは使わない）"
			}
			return filepath.Join(s.workspace, r.Slots.Base), r.Name, ""
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
	host, _ := os.Hostname()
	for _, e := range entries {
		pidText, rest, _ := strings.Cut(e.Name(), "-")
		pid, err := strconv.ParseInt(pidText, 10, 64)
		if err != nil || !strings.HasPrefix(rest, j5HeadHostTag()+"-") || isRunProcessAlive(host, pid) {
			continue
		}
		removeJ5Head(ctx, git, clone, filepath.Join(dir, e.Name()))
	}
}

// addJ5HeadWorktree は sha を取り出した一時の worktree を作り、そのパスを返す。置き場はリポジトリごと
// に分ける（sweepJ5Heads が同じクローンの worktree だけを見るように）。
func (s *Store) addJ5HeadWorktree(ctx context.Context, git HeadWorktreeGit, clone, repoName, sha string) (string, error) {
	if err := ensureWorktreesIgnored(s.workspace); err != nil {
		return "", err
	}
	dir := filepath.Join(s.workspace, flywheelDirName, "worktrees", j5HeadsDirName, repoName)
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

// prepareJ5Head は prs の head（J5 の入力にしたチェックの取得で得た値。ここで取り直さない）の
// 差分の区画と一時の worktree を用意する。PR が無ければ何もしない。差分の取得・worktree の作成に
// 失敗したら NotStarted（upstream_fetch_failed）を返し、run を作らない。呼び出し元は、NotStarted で
// なければ必ず guard を呼ぶ。
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
	clone, repoName, reason := s.headClone(in.Connectors, repo)
	if reason == "" {
		has, err := in.HeadGit.HasCommit(ctx, clone, pick.HeadSHA)
		if err != nil {
			return failed(err)
		}
		if !has {
			reason = "手元のクローンに head のコミットが無い"
		}
	}
	var checkout []string
	if reason != "" {
		checkout = append(checkout, fmt.Sprintf("%s の head（%s）を取り出せなかった（%s）。作業ディレクトリはワークスペースで、PR の中身は差分の区画でしか読めない。", pick.URL, pick.HeadSHA, reason))
	} else {
		path, err := s.addJ5HeadWorktree(ctx, in.HeadGit, clone, repoName, pick.HeadSHA)
		if err != nil {
			return failed(err)
		}
		head.WorkDir, head.clone = path, clone
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
	head.Sections = append(head.Sections, JudgmentDataSection{Label: labelPullRequestHeadNotice, Content: strings.Join(append(checkout, notes...), "\n")})
	return head, nil
}
