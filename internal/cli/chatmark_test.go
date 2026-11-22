package cli

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// docs/features/core-redesign-s4d.md S4d-1「欄から受けるコマンドの一覧」「欄から受ける承認・回答」の CLI の口
// （AC-68〜71・84〜96・105・106・108・110・111・119〜129・141）。実ストアで、欄の印の環境変数を付けて同じプロセスで実行する。

const chatOwnerBody = "計画で進めてください。予算も上げて"

type chatEnvFix struct {
	session string
	message int64
	ws      string
}

// openChat は会話を 1 つ開き、オーナーの発言 1 つと進行中の返事を作る（印の発言への返事が進行中の状態）。
func openChat(t *testing.T, ws string) *chatEnvFix {
	t.Helper()
	s, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	turn, err := s.PostChatMessage(context.Background(), chatOwnerBody, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &chatEnvFix{session: turn.EngineSession, message: turn.OwnerMessageID, ws: ws}
}

// closeReply は返事を終え、会話を閉じる（印の会話が閉じている状態）。
func (f *chatEnvFix) close(t *testing.T) {
	t.Helper()
	s, err := core.OpenWorkspace(f.ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	chat, err := s.ReadChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range chat.Messages {
		if m.State == core.ChatStateReplying {
			if err := s.FinishChatReply(context.Background(), m.ID, core.ChatStateDone, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.CloseChat(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// set は印の環境変数を付ける（tools が空なら FLYWHEEL_CHAT_TOOLS は付けない）。
func (f *chatEnvFix) set(t *testing.T, tools string) {
	t.Helper()
	t.Setenv(core.ChatEnvSession, f.session)
	t.Setenv(core.ChatEnvMessage, strconv.FormatInt(f.message, 10))
	if tools != "" {
		t.Setenv(core.ChatEnvTools, tools)
	}
}

func chatRun(t *testing.T, ws string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	full := append(append([]string{}, args...), "--workspace", ws, "--json")
	var out, errb bytes.Buffer
	code = runWithLookPath(full, strings.NewReader(""), &out, &errb, defaultCommands(), lookPathFor(t))
	return code, out.String(), errb.String()
}

func requireRefused(t *testing.T, code int, stderr string) {
	t.Helper()
	if code == 0 || !strings.Contains(stderr, chatCommandRefusal) {
		t.Fatalf("exit=%d stderr=%s, want refused with %q", code, stderr, chatCommandRefusal)
	}
}

func requireNotRefused(t *testing.T, stderr string) {
	t.Helper()
	if strings.Contains(stderr, chatCommandRefusal) {
		t.Fatalf("stderr=%s, want not refused by the list check", stderr)
	}
}

// AC-84・AC-85: 一覧の「読む」「記録する」「サイクル」のコマンドを表にして全部回し、一覧の検査を通ることを確かめる
// （S4a が足すコマンドは、名前が一覧にあることだけ）。
func TestChatList_EveryListedCommandPassesTheListCheck(t *testing.T) {
	read := [][]string{
		{"status"}, {"show", "C-1"}, {"list"}, {"log"}, {"runs"}, {"metrics"}, {"followup", "list"}, {"version"}, {"help"},
		{"cycle", "status"},
	}
	record := [][]string{
		{"approve", "C-1"}, {"reject", "C-1", "--reason", "x"}, {"answer", "C-1", "--answer", "x"}, {"withdraw", "C-1", "--reason", "x"},
		{"budget", "C-1", "--impl-usd", "1"}, {"followup", "drop", "F-1", "--reason", "x"}, {"followup", "promote", "F-1"},
		{"followup", "done", "F-1", "--note", "x"}, {"create", "--title", "t"},
		{"cycle", "stop"},
	}
	for _, tools := range []core.ChatTools{core.ChatToolsRestricted, core.ChatToolsFull} {
		mark := &chatMark{Tools: tools}
		for _, c := range read {
			if need, _, err := chatGate(mark, c); err != nil || need {
				t.Errorf("%v: %v: needMark=%v err=%v, want passed without the mark check", tools, c, need, err)
			}
		}
		for _, c := range record {
			if need, _, err := chatGate(mark, c); err != nil || !need {
				t.Errorf("%v: %v: needMark=%v err=%v, want passed with the mark check", tools, c, need, err)
			}
		}
		// cycle は解析の後に --detach の有無で決める。
		if need, err := chatGateCycle(mark, true); err != nil || !need {
			t.Errorf("%v: cycle --detach: needMark=%v err=%v", tools, need, err)
		}
	}
	if got := classifyChatCommand([]string{"cycle", "--detach"}); got != chatKindCycle {
		t.Errorf("kind of cycle --detach = %v, want cycle", got)
	}
	// --detach が別のフラグの値のとき（解析の後は --detach が立たない）、restricted は断り、full は通す。
	if _, err := chatGateCycle(&chatMark{Tools: core.ChatToolsRestricted}, false); err == nil {
		t.Error("restricted: cycle without --detach must be refused")
	}
	if _, err := chatGateCycle(&chatMark{Tools: core.ChatToolsFull}, false); err != nil {
		t.Errorf("full: cycle without --detach: %v", err)
	}
	// 全体のフラグを除いて名前を照合する（フラグが前後どちらにあっても同じ）。
	for _, c := range [][]string{
		{"--json", "status"}, {"--workspace", "/w", "followup", "list"}, {"followup", "--json", "list"}, {"status", "--workspace=/w"},
	} {
		if got := classifyChatCommand(c); got != chatKindRead {
			t.Errorf("kind of %v = %v, want read", c, got)
		}
	}
	if got := classifyChatCommand([]string{"ingest", "o/r#1"}); got != chatKindIngest {
		t.Errorf("ingest kind = %v", got)
	}

	// 実際に回す（一覧の検査を通ること。コマンド自体の成否は問わない）。印の会話は開いている。
	ws := initializedWorkspace(t)
	createForCase(t, ws)
	f := openChat(t, ws)
	f.set(t, "restricted")
	for _, c := range read {
		if c[0] == "cycle" || c[0] == "help" {
			continue // cycle status は S4a が足す。help は引数の最初で返る。
		}
		code, _, stderr := chatRun(t, ws, c...)
		requireNotRefused(t, stderr)
		if (c[0] == "status" || c[0] == "list" || c[0] == "log" || c[0] == "runs" || c[0] == "metrics" || c[0] == "version") && code != 0 {
			t.Errorf("%v exit=%d stderr=%s", c, code, stderr)
		}
	}
	for _, c := range record {
		if c[0] == "cycle" {
			continue
		}
		_, _, stderr := chatRun(t, ws, c...)
		requireNotRefused(t, stderr)
	}
}

// AC-86〜AC-93・AC-94: 一覧に無いコマンドは、印があり restricted のとき断られる。文言が出て、終了コードは 0 でない。
func TestChatList_UnlistedCommandsAreRefused(t *testing.T) {
	ws := initializedWorkspace(t)
	createForCase(t, ws)
	f := openChat(t, ws)
	f.set(t, "restricted")
	for _, c := range [][]string{
		{"serve"}, {"export"}, {"import"}, {"cycle"}, {"run", "C-1"}, {"op", "exec", "OP-1"}, {"op", "add", "C-1", "--kind", "release", "--summary", "s"},
		{"ingest"}, {"edit", "C-1", "--title", "x"}, {"bind", "C-1", "--source", "s"}, {"classify", "C-1", "--priority", "P0"},
	} {
		code, _, stderr := chatRun(t, ws, c...)
		requireRefused(t, code, stderr)
	}
}

// AC-71: 道具の範囲が決まった値でない（空・別の値）環境は restricted として扱い、run を断る。無い環境も同じ。
func TestChatList_UnknownOrMissingToolsMeansRestricted(t *testing.T) {
	ws := initializedWorkspace(t)
	createForCase(t, ws)
	f := openChat(t, ws)
	for _, tools := range []string{"", "everything", "FULL", " full"} {
		f.set(t, "")
		t.Setenv(core.ChatEnvTools, tools)
		code, _, stderr := chatRun(t, ws, "run", "C-1")
		requireRefused(t, code, stderr)
	}
	f.set(t, "") // FLYWHEEL_CHAT_TOOLS を付けない
	code, _, stderr := chatRun(t, ws, "run", "C-1")
	requireRefused(t, code, stderr)
}

// AC-68: full では一覧の検査をかけない（run が、一覧の検査で断られない）。
func TestChatList_FullSkipsTheListCheck(t *testing.T) {
	ws := initializedWorkspace(t)
	createForCase(t, ws)
	f := openChat(t, ws)
	f.set(t, "full")
	for _, c := range [][]string{{"run", "C-1"}, {"export"}, {"ingest"}, {"cycle"}} {
		_, _, stderr := chatRun(t, ws, c...)
		requireNotRefused(t, stderr)
	}
}

// AC-95・AC-96: 欄の印と委譲の子の印の両方があれば status も断る。会話が閉じている印では、読むコマンドは成功する。
func TestChatMark_DelegatedMarkWinsAndReadsSkipTheMarkCheck(t *testing.T) {
	ws := initializedWorkspace(t)
	createForCase(t, ws)
	f := openChat(t, ws)
	f.set(t, "restricted")
	t.Setenv(core.DelegatedRunEnvVar, "R-1")
	if code, _, stderr := chatRun(t, ws, "status"); code == 0 || !strings.Contains(stderr, core.DelegatedRunEnvVar) {
		t.Fatalf("status with both marks: exit=%d stderr=%s", code, stderr)
	}
	// t.Setenv は後で戻す。ここでは委譲の子の印だけを外す。
	t.Setenv(core.DelegatedRunEnvVar, "R-1")
	unsetEnv(t, core.DelegatedRunEnvVar)

	f.close(t)
	if code, _, stderr := chatRun(t, ws, "status"); code != 0 {
		t.Fatalf("status with closed chat: exit=%d stderr=%s", code, stderr)
	}
}

// AC-126・AC-127・AC-129: 本人確認を通らない記録するコマンド（followup done・create）は、印の検査だけをかけて成立する。
// 会話が閉じているときは断られ、何も書かない。
func TestChatMark_UnattestedRecordCommandsCheckTheMarkOnly(t *testing.T) {
	ws := fuWorkspace(t)
	f := openChat(t, ws)
	f.set(t, "restricted")

	before := len(runJSON(t, ws, "list")["challenges"].([]any))
	if code, _, stderr := chatRun(t, ws, "create", "--title", "欄から", "--done-criteria", "ok"); code != 0 {
		t.Fatalf("create: exit=%d stderr=%s", code, stderr)
	}
	if after := len(runJSON(t, ws, "list")["challenges"].([]any)); after != before+1 {
		t.Errorf("challenges = %d, want %d", after, before+1)
	}
	if code, _, stderr := chatRun(t, ws, "followup", "done", "F-1", "--note", "確かめた"); code != 0 {
		t.Fatalf("followup done: exit=%d stderr=%s", code, stderr)
	}
	if got := fuStatus(t, ws, 1); got != "done" {
		t.Errorf("F-1 status = %q, want done", got)
	}
	// 経路は渡さない（作業ログの経路は cli のまま）。
	if rows := coretest.QueryRows(t, ws, `SELECT COUNT(*) FROM activity WHERE channel = 'chat'`); rows[0][0].(int64) != 0 {
		t.Errorf("chat activities = %v, want 0 (経路は渡さない)", rows[0][0])
	}

	f.close(t)
	code, _, stderr := chatRun(t, ws, "followup", "done", "F-2", "--note", "x")
	if code == 0 || !strings.Contains(stderr, "verification_rejected") {
		t.Fatalf("followup done on a closed chat: exit=%d stderr=%s", code, stderr)
	}
	if got := fuStatus(t, ws, 2); got == "done" {
		t.Errorf("F-2 status = %q, want unchanged", got)
	}
	n := len(runJSON(t, ws, "list")["challenges"].([]any))
	if code, _, _ := chatRun(t, ws, "create", "--title", "x", "--done-criteria", "ok"); code == 0 {
		t.Error("create on a closed chat must be refused")
	}
	unsetEnv(t, core.ChatEnvSession)
	unsetEnv(t, core.ChatEnvMessage)
	if after := len(runJSON(t, ws, "list")["challenges"].([]any)); after != n {
		t.Errorf("challenges = %d, want %d (何も書かない)", after, n)
	}
}

// AC-128: 取り込み元を 1 つ指定した取り込みが成立する。引数の無い形は restricted で断られる。
func TestChatMark_IngestWithSpecPassesAndWithoutIsRefused(t *testing.T) {
	parallelTools(t, true) // t.Setenv を使うので並列にしない
	ws := selectSetup(t)
	f := openChat(t, ws)
	f.set(t, "restricted")

	code, _, stderr := chatRun(t, ws, "ingest")
	requireRefused(t, code, stderr)
	if code, _, stderr := chatRun(t, ws, "ingest", "OWNER/select-repo#2"); code != 0 {
		t.Fatalf("ingest <指定>: exit=%d stderr=%s", code, stderr)
	}
	if n := len(runJSON(t, ws, "list")["challenges"].([]any)); n != 1 {
		t.Errorf("challenges = %d, want 1", n)
	}
	// 印の検査: 会話が閉じていれば、指定つきでも断る。
	f.close(t)
	if code, _, _ := chatRun(t, ws, "ingest", "OWNER/select-repo#1"); code == 0 {
		t.Error("ingest <指定> on a closed chat must be refused")
	}
}

type approvalRow struct{ channel, verification, session, quote string }

func lastApproval(t *testing.T, ws string) approvalRow {
	t.Helper()
	r := coretest.QueryRows(t, ws, `SELECT channel, verification, COALESCE(relay_session, ''), COALESCE(relay_quote, '') FROM approval ORDER BY id DESC LIMIT 1`)
	if len(r) != 1 {
		t.Fatalf("approval rows = %d, want 1", len(r))
	}
	return approvalRow{r[0][0].(string), r[0][1].(string), r[0][2].(string), r[0][3].(string)}
}

// AC-69・AC-105・AC-106・AC-108・AC-110・AC-111: 印があれば、計画の承認の記録は経路 chat・方式 relayed になり、
// 発言は印の発言の body、会話の識別は印の会話の識別になる（--session・--quote に別の値を渡しても使わない）。
// full でも同じ。--relayed・--session・--quote は要らない。
func TestChatMark_ApproveRecordsChatChannelAndTheMarkedMessage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools string
		extra []string
	}{
		{"restricted・フラグ無し（AC-111）", "restricted", nil},
		{"restricted・別の値を渡す（AC-108・AC-110）", "restricted", []string{"--relayed", "--session", "OTHER-SESSION", "--quote", "別の文"}},
		{"full（AC-69）", "full", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := initializedWorkspace(t)
			id := planPendingChallenge(t, ws)
			f := openChat(t, ws)
			f.set(t, tc.tools)
			if code, _, stderr := chatRun(t, ws, append([]string{"approve", id}, tc.extra...)...); code != 0 {
				t.Fatalf("approve: exit=%d stderr=%s", code, stderr)
			}
			if got := workStatus(t, ws, id); got == "awaiting_plan_approval" {
				t.Fatalf("status = %q, approve did not take effect", got)
			}
			a := lastApproval(t, ws)
			if a.channel != "chat" || a.verification != "relayed" {
				t.Errorf("channel/verification = %s/%s, want chat/relayed", a.channel, a.verification)
			}
			if a.quote != chatOwnerBody || a.session != f.session {
				t.Errorf("session/quote = %q/%q, want %q/%q", a.session, a.quote, f.session, chatOwnerBody)
			}
		})
	}
}

// AC-141: 印が無い環境の --relayed の承認は、channel が cli のまま。
func TestChatMark_WithoutTheMarkRelayedStaysCLI(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	runJSON(t, ws, "approve", id, "--relayed", "--session", "s", "--quote", "q")
	if a := lastApproval(t, ws); a.channel != "cli" || a.verification != "relayed" || a.session != "s" || a.quote != "q" {
		t.Errorf("approval = %+v, want cli/relayed with the flags", a)
	}
}

// 印の検査に通らない承認（会話が閉じている）は、何も書かずに断られる。
func TestChatMark_ApproveOnAClosedChatChangesNothing(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	f := openChat(t, ws)
	f.set(t, "restricted")
	f.close(t)
	code, _, stderr := chatRun(t, ws, "approve", id)
	if code == 0 || !strings.Contains(stderr, "verification_rejected") {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got := workStatus(t, ws, id); got != "awaiting_plan_approval" {
		t.Errorf("status = %q, want unchanged", got)
	}
}

func lastActivityChannel(t *testing.T, ws string) string {
	t.Helper()
	return coretest.QueryRows(t, ws, `SELECT channel FROM activity ORDER BY id DESC LIMIT 1`)[0][0].(string)
}

// AC-119: 差し戻しの記録は経路 chat になる。
func TestChatMark_RejectRecordsChat(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	openChat(t, ws).set(t, "restricted")
	if code, _, stderr := chatRun(t, ws, "reject", id, "--reason", "no"); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if a := lastApproval(t, ws); a.channel != "chat" {
		t.Errorf("channel = %q, want chat", a.channel)
	}
}

// AC-120: 保留への回答の記録の経路（answered_channel）は chat になる。
func TestChatMark_AnswerHoldRecordsChat(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "verifying")
	runJSON(t, ws, "verify", id, "--result", "uncertain", "--question", "why?")
	f := openChat(t, ws)
	f.set(t, "restricted")
	if code, _, stderr := chatRun(t, ws, "answer", id, "--answer", "because"); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	r := coretest.QueryRows(t, ws, `SELECT answered_channel, relay_session, relay_quote FROM hold ORDER BY id DESC LIMIT 1`)
	if r[0][0] != "chat" || r[0][1] != f.session || r[0][2] != chatOwnerBody {
		t.Errorf("hold = %v, want chat/%s/%s", r[0], f.session, chatOwnerBody)
	}
}

// AC-121: 計画の問いへの回答の記録の経路は chat になる。
func TestChatMark_AnswerPlanQuestionRecordsChat(t *testing.T) {
	ws, id := planAnswerWorkspace(t)
	openChat(t, ws).set(t, "restricted")
	if code, _, stderr := chatRun(t, ws, "answer", id, "--choice", "Q1=1", "--choice", "Q2=1"); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	rows := coretest.QueryRows(t, ws, `SELECT channel FROM plan_answer`)
	if len(rows) != 2 {
		t.Fatalf("plan_answer rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r[0] != "chat" {
			t.Errorf("plan_answer channel = %v, want chat", r[0])
		}
	}
}

// AC-122: 取り下げの記録の経路は chat になる。
func TestChatMark_WithdrawRecordsChat(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	openChat(t, ws).set(t, "restricted")
	if code, _, stderr := chatRun(t, ws, "withdraw", id, "--reason", "採らない"); code != 0 {
		t.Fatalf("withdraw: exit=%d stderr=%s", code, stderr)
	}
	if got := lastActivityChannel(t, ws); got != "chat" {
		t.Errorf("withdraw channel = %q, want chat", got)
	}

}

// AC-123: 予算の増額の記録の経路は chat になる。
func TestChatMark_BudgetRecordsChat(t *testing.T) {
	ws2 := initializedWorkspace(t)
	bid := createBudgetTargetForCase(t, ws2)
	openChat(t, ws2).set(t, "restricted")
	if code, _, stderr := chatRun(t, ws2, "budget", bid, "--impl-usd", "80"); code != 0 {
		t.Fatalf("budget: exit=%d stderr=%s", code, stderr)
	}
	if got := lastActivityChannel(t, ws2); got != "chat" {
		t.Errorf("budget channel = %q, want chat", got)
	}
}

// AC-124・AC-125: フォローアップを捨てる・上げる記録の経路は chat になる。
func TestChatMark_FollowupDropAndPromoteRecordChat(t *testing.T) {
	ws := fuWorkspace(t)
	openChat(t, ws).set(t, "restricted")
	if code, _, stderr := chatRun(t, ws, "followup", "drop", "F-1", "--reason", "不要"); code != 0 {
		t.Fatalf("drop: exit=%d stderr=%s", code, stderr)
	}
	if got := lastActivityChannel(t, ws); got != "chat" {
		t.Errorf("drop channel = %q, want chat", got)
	}
	if code, _, stderr := chatRun(t, ws, "followup", "promote", "F-2"); code != 0 {
		t.Fatalf("promote: exit=%d stderr=%s", code, stderr)
	}
	if got := lastActivityChannel(t, ws); got != "chat" {
		t.Errorf("promote channel = %q, want chat", got)
	}
}

// AC-70: full でも、既定の宣言の external_send の不可逆操作の承認は断られる。差し戻しは受ける。
func TestChatMark_ExternalSendApprovalIsRefusedEvenWithFull(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	op := runJSON(t, ws, "op", "add", id, "--kind", "external_send", "--summary", "notify partner")["operation"].(map[string]any)["id"].(string)
	op2 := runJSON(t, ws, "op", "add", id, "--kind", "external_send", "--summary", "notify other")["operation"].(map[string]any)["id"].(string)
	openChat(t, ws).set(t, "full")
	code, _, stderr := chatRun(t, ws, "approve", op)
	if code == 0 || !strings.Contains(stderr, "ターミナルで行ってください") {
		t.Fatalf("approve external_send: exit=%d stderr=%s", code, stderr)
	}
	if code, _, stderr := chatRun(t, ws, "reject", op2, "--reason", "no"); code != 0 {
		t.Fatalf("reject external_send: exit=%d stderr=%s", code, stderr)
	}
}

// unsetEnv は環境変数を外す（t.Setenv で元の値の復元を登録した後に呼ぶ）。
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}
