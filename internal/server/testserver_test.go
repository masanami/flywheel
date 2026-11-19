package server

import (
	"net"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは、server のテストが共有する支援関数だけを置く（テストの本体は置かない）。

// startServerAt は、時計を now に固定した server を空きポートで起動し、そのポートを返す。終了時に止める。
func startServerAt(t *testing.T, now time.Time, ws ...core.FleetWorkspace) int {
	t.Helper()
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(ln, ws, eventsConfig{now: func() time.Time { return now }})
	errc := make(chan error, 1)
	go func() { errc <- s.Serve() }()
	t.Cleanup(func() {
		_ = s.Shutdown()
		<-errc
	})
	return ln.Addr().(*net.TCPAddr).Port
}
