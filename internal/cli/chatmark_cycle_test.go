package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/server"
)

// 対話の欄の経路（印あり）でも、承認が成立した後にサイクルを始める処理は main の決まりどおりに働く
// （--no-cycle を付けなければ始める・出力に cycle が出る）。

func chatRunWithStarter(t *testing.T, st *fakeStarter, ws string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	full := append(append([]string{}, args...), "--workspace", ws, "--json")
	var out, errb bytes.Buffer
	code = runWithDeps(full, strings.NewReader(""), &out, &errb, defaultCommands(), runDeps{LookPath: lookPathFor(t), CycleStart: st.start})
	return code, out.String(), errb.String()
}

func TestChatMark_ApproveStartsTheCycleAndPrintsCycle(t *testing.T) {
	ws, id := planAwaitingApproval(t)
	f := openChat(t, ws)
	f.set(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}

	code, stdout, stderr := chatRunWithStarter(t, st, ws, "approve", id)

	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if len(st.calls) != 1 || st.calls[0] != "approve" {
		t.Fatalf("starter calls = %v, want [approve]", st.calls)
	}
	if c := decodeCycle(t, stdout); c["started"] != true {
		t.Errorf("cycle = %v, want started", c)
	}
	if a := lastApproval(t, ws); a.channel != "chat" {
		t.Errorf("channel = %s, want chat", a.channel)
	}
}

func TestChatMark_ApproveWithNoCycleStartsNothing(t *testing.T) {
	ws, id := planAwaitingApproval(t)
	f := openChat(t, ws)
	f.set(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}

	code, stdout, stderr := chatRunWithStarter(t, st, ws, "approve", id, "--no-cycle")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if len(st.calls) != 0 {
		t.Errorf("starter calls = %v, want none", st.calls)
	}
	if c := decodeCycle(t, stdout); c["started"] != false {
		t.Errorf("cycle = %v, want not started", c)
	}
}
