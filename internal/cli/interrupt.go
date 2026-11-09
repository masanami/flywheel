package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// interruptContext は、SIGINT・SIGTERM で取り消される ctx を返す（#465）。取り消されると claude は
// 止まり、core は run を記録して、J5 の一時の worktree を消してから返る。最初のシグナルの後は既定の
// 扱いへ戻すので、2 回目のシグナルではすぐに終わる。返す関数は必ず呼ぶ。
func interruptContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}
