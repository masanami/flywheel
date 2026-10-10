package cli

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/masanami/flywheel/internal/server"
)

// runServe は `flywheel serve` の実装。127.0.0.1 だけへ bind し、SIGINT・SIGTERM で
// 処理中の要求を待ってから終了コード 0 で終わる。待ち受けの規則（Host と Origin の
// 検査など）は internal/server に置き、ここは引数の解釈とエラーコードへの写像だけを持つ。
func runServe(a Args) (any, error) {
	port := server.DefaultPort
	if v, ok := a.Values["port"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 65535 {
			return nil, NewError(CodeUsageError, fmt.Sprintf("--port は 0〜65535 の整数で指定してください: %q", v))
		}
		port = n
	}

	ln, err := server.Listen(port)
	if err != nil {
		return nil, NewError(CodeListenFailed, fmt.Sprintf("127.0.0.1:%d で待ち受けられない: %v", port, err))
	}
	srv := server.New(ln)

	// シグナルは待ち受けを始める前に登録する（登録前に届いた SIGTERM で既定の動作に
	// なり、終了コード 0 にならないのを避ける）。
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	stderr := a.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = fmt.Fprintf(stderr, "listening on %s\n", srv.Addr())

	select {
	case err := <-serveErr:
		if err != nil {
			return nil, NewError(CodeInternalError, err.Error())
		}
	case <-sigs:
		if err := srv.Shutdown(); err != nil {
			return nil, NewError(CodeInternalError, fmt.Sprintf("終了処理に失敗: %v", err))
		}
		<-serveErr
	}
	return textOutput{json: map[string]any{"stopped": true}, text: ""}, nil
}
