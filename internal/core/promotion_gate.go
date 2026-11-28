package core

// このファイルは、統合ブランチを反映先（main）へ上げる前に回すフルの検査（昇格の前の検査）を持つ。
//
// 統合先を使う課題のタスクの PR は、軽い検査（変えた所だけ）で統合ブランチへ入る。そこで拾えないものを、反映の段が
// 「入れてよい」を返す直前に、通す中身（統合ブランチの先頭）を取り出した一時の worktree で、リポジトリが宣言した
// コマンドを回して拾う。通らなければ「入れてよい」を返さず、反映の試行の記録に理由を残す。
//
// 回すコマンドは、通す中身の中の宣言 .claude-harness/promotion-gate.json（{"command": ["make", "gate"]}。シェルを介さない）で決まる。
// 宣言が無いリポジトリは検査せずに進み、そのことを試行の記録に残す。中身を取り出せない・宣言が読めない・起動できないときは上げずに待つ。
// コマンドの起動は PromotionGateRunner（実装は internal/invoker）が担い、core は起動しない。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// promotionGateDeclarationPath は、昇格の前の検査の宣言の、リポジトリの中の位置。
const promotionGateDeclarationPath = ".claude-harness/promotion-gate.json"

// promotionGateTimeout は昇格の前の検査 1 回の時間の上限。超えたら通らなかった扱いにする。
const promotionGateTimeout = 40 * time.Minute

// PromotionGateInput は昇格の前の検査 1 回の起動の入力。
type PromotionGateInput struct {
	// Dir は作業ディレクトリ（通す中身を取り出した一時の worktree）の絶対パス。
	Dir string
	// Command は起動する引数の配列（1 つ以上）。シェルを介さない。
	Command []string
	// LogPath は出力の全文を書くファイルの絶対パス。
	LogPath string
	// Timeout は時間の上限。
	Timeout time.Duration
}

// PromotionGateOutput は起動できた検査の結果。
type PromotionGateOutput struct {
	// ExitCode は終了コード。シグナルで終わったときは負の値。
	ExitCode int
	// TimedOut は時間の上限で止めたこと。
	TimedOut bool
	// OutputTail は出力（標準出力と標準エラー）の末尾。
	OutputTail string
}

// PromotionGateRunner は昇格の前の検査のコマンドを起動する口。実装は internal/invoker。
// 起動できなかった・ctx が取り消されたときは error。コマンドが非 0 で終わったことと時間切れは error にしない。
type PromotionGateRunner interface {
	RunPromotionGate(ctx context.Context, in PromotionGateInput) (PromotionGateOutput, error)
}

// promotionGateVerdict は昇格の前の検査の判定。
type promotionGateVerdict struct {
	// passed は「入れてよい」を返してよいこと。
	passed bool
	// result は passed が偽のときの試行の結果（gate_failed か gate_unavailable）。
	result ExecutionResult
	// detail は試行の記録に残す理由（passed で空でなければ、「入れてよい」の結果の詳細に残す）。
	detail string
}

func gateUnavailable(format string, args ...any) promotionGateVerdict {
	return promotionGateVerdict{result: ExecutionResultGateUnavailable, detail: "could not run the promotion gate: " + fmt.Sprintf(format, args...)}
}

