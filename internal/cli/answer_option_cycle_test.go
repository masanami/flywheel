package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
	"github.com/masanami/flywheel/internal/server"
)

// 選択肢つきの回答（`answer <課題> --option <識別>`）は、main の決まりどおりに働く（長く走るサイクルの、操作の後にサイクルを始める処理と、
// 対話の欄から受けるコマンドの一覧の検査）。選択肢つきの保留は CLI から作れないので、coretest で作る。

func openOptionHold(t *testing.T, ws string) string {
	t.Helper()
	id := holdWithOptionsChallenge(t, ws, "", "awaiting_human")
	createForCase(t, ws) // いま始められるもの（未分類の課題）。回答の後にサイクルを始める理由になる。
	return id
}

func holdAnswerRow(t *testing.T, ws string) []string {
	t.Helper()
	r := coretest.QueryRows(t, ws, `SELECT answer, answer_option, answered_channel, relay_session, relay_quote FROM hold ORDER BY id DESC LIMIT 1`)
	if len(r) != 1 {
		t.Fatalf("hold rows = %v, want 1", r)
	}
	out := make([]string, len(r[0]))
	for i, v := range r[0] {
		out[i] = fmt.Sprint(v)
	}
	return out
}

// 印の無い経路: 成立した後に、--no-cycle を付けなければサイクルを始める（きっかけは answer）。出力に cycle が出る。
func TestAnswerOption_StartsTheCycleAndPrintsCycle(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	id := openOptionHold(t, ws)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}

	code, stdout, stderr := runWithStarter(t, st, "answer", id, "--option", "add", "--relayed", "--session", "S", "--quote", "足して", "--workspace", ws, "--json")

	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if len(st.calls) != 1 || st.calls[0] != "answer" {
		t.Fatalf("starter calls = %v, want [answer]", st.calls)
	}
	if c := decodeCycle(t, stdout); c["started"] != true || c["reason"] != "started" {
		t.Errorf("cycle = %v, want started", c)
	}
	if r := holdAnswerRow(t, ws); r[0] != "足す" || r[1] != "add" {
		t.Errorf("hold = %v, want the chosen label as the answer and answer_option add", r)
	}
}

func TestAnswerOption_WithNoCycleStartsNothing(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	id := openOptionHold(t, ws)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}

	code, stdout, stderr := runWithStarter(t, st, "answer", id, "--option", "add", "--no-cycle", "--relayed", "--session", "S", "--quote", "足して", "--workspace", ws, "--json")

	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if len(st.calls) != 0 {
		t.Errorf("starter calls = %v, want none", st.calls)
	}
	if c := decodeCycle(t, stdout); c["started"] != false || c["reason"] != "disabled" {
		t.Errorf("cycle = %v, want not started (disabled)", c)
	}
	if r := holdAnswerRow(t, ws); r[1] != "add" {
		t.Errorf("hold = %v, want answer_option add (the answer is recorded)", r)
	}
}

// 本人確認の失敗（--relayed なし・端末でもない）で成立しなかったとき、始める処理は呼ばない。
func TestAnswerOption_IdentityFailureCallsNoStarter(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	id := openOptionHold(t, ws)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}

	code, stdout, stderr := runWithStarter(t, st, "answer", id, "--option", "add", "--workspace", ws, "--json")

	if code != 1 || !strings.Contains(stdout+stderr, string(CodeTTYRequired)) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 1 with %s", code, stdout, stderr, CodeTTYRequired)
	}
	if len(st.calls) != 0 {
		t.Errorf("starter calls = %v, want none", st.calls)
	}
	if r := holdAnswerRow(t, ws); r[0] != "" && r[0] != "<nil>" {
		t.Errorf("hold = %v, want unanswered", r)
	}
}

// 対話の欄の印がある経路: `answer <課題> --option <識別>` は成立し、記録の経路は chat になる。
// 成立した後にサイクルを始める処理も、印の無い経路と同じに働く。
func TestChatMark_AnswerOptionRecordsChatAndStartsTheCycle(t *testing.T) {
	ws := initializedWorkspace(t)
	id := openOptionHold(t, ws)
	f := openChat(t, ws)
	f.set(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}

	code, stdout, stderr := chatRunWithStarter(t, st, ws, "answer", id, "--option", "add")

	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	r := holdAnswerRow(t, ws)
	if r[0] != "足す" || r[1] != "add" || r[2] != "chat" || r[3] != f.session || r[4] != chatOwnerBody {
		t.Errorf("hold = %v, want 足す/add/chat/%s/%s", r, f.session, chatOwnerBody)
	}
	if got := lastActivityChannel(t, ws); got != "chat" {
		t.Errorf("work log channel = %q, want chat", got)
	}
	if len(st.calls) != 1 || st.calls[0] != "answer" {
		t.Fatalf("starter calls = %v, want [answer]", st.calls)
	}
	if c := decodeCycle(t, stdout); c["started"] != true {
		t.Errorf("cycle = %v, want started", c)
	}
}

func TestChatMark_AnswerOptionWithNoCycleStartsNothing(t *testing.T) {
	ws := initializedWorkspace(t)
	id := openOptionHold(t, ws)
	f := openChat(t, ws)
	f.set(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}

	code, stdout, stderr := chatRunWithStarter(t, st, ws, "answer", id, "--option", "add", "--no-cycle")

	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if len(st.calls) != 0 {
		t.Errorf("starter calls = %v, want none", st.calls)
	}
	if r := holdAnswerRow(t, ws); r[1] != "add" || r[2] != "chat" {
		t.Errorf("hold = %v, want answer_option add recorded via chat", r)
	}
}
