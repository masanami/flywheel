// Package server は `flywheel serve` の HTTP サーバである。手元の待ち受け
// （127.0.0.1 だけ）・Host と Origin の検査・終了の口を持つ。状態の読み書きは
// core の公開 API だけを通し、server 自身はストアの外にも状態を持たない
// （docs/features/m4-ui-server.md）。このパッケージはストアのパッケージ・
// internal/cli・internal/invoker を import しない。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 要求を拒否するときのエラーコード。internal/cli のエラーコードの表
// （CodeForbiddenOrigin）と同じ値で、cli のテストが一致を検査する。
const (
	CodeForbiddenOrigin = "forbidden_origin"
	CodeNotFound        = "not_found"
)

// LoopbackHost は手元の待ち受けの bind 先。変える口は持たない。
const LoopbackHost = "127.0.0.1"

// DefaultPort は --port の既定（旧 board の 4317 と並走するため 4318）。
const DefaultPort = 4318

// shutdownTimeout は Shutdown が処理中の要求を待つ上限。超えたら接続を閉じる。
const shutdownTimeout = 5 * time.Second

// Listen は 127.0.0.1:<port> だけに bind する。port 0 は OS が空きを選ぶ。
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp4", net.JoinHostPort(LoopbackHost, strconv.Itoa(port)))
}

// Server は 1 つの待ち受けを配信する。
type Server struct {
	ln       net.Listener
	http     *http.Server
	done     chan struct{}
	doneOnce sync.Once
}

// New は ln（Listen の戻り値）を配信する Server を作る。許可する Host と
// Origin は、ln が実際に bind したポートから導く。
func New(ln net.Listener) *Server {
	s := &Server{ln: ln, done: make(chan struct{})}
	port := ln.Addr().(*net.TCPAddr).Port
	s.http = &http.Server{
		Handler:           newHandler(port),
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.http.RegisterOnShutdown(func() { s.doneOnce.Do(func() { close(s.done) }) })
	return s
}

// Addr は待ち受けのアドレス。
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Done は Shutdown が始まると閉じる。SSE のような長い接続のハンドラは、
// これを見て自分で接続を閉じる（http.Server.Shutdown は長い接続を待つため）。
func (s *Server) Done() <-chan struct{} { return s.done }

// Serve は Shutdown されるまで配信する。Shutdown による正常な終了は nil。
func (s *Server) Serve() error {
	err := s.http.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown は新しい要求を受け付けず、処理中の要求を待ってから返る。
// 待ちが上限（shutdownTimeout）を超えたら残りの接続を閉じて nil を返す
// （終了コード 0 で終わるため。上限を超えた要求は打ち切られる）。
func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := s.http.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return s.http.Close()
	}
	return err
}

func newHandler(port int) http.Handler {
	mux := http.NewServeMux()
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, CodeNotFound, "not found")
	}
	mux.Handle("/", uiHandler(uiFS(), notFound))
	return guard(port, mux)
}

// guard は Host と Origin を検査し、許可されない要求を core（next）を
// 呼ばずに 403 で拒否する（DNS rebinding と他のサイトからの要求への対策）。
func guard(port int, next http.Handler) http.Handler {
	p := strconv.Itoa(port)
	allowedHosts := []string{"localhost:" + p, LoopbackHost + ":" + p}
	allowedOrigins := []string{"http://localhost:" + p, "http://" + LoopbackHost + ":" + p}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !containsFold(allowedHosts, r.Host) {
			writeError(w, http.StatusForbidden, CodeForbiddenOrigin, fmt.Sprintf("host %q は許可されていない", r.Host))
			return
		}
		for _, o := range r.Header.Values("Origin") {
			if !containsFold(allowedOrigins, o) {
				writeError(w, http.StatusForbidden, CodeForbiddenOrigin, fmt.Sprintf("origin %q は許可されていない", o))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func containsFold(list []string, v string) bool {
	for _, a := range list {
		if strings.EqualFold(a, v) {
			return true
		}
	}
	return false
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorDetail{Code: code, Message: message}})
}