// runPromotionGate は反映の操作 row の通す中身 sha に昇格の前の検査を回す。統合先を使わない課題は回さずに通す
// （タスクの PR の宛先が反映先なので、PR の品質ゲートがフルになる）。
// 返すエラーは、ストアを読めなかったときと、ctx が取り消されたときだけ。
func (s *Store) runPromotionGate(ctx context.Context, row *operationRow, opts ReleaseExecOptions, sha string) (promotionGateVerdict, error) {
	var integ bool
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		u, err := challengeUsesIntegration(ctx, tx, row.ChallengeID)
		integ = u != nil && *u
		return err
	}); classifyReadWriteErr(err) != nil {
		return promotionGateVerdict{}, classifyReadWriteErr(err)
	}
	if !integ {
		return promotionGateVerdict{passed: true}, nil
	}
	if opts.Gate == nil || opts.HeadGit == nil {
		return gateUnavailable("the gate runner is not available"), nil
	}
	repo, _, ok := parseReleasePullRequestRef(row.Ref.String)
	if !ok {
		return gateUnavailable("the release ref %q is not a pull request", row.Ref.String), nil
	}
	co, reason, err := s.checkoutJ5Commit(ctx, J5AutoInput{HeadGit: opts.HeadGit, Connectors: opts.Connectors}, repo, sha)
	if ctx.Err() != nil {
		return promotionGateVerdict{}, ctx.Err()
	}
	if err != nil {
		return gateUnavailable("check out %s: %v", sha, err), nil
	}
	if reason != "" {
		return gateUnavailable("check out %s: %s", sha, reason), nil
	}
	head := &j5Head{git: opts.HeadGit, clone: co.clone, WorkDir: co.WorkDir, ref: co.ref}
	defer head.remove(ctx)

	command, declared, err := readPromotionGateDeclaration(co.WorkDir)
	if err != nil {
		return gateUnavailable("%s at %s: %v", promotionGateDeclarationPath, sha, err), nil
	}
	if !declared {
		return promotionGateVerdict{passed: true, detail: fmt.Sprintf("no promotion gate is declared (%s is not in %s)", promotionGateDeclarationPath, sha)}, nil
	}

	logDir := filepath.Join(s.workspace, flywheelDirName, "runs", "promotion-gate")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return gateUnavailable("create %s: %v", logDir, err), nil
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("%s-%d.log", formatOperationID(row.ID), s.currentTime().UnixNano()))
	out, err := opts.Gate.RunPromotionGate(ctx, PromotionGateInput{Dir: co.WorkDir, Command: command, LogPath: logPath, Timeout: promotionGateTimeout})
	if ctx.Err() != nil {
		return promotionGateVerdict{}, ctx.Err()
	}
	if err != nil {
		return gateUnavailable("start %q: %v", strings.Join(command, " "), err), nil
	}
	name := strings.Join(command, " ")
	switch {
	case out.TimedOut:
		return promotionGateVerdict{result: ExecutionResultGateFailed, detail: fmt.Sprintf(
			"the promotion gate %q on %s did not finish within %s (output: %s)\n%s", name, sha, promotionGateTimeout, logPath, out.OutputTail)}, nil
	case out.ExitCode != 0:
		return promotionGateVerdict{result: ExecutionResultGateFailed, detail: fmt.Sprintf(
			"the promotion gate %q on %s failed with exit code %d (output: %s)\n%s", name, sha, out.ExitCode, logPath, out.OutputTail)}, nil
	}
	return promotionGateVerdict{passed: true, detail: fmt.Sprintf("the promotion gate %q passed on %s", name, sha)}, nil
}

// readPromotionGateDeclaration は dir の宣言を読む。ファイルが無ければ declared は偽。形が違えばエラー。
func readPromotionGateDeclaration(dir string) (command []string, declared bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(promotionGateDeclarationPath)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var d struct {
		Command []string `json:"command"`
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, false, fmt.Errorf("invalid declaration: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false, errors.New("invalid declaration: data after the object")
	}
	if len(d.Command) == 0 || d.Command[0] == "" {
		return nil, false, errors.New(`invalid declaration: "command" must be a non-empty array`)
	}
	return d.Command, true, nil
}

// releaseAttemptStaleForChallenge は、課題 cid の終わっていない試行 a が止まっているかを返す。統合先を使う課題の試行は
// 昇格の前の検査を回しうるので、生きているプロセスの試行は、経過時間の上限に検査の時間の上限を足して数える。
func releaseAttemptStaleForChallenge(ctx context.Context, tx *sql.Tx, cid int64, a *operationExecutionRow, now time.Time) (bool, error) {
	u, err := challengeUsesIntegration(ctx, tx, cid)
	if err != nil {
		return false, err
	}
	if u != nil && *u {
		now = now.Add(-promotionGateTimeout)
	}
	return releaseAttemptStale(a, now), nil
}
