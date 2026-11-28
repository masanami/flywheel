package core

// このファイルは、flywheel が統合ブランチを反映先へ上げる前のフルの検査（make gate 等）の規則を持つ。
//
// 接続の宣言のリポジトリに full_check（引数の配列）があるとき、統合先を使う課題の反映の段は、「入れてよい」を返す前に、
// 通す中身（統合ブランチの先頭のコミット）を一時の作業ツリーへ取り出して full_check を 1 回実行する。通らなければ「入れてよい」を
// 返さず、結果 full_check_failed（承認を取り消す）と理由を反映の試行の記録に残す。検査の用意（コミットの取り出し）ができないときも
// 返さず、結果 upstream_failed（承認を保ち、次の反映の段でやり直す）にする。通した中身（land_ref を書いた中身）へ「入れてよい」を
// 返し直すときは、もう一度は実行しない。
//
// タスクの PR の検査は軽い検査（make gate-light）なので、そこで拾えないものはこの検査で拾う。
// コマンドの起動は core の外（internal/invoker）が持ち、一時の作業ツリーは J5 の head を読む口（HeadWorktreeGit）で作る。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// fullCheckTimeout はフルの検査のコマンド 1 回の時間の上限。終わらないコマンドを止めるための値で、遅いが通る検査を
// 失敗（承認の取り消し）にしないよう、make gate の内側の上限（go test の -timeout 20m はパッケージごと。その前後に
// 依存の取得・UI のゲート・e2e が続く）より長くする。
const fullCheckTimeout = 60 * time.Minute

// fullCheckRenewInterval は、検査の用意と実行の間に反映の試行の生存を更新する間隔。試行は検査の前の処理（上流の読み・
// コミットの取り出し）も含めて releaseAttemptStaleAfter を超えうるので、更新して他の呼び出しに閉じられないようにする。
const fullCheckRenewInterval = time.Minute

// FullCheckRequest はフルの検査の起動の入力。Dir は通す中身を取り出した作業ツリー、Command は宣言の引数の配列
// （シェルを介さずに起動する）。
type FullCheckRequest struct {
	Dir     string
	Command []string
	Timeout time.Duration
}

// FullCheckOutcome はフルの検査の結果。Passed は終了コード 0 で終わったことだけを表し、Detail は通らなかった理由
// （終了の仕方と出力の末尾）。
type FullCheckOutcome struct {
	Passed bool
	Detail string
}

// FullCheckRunner はフルの検査のコマンドを起動する口。起動できない・時間切れ・非 0 の終了は Passed=false で返す。
type FullCheckRunner interface {
	RunFullCheck(ctx context.Context, req FullCheckRequest) FullCheckOutcome
}

// fullCheckBeforeLand は、反映の操作 row の中身 passRef へ「入れてよい」を返す前のフルの検査を行う。blocked が nil なら
// 返してよい（検査が要らない・通った）。nil でなければ返さず、その結果と理由で試行を終える。
func (s *Store) fullCheckBeforeLand(ctx context.Context, row *operationRow, opts ReleaseExecOptions, run *releaseRun, passRef string) (blocked *releaseJudgment, retErr error) {
	decl := releaseRepoFor(opts, row)
	if decl == nil || len(decl.FullCheck) == 0 || run.row.LandRef == passRef {
		return nil, nil
	}
	var integ bool
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		u, err := challengeUsesIntegration(ctx, tx, row.ChallengeID)
		integ = err == nil && u != nil && *u
		return err
	}); classifyReadWriteErr(err) != nil {
		return nil, classifyReadWriteErr(err)
	}
	if !integ {
		return nil, nil // 統合先を使わない課題は、タスクの PR でフルの検査を回す。
	}
	command := strings.Join(decl.FullCheck, " ")
	checkCtx, stopRenewal, err := s.keepReleaseAttemptAlive(ctx, opts)
	if err != nil {
		return nil, err
	}
	// 戻る前に更新を止め、もう一度更新する。試行が閉じられていたら errExecutionNotOpen を返し、検査の結果は使わない
	// （「入れてよい」を返さない）。通ったときの「入れてよい」の送信には、ここから releaseAttemptStaleAfter が残る。
	defer func() {
		if err := stopRenewal(); err != nil {
			blocked, retErr = nil, err
		}
	}()
	unready := func(why string) (*releaseJudgment, error) {
		return &releaseJudgment{result: ExecutionResultUpstreamFailed,
			detail: "could not prepare the full check (" + command + ") at " + passRef + ": " + why}, nil
	}
	if opts.FullCheck == nil || opts.HeadGit == nil {
		return unready("the full check runner is not available")
	}
	co, reason, err := s.checkoutJ5Commit(checkCtx, J5AutoInput{Connectors: opts.Connectors, HeadGit: opts.HeadGit}, decl.Remote, passRef)
	if err != nil {
		return unready(err.Error())
	}
	if reason != "" {
		return unready(reason)
	}
	head := &j5Head{WorkDir: co.WorkDir, clone: co.clone, ref: co.ref, git: opts.HeadGit}
	defer head.remove(ctx)
	out := opts.FullCheck.RunFullCheck(checkCtx, FullCheckRequest{Dir: co.WorkDir, Command: append([]string(nil), decl.FullCheck...), Timeout: fullCheckTimeout})
	if err := ctx.Err(); err != nil {
		return nil, err // 呼び出し元が取り消した。検査の失敗とは扱わず、試行は interrupted で閉じる。
	}
	if !out.Passed {
		return &releaseJudgment{result: ExecutionResultFullCheckFailed,
			detail: "the full check (" + command + ") failed at " + passRef + ": " + out.Detail}, nil
	}
	return nil, nil
}

// keepReleaseAttemptAlive は、実行中の反映の試行の生存をすぐに更新し、止めるまで間隔ごとに更新し続ける。返す context は
// 更新が失敗する（試行が閉じられた等）と取り消される。stop は更新を止めて最後にもう一度更新し、失敗していればそのエラーを返す
// （呼び出し元の ctx が取り消されていれば、そのエラー）。
func (s *Store) keepReleaseAttemptAlive(ctx context.Context, opts ReleaseExecOptions) (context.Context, func() error, error) {
	now := opts.Now
	if now == nil {
		now = s.currentTime
	}
	every := opts.fullCheckRenewEvery
	if every <= 0 {
		every = fullCheckRenewInterval
	}
	renew := func(ctx context.Context) error {
		return classifyReadWriteErr(s.db.Write(ctx, func(tx *sql.Tx) error {
			return renewOperationExecution(ctx, tx, opts.attemptID, now())
		}))
	}
	if err := renew(ctx); err != nil {
		return nil, nil, err
	}
	checkCtx, cancel := context.WithCancel(ctx)
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				return
			case <-checkCtx.Done():
				return
			case <-ticker.C:
				// 一時的なストアの失敗では検査を止めない。次の間隔でやり直す。
				if err := renew(checkCtx); errors.Is(err, errExecutionNotOpen) {
					cancel()
					return
				}
			}
		}
	}()
	return checkCtx, func() error {
		close(quit)
		<-done
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		return renew(ctx)
	}, nil
}
