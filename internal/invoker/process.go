package invoker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// waitDelayAfterCancel は Cancel（プロセスグループへの SIGKILL）を発行して
// から、I/O のパイプを強制的に閉じて Wait を返すまで待つ上限
// （internal/adapters/github/run.go と同じ値・同じ理由）。
const waitDelayAfterCancel = 5 * time.Second

// runResult は claude を1回起動した結果。
type runResult struct {
	stdout   []byte
	stderr   []byte
	timedOut bool
	// launchFailed は「起動そのものができなかった」（cmd.Start が失敗した）
	// ことを表す。timedOut・非0終了とは区別する（§結果の判別 の1行目と2行目
	// を混同しないため）。
	launchFailed bool
	err          error
}

// runClaude は claudePath を args で起動し、stdin を渡して stdout・stderr を
// 集め、timeout を超えたらプロセスグループごと SIGKILL してタイムアウト
// 扱いにする（internal/adapters/github/run.go と同じ方式。プロセスグループに
// することで孫プロセスも道連れに終了させる）。
func runClaude(ctx context.Context, claudePath, workspace string, args []string, stdin []byte, timeout time.Duration, env []string) runResult {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, claudePath, args...)
	cmd.Dir = workspace
	// env が nil なら親の環境をそのまま引き継ぐ（os/exec の既定）。委譲の起動だけが目印つきの環境を渡す。
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else if err != nil {
			return err
		}
		return nil
	}
	cmd.WaitDelay = waitDelayAfterCancel

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	timedOut := err != nil && ctx.Err() == nil && errors.Is(cctx.Err(), context.DeadlineExceeded)
	// cmd.Process は Start が成功したときだけ非nilになる（os/exec の契約）。
	// timedOut のときはプロセスは開始できているので、launchFailed とは
	// 区別する。
	launchFailed := err != nil && !timedOut && cmd.Process == nil

	return runResult{
		stdout:       stdout.Bytes(),
		stderr:       stderr.Bytes(),
		timedOut:     timedOut,
		launchFailed: launchFailed,
		err:          err,
	}
}
