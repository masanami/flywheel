package invoker

// このファイルは core.PromotionGateRunner（昇格の前の検査のコマンドの起動）の実装を持つ。何を回すか・結果の扱いは core が決め、
// ここは引数の配列をシェルを介さず起動して、出力をファイルへ書き、終了コードと出力の末尾を返すだけにする。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/masanami/flywheel/internal/core"
)

// promotionGateTailBytes は返す出力の末尾の上限（バイト）。
const promotionGateTailBytes = 4000

var _ core.PromotionGateRunner = (*Launcher)(nil)

// RunPromotionGate は core.PromotionGateRunner の実装。時間の上限・ctx の取り消しでは、プロセスグループごと SIGKILL する。
func (l *Launcher) RunPromotionGate(ctx context.Context, in core.PromotionGateInput) (core.PromotionGateOutput, error) {
	if len(in.Command) == 0 {
		return core.PromotionGateOutput{}, errors.New("invoker: promotion gate: empty command")
	}
	logFile, err := os.OpenFile(in.LogPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return core.PromotionGateOutput{}, fmt.Errorf("invoker: promotion gate: open the log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	cctx, cancel := context.WithTimeout(ctx, in.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, in.Command[0], in.Command[1:]...)
	cmd.Dir = in.Dir
	cmd.Env = promotionGateEnv(os.Environ())
	// ファイルを直接渡す（パイプにすると、残った孫プロセスが Wait を止める）。
	cmd.Stdout, cmd.Stderr = logFile, logFile
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

	runErr := cmd.Run()
	if err := ctx.Err(); err != nil {
		return core.PromotionGateOutput{}, err
	}
	out := core.PromotionGateOutput{OutputTail: fileTail(logFile, promotionGateTailBytes)}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		out.TimedOut, out.ExitCode = true, -1
	case errors.As(runErr, &exitErr):
		out.ExitCode = exitErr.ExitCode()
	default:
		return core.PromotionGateOutput{}, fmt.Errorf("invoker: promotion gate: %w", runErr)
	}
	return out, nil
}

// promotionGateEnv は検査の子へ渡す環境。flywheel の目印（FLYWHEEL_*。テストが実際のワークスペースを指さないように）と、
// git の作業ツリーの解決を変える変数（GIT_*）を除く。
func promotionGateEnv(environ []string) []string {
	env := []string{}
	for _, kv := range environ {
		if strings.HasPrefix(kv, "FLYWHEEL_") || strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") ||
			strings.HasPrefix(kv, "GIT_INDEX_FILE=") || strings.HasPrefix(kv, "GIT_COMMON_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// fileTail は f の末尾の limit バイトまでを返す（読めなければ空）。
func fileTail(f *os.File, limit int64) string {
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	off := info.Size() - limit
	if off < 0 {
		off = 0
	}
	b, err := io.ReadAll(io.NewSectionReader(f, off, info.Size()-off))
	if err != nil {
		return ""
	}
	return strings.ToValidUTF8(string(b), "")
}
