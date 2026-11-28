package invoker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは core.FullCheckRunner（統合ブランチを反映先へ上げる前のフルの検査の起動）の実装を持つ。
// いつ・どの中身で実行するかと、結果の扱いは core が持つ。

var _ core.FullCheckRunner = (*Launcher)(nil)

// fullCheckOutputTailBytes は、通らなかった理由に残す出力の末尾の上限（バイト）。
const fullCheckOutputTailBytes = 4000

// RunFullCheck は req.Command をシェルを介さずに req.Dir で実行する。時間の上限を超えたらプロセスグループごと終了させる。
// 呼び出し元の git の向き先（GIT_DIR 等）は引き継がない。
func (l *Launcher) RunFullCheck(ctx context.Context, req core.FullCheckRequest) core.FullCheckOutcome {
	if len(req.Command) == 0 {
		return core.FullCheckOutcome{Detail: "the command is empty"}
	}
	cctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, req.Command[0], req.Command[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = withoutGitLocation(os.Environ())
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
	out := &tailBuffer{max: fullCheckOutputTailBytes}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if err == nil {
		return core.FullCheckOutcome{Passed: true}
	}
	reason := err.Error()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		reason = fmt.Sprintf("timed out after %s", req.Timeout)
	}
	if tail := strings.TrimSpace(strings.ToValidUTF8(string(out.buf), "")); tail != "" {
		reason += "\n" + tail
	}
	return core.FullCheckOutcome{Detail: reason}
}

func withoutGitLocation(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR":
			continue
		}
		out = append(out, e)
	}
	return out
}

// tailBuffer は書かれた内容の末尾 max バイトだけを持つ。Stdout と Stderr に同じ値を渡すと、os/exec は書き込みを直列にする。
type tailBuffer struct {
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	return len(p), nil
}
