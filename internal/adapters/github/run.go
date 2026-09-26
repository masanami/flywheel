package github

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// runResult は gh を 1 回起動した結果。timedOut は c.timeout（時間の上限）に
// 達したときだけ true で、呼び出し側の ctx の取り消し・期限切れは ctxErr に
// 入る（core が errors.Is で「呼び出し側の中断」と「gh の時間切れ」を
// 区別できるようにする）。err は cmd.Run() が返した生のエラー
// （終了コード非 0 を含む。gh は 404 のときも終了コード 1 を返すため、
// err の非 nil だけでは失敗とみなさない呼び出し側〈GetIssue〉がある）。
type runResult struct {
	stdout   []byte
	stderr   []byte
	timedOut bool
	ctxErr   error
	err      error
}

// waitDelayAfterCancel は Cancel（プロセスグループへの SIGKILL）を発行して
// から、I/O のパイプを強制的に閉じて Wait を返すまで待つ上限。
const waitDelayAfterCancel = 5 * time.Second

// run は gh を子プロセスとして args で起動し、c.timeout を超えたら
// プロセスグループごと SIGKILL してタイムアウト扱いにする
// （docs/features/m2-github-issue-ingest.md §上流からの取得「gh の 1 回の
// 呼び出しには時間の上限を置き、超えたらその呼び出しを失敗として扱う
// （プロセスを残さない）」）。
func (c *Client) run(ctx context.Context, args ...string) runResult {
	cctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, c.ghPath, args...)
	cmd.SysProcAttr = newProcessGroupSysProcAttr()
	cmd.Cancel = func() error {
		// 負の pid はプロセスグループ全体への送信を意味する
		// （Setpgid: true により、この子プロセスの pid がそのままグループID）。
		// グループが既に無い（子が先に終わり回収済み）ときは、os/exec の
		// 既定の Cancel と同じく os.ErrProcessDone を返し、Wait がこれを
		// 取り消しの失敗として報告しないようにする。
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else if err != nil {
			return err
		}
		return nil
	}
	cmd.WaitDelay = waitDelayAfterCancel
	cmd.Env = ghEnv(os.Environ())

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	return runResult{
		stdout: stdout.Bytes(),
		stderr: stderr.Bytes(),
		// 上限の直前に正常に終わった呼び出しを時間切れとして捨てないよう、
		// 起動の失敗・非 0 終了のとき（err != nil）だけ ctx の状態を見る。
		timedOut: err != nil && ctx.Err() == nil && errors.Is(cctx.Err(), context.DeadlineExceeded),
		ctxErr:   ctxErrIf(ctx, err),
		err:      err,
	}
}

// newProcessGroupSysProcAttr は子プロセスを新しいプロセスグループの
// リーダーにする SysProcAttr を返す（macOS・Linux 両対応。子プロセスが
// さらに孫プロセスを作っても、グループごと SIGKILL できる）。
func newProcessGroupSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func ctxErrIf(ctx context.Context, runErr error) error {
	if runErr == nil {
		return nil
	}
	return ctx.Err()
}

// ghEnv は gh に渡す環境変数。利用者の環境を引き継いだうえで（認証・ホスト・
// プロキシは gh の設定に委ねる）、応答の JSON を着色・整形・ページャーで
// 崩しうる変数だけを打ち消す（CLICOLOR_FORCE・GH_FORCE_TTY が設定されて
// いると gh api は端末でなくても色付きで出力しうる）。後に置いた値が
// 優先される（os/exec は重複した鍵の最後の値を使う）。
func ghEnv(base []string) []string {
	return append(base,
		"GH_FORCE_TTY=",
		"CLICOLOR_FORCE=0",
		"NO_COLOR=1",
		"GH_PAGER=",
		"PAGER=",
		"GH_PROMPT_DISABLED=1",
	)
}
