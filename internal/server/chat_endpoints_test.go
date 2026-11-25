package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは対話の欄の、確認への答え・会話の一覧・続ける・全文の口と、その守りの試験
// （docs/features/chat-session.md「画面・API設計」）。

var chatTables = []string{"chat_session", "chat_message", "chat_prompt", "chat_link"}

// chatErrCode は失敗の code を返す。transcript_unavailable は対話の欄の口だけのコードで（engine_unavailable と
// 同じ）、M4 の仕様の表の閉集合の外にある。
func chatErrCode(t *testing.T, body string) string {
	t.Helper()
	var doc struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("not JSON: %q", body)
	}
	return doc.Error.Code
}

func dumpChat(t *testing.T, ws string) string {
	t.Helper()
	return coretest.DumpTables(t, ws, chatTables...)
}

func questionPromptReq(id string) core.ChatPromptRequest {
	return core.ChatPromptRequest{
		RequestID: id, Kind: core.ChatPromptQuestion, Tool: "AskUserQuestion",
		Questions: []core.ChatQuestion{{Question: "どちらにする？", Options: []core.ChatQuestionOption{{Label: "A", Description: "a"}, {Label: "B", Description: "b"}}}},
	}
}

// askPrompt は発言を送り、req の確認が届いた状態にして、会話と記録した確認を返す。
func (e *chatEnvT) askPrompt(t *testing.T, req core.ChatPromptRequest) (*fakeConv, view.ChatPrompt) {
	t.Helper()
	conv := e.send(t, 0, "やって")
	conv.started()
	conv.prompt(req)
	return conv, onlyPrompt(t, e.port)
}

func onlyPrompt(t *testing.T, port int) view.ChatPrompt {
	t.Helper()
	ps := lastReply(getChat(t, port)).Prompts
	if len(ps) != 1 {
		t.Fatalf("prompts = %+v, want 1", ps)
	}
	return ps[0]
}

func answerPath(id int64) string {
	return "/prompts/" + strconv.FormatInt(id, 10) + "/answer"
}

func (e *chatEnvT) answer(t *testing.T, id int64, body string) (int, string) {
	t.Helper()
	return apiPost(t, e.port, chatBase+answerPath(id), body)
}

func noEngineAnswer(t *testing.T, conv *fakeConv) {
	t.Helper()
	select {
	case a := <-conv.answers:
		t.Errorf("the engine received an answer: %+v", a)
	default:
	}
}

// GET …/chat は答えを待っている確認を返し、4,000 文字を超える対象の全文を切らない。
func TestChat_GetReturnsThePendingPromptWithTheWholeTarget(t *testing.T) {
	e := newChatEnv(t)
	command := "echo " + strings.Repeat("あ", 5000)
	_, p := e.askPrompt(t, core.ChatPromptRequest{RequestID: "r1", Kind: core.ChatPromptPermission, Tool: "Bash", Target: command})
	if p.State != "pending" || p.Kind != "permission" || p.Target == nil || *p.Target != command || p.Answer != nil || p.AnsweredAt != nil {
		t.Errorf("prompt = %+v", p)
	}
}

// 答えは Claude Code へ届き、確認は種類に応じた状態になる。許可の答えは decision だけ（許可の書き足しを持たない）。
func TestChat_AnswerIsSentToTheEngineAndRecorded(t *testing.T) {
	for name, tc := range map[string]struct {
		req   core.ChatPromptRequest
		body  string
		want  core.ChatPromptAnswer
		state string
	}{
		"allow": {permissionPrompt("r1"), `{"decision":"allow"}`, core.ChatPromptAnswer{Decision: core.ChatDecisionAllow}, "allowed"},
		"deny":  {permissionPrompt("r1"), `{"decision":"deny","message":"消さないで"}`, core.ChatPromptAnswer{Decision: core.ChatDecisionDeny, Message: "消さないで"}, "denied"},
		"question": {questionPromptReq("r1"), `{"answers":{"どちらにする？":"A"}}`,
			core.ChatPromptAnswer{Answers: map[string]string{"どちらにする？": "A"}}, "answered"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newChatEnv(t)
			conv, p := e.askPrompt(t, tc.req)
			if code, body := e.answer(t, p.ID, tc.body); code != http.StatusNoContent {
				t.Fatalf("answer = %d %s", code, body)
			}
			select {
			case a := <-conv.answers:
				if a.requestID != "r1" || !reflect.DeepEqual(a.answer, tc.want) {
					t.Errorf("engine received %+v, want %+v", a, tc.want)
				}
			default:
				t.Fatal("no answer was sent to the engine")
			}
			got := onlyPrompt(t, e.port)
			if got.State != tc.state || got.Answer == nil || got.AnsweredAt == nil {
				t.Errorf("prompt = %+v, want %s", got, tc.state)
			}
			// 2 回目の答えは 409 で、記録を変えず、Claude Code へも送らない。
			before := dumpChat(t, e.ws)
			if code, body := e.answer(t, p.ID, tc.body); code != 409 || errCode(t, body) != CodeConflict {
				t.Errorf("second answer = %d %s", code, body)
			}
			if after := dumpChat(t, e.ws); after != before {
				t.Errorf("the record changed:\n%s\n->\n%s", before, after)
			}
			noEngineAnswer(t, conv)
		})
	}
}

// 形の誤り（種類に合わない入力・空の答え・以後も許す指定）は 400 で、確認を変えず、Claude Code へ送らない。
func TestChat_MalformedAnswerIs400(t *testing.T) {
	for name, tc := range map[string]struct {
		req    core.ChatPromptRequest
		bodies []string
	}{
		"permission": {permissionPrompt("r1"), []string{
			`{"answers":{"どちらにする？":"A"}}`, `{}`, `null`, ``, `{"decision":""}`, `{"decision":"always"}`,
			`{"decision":"allow","message":"x"}`, `{"decision":"allow","updatedPermissions":[]}`, `{"decision":"allow"}{}`,
		}},
		"question": {questionPromptReq("r1"), []string{
			`{"decision":"allow"}`, `{"decision":"deny"}`, `{}`, `{"answers":{}}`, `{"answers":{"どちらにする？":" "}}`,
			`{"answers":{"別の問い":"A"}}`,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newChatEnv(t)
			conv, p := e.askPrompt(t, tc.req)
			before := dumpChat(t, e.ws)
			for _, body := range tc.bodies {
				if code, got := e.answer(t, p.ID, body); code != 400 || errCode(t, got) != CodeValidationFailed {
					t.Errorf("answer %q = %d %s, want 400", body, code, got)
				}
			}
			if after := dumpChat(t, e.ws); after != before {
				t.Errorf("the record changed:\n%s\n->\n%s", before, after)
			}
			noEngineAnswer(t, conv)
		})
	}
}

// 無い識別と、ほかのエージェントの確認への答えは 404。
func TestChat_AnswerToUnknownOrForeignPromptIs404(t *testing.T) {
	ws, other := initWorkspace(t), initWorkspace(t)
	inv := newFakeChat()
	_, port := startChatServer(t, inv, eventsConfig{},
		core.FleetWorkspace{Name: "w", Path: ws}, core.FleetWorkspace{Name: "v", Path: other})
	if code, body := sendChat(t, port, "x"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	conv := inv.conv(t, 0)
	conv.waitSend(t)
	conv.prompt(permissionPrompt("r1"))
	id := strconv.FormatInt(onlyPrompt(t, port).ID, 10)
	before := dumpChat(t, ws)
	for _, path := range []string{
		"/api/v1/workspaces/v/chat/prompts/" + id + "/answer",
		"/api/v1/workspaces/nope/chat/prompts/" + id + "/answer",
		chatBase + "/prompts/999/answer",
		chatBase + "/prompts/abc/answer",
		chatBase + "/prompts/0/answer",
	} {
		if code, body := apiPost(t, port, path, `{"decision":"allow"}`); code != 404 {
			t.Errorf("POST %s = %d %s, want 404", path, code, body)
		}
	}
	if after := dumpChat(t, ws); after != before {
		t.Errorf("the record changed:\n%s\n->\n%s", before, after)
	}
	noEngineAnswer(t, conv)
}

// プロセスが終わった後の答えは 409 で、確認は expired、答えは記録しない。
func TestChat_AnswerAfterTheProcessExitedIs409(t *testing.T) {
	e := newChatEnv(t)
	conv, p := e.askPrompt(t, permissionPrompt("r1"))
	conv.exit("exit status 1")
	if code, body := e.answer(t, p.ID, `{"decision":"allow"}`); code != 409 || errCode(t, body) != CodeConflict {
		t.Fatalf("answer = %d %s", code, body)
	}
	if got := onlyPrompt(t, e.port); got.State != "expired" || got.Answer != nil || got.AnsweredAt != nil {
		t.Errorf("prompt = %+v", got)
	}
	noEngineAnswer(t, conv)
}

// 送れなかった（実行エンジンが誤りを返した）答えは 409 で、確認は expired、答えは記録しない。
func TestChat_AnswerThatCannotBeSentExpiresThePrompt(t *testing.T) {
	e := newChatEnv(t)
	conv, p := e.askPrompt(t, permissionPrompt("r1"))
	conv.answerErr = errors.New("broken pipe")
	if code, body := e.answer(t, p.ID, `{"decision":"allow"}`); code != 409 || errCode(t, body) != CodeConflict {
		t.Fatalf("answer = %d %s", code, body)
	}
	if got := onlyPrompt(t, e.port); got.State != "expired" || got.Answer != nil || got.AnsweredAt != nil {
		t.Errorf("prompt = %+v", got)
	}
}

// promptStates は最後の返事の確認の状態を、実行エンジンの識別ごとに返す（view の確認は識別を持たないので ID で引く）。
func promptStates(t *testing.T, port int) map[int64]string {
	t.Helper()
	out := map[int64]string{}
	for _, p := range lastReply(getChat(t, port)).Prompts {
		out[p.ID] = p.State
	}
	return out
}

// 2 つの確認が答えを待っている状態にして、確認の ID を返す。
func (e *chatEnvT) twoPrompts(t *testing.T) (conv *fakeConv, first, second int64) {
	t.Helper()
	conv = e.send(t, 0, "やって")
	conv.started()
	conv.prompt(permissionPrompt("r1"))
	conv.prompt(permissionPrompt("r2"))
	ps := lastReply(getChat(t, e.port)).Prompts
	if len(ps) != 2 {
		t.Fatalf("prompts = %+v, want 2", ps)
	}
	return conv, ps[0].ID, ps[1].ID
}

// 実行エンジンが取り下げた確認は、その 1 件だけが expired になり、ほかの確認へは答えられる。
func TestChat_CancelledPromptExpiresOnlyThatPrompt(t *testing.T) {
	e := newChatEnv(t)
	conv, first, second := e.twoPrompts(t)
	conv.cancelPrompt("r1")
	if got := promptStates(t, e.port); got[first] != "expired" || got[second] != "pending" {
		t.Fatalf("states = %v, want only the first expired", got)
	}
	// 取り下げた確認への答えは送らず、409。
	if code, body := e.answer(t, first, `{"decision":"allow"}`); code != 409 {
		t.Fatalf("answer to the cancelled prompt = %d %s", code, body)
	}
	noEngineAnswer(t, conv)
	// ほかの確認は答えられて、答えは実行エンジンへ届く。
	if code, body := e.answer(t, second, `{"decision":"allow"}`); code != http.StatusNoContent {
		t.Fatalf("answer to the other prompt = %d %s", code, body)
	}
	if a := <-conv.answers; a.requestID != "r2" {
		t.Errorf("answer = %+v", a)
	}
	if got := promptStates(t, e.port); got[first] != "expired" || got[second] != "allowed" {
		t.Errorf("states = %v", got)
	}
}

// 答えを実行エンジンへ送れなかったときは、その確認だけが expired になる（プロセスが動いていれば、ほかの確認は
// 答えを待ったまま）。
func TestChat_AnswerThatCannotBeSentExpiresOnlyThatPrompt(t *testing.T) {
	e := newChatEnv(t)
	conv, first, second := e.twoPrompts(t)
	conv.answerErr = errors.New("broken pipe")
	if code, body := e.answer(t, first, `{"decision":"allow"}`); code != 409 {
		t.Fatalf("answer = %d %s", code, body)
	}
	if got := promptStates(t, e.port); got[first] != "expired" || got[second] != "pending" {
		t.Errorf("states = %v, want only the first expired", got)
	}
}

// このサーバが持つプロセスの無い確認（ストアに残っただけ）への答えは、送れないので 409 で expired になる。
func TestChat_AnswerWithoutAProcessExpiresThePrompt(t *testing.T) {
	e := newChatEnv(t)
	st, err := core.OpenWorkspace(e.ws)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.PostChatMessage(context.Background(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.RecordChatPrompt(context.Background(), turn.ReplyMessageID, permissionPrompt("r1"))
	_ = st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code, body := e.answer(t, id, `{"decision":"allow"}`); code != 409 {
		t.Fatalf("answer = %d %s", code, body)
	}
	if got := onlyPrompt(t, e.port); got.State != "expired" || got.Answer != nil {
		t.Errorf("prompt = %+v", got)
	}
}

// 答えを送ってから記録するまでの間に返事の終わり・プロセスの終わりが届いても、送れた答えは記録に残る。
func TestChat_ReplyEndWaitsUntilTheSentAnswerIsRecorded(t *testing.T) {
	for name, end := range map[string]func(*fakeConv){
		"reply end":    func(c *fakeConv) { c.reply(core.ChatStateDone, nil) },
		"process exit": func(c *fakeConv) { c.exit("exit status 1") },
	} {
		t.Run(name, func(t *testing.T) {
			e := newChatEnv(t)
			conv, p := e.askPrompt(t, permissionPrompt("r1"))
			ended := make(chan struct{})
			conv.onAnswer = func() {
				go func() { end(conv); close(ended) }()
				// 終わりの記録が待たされなければ、この間に済む。
				select {
				case <-ended:
				case <-time.After(500 * time.Millisecond):
				}
			}
			if code, body := e.answer(t, p.ID, `{"decision":"allow"}`); code != http.StatusNoContent {
				t.Fatalf("answer = %d %s", code, body)
			}
			select {
			case <-ended:
			case <-time.After(10 * time.Second):
				t.Fatal("the end was not delivered")
			}
			waitReply(t, e.port)
			if got := onlyPrompt(t, e.port); got.State != "allowed" {
				t.Errorf("prompt = %+v, want allowed", got)
			}
		})
	}
}

func getSessions(t *testing.T, port int) []view.ChatSessionSummary {
	t.Helper()
	code, body := apiGet(t, port, chatBase+"/sessions")
	if code != 200 {
		t.Fatalf("GET sessions = %d %s", code, body)
	}
	var out view.ChatSessionsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out.Sessions
}

// twoSessions は閉じた会話（始まりの行が出た。old）と、プロセスが動いている開いた会話（current）を作る。
func (e *chatEnvT) twoSessions(t *testing.T) (old, current *fakeConv) {
	t.Helper()
	old = e.send(t, 0, "最初の相談")
	old.started()
	old.reply(core.ChatStateDone, usdTotal(0.25))
	waitReply(t, e.port)
	e.post(t, "/new", http.StatusNoContent)
	current = e.send(t, 1, "次の相談")
	current.started()
	current.reply(core.ChatStateDone, nil)
	waitReply(t, e.port)
	return old, current
}

// 一覧は閉じた会話を含めて新しい順で、最初の発言の先頭・費用の合計・紐づいた課題・全文の有無を返す。
func TestChat_SessionsListsClosedSessionsNewestFirst(t *testing.T) {
	e := newChatEnv(t)
	if got := getSessions(t, e.port); len(got) != 0 {
		t.Fatalf("sessions = %+v, want empty", got)
	}
	st, err := core.OpenWorkspace(e.ws)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "紐づいた課題", DoneCriteria: "d"})
	_ = st.Close()
	if err != nil {
		t.Fatal(err)
	}
	old, current := e.twoSessions(t)
	coretest.Exec(t, e.ws, `INSERT INTO chat_link (session_id, challenge_id, action, message_id, created_at)
		VALUES (1, 1, 'create', 2, '2026-11-25T00:00:00.000Z')`)
	e.inv.setTranscript(current.in.SessionID)

	got := getSessions(t, e.port)
	if len(got) != 2 || got[0].ID != current.in.SessionID || got[1].ID != old.in.SessionID {
		t.Fatalf("sessions = %+v", got)
	}
	if !got[0].Open || got[0].ClosedAt != nil || !got[0].TranscriptAvailable || len(got[0].Challenges) != 0 {
		t.Errorf("current = %+v", got[0])
	}
	closed := got[1]
	if closed.Open || closed.ClosedAt == nil || closed.StartedAt == "" || closed.FirstMessage != "最初の相談" || closed.CostUSD != 0.25 ||
		closed.TranscriptAvailable || !reflect.DeepEqual(closed.Challenges, []view.ChatSessionChallenge{{ChallengeID: "C-1", Title: "紐づいた課題"}}) {
		t.Errorf("closed = %+v", closed)
	}
	// 全文のファイルは、そのエージェントのワークスペースを作業フォルダにして探す。
	for _, ref := range e.inv.refs {
		if ref.WorkDir != e.ws {
			t.Errorf("ref = %+v, want work dir %s", ref, e.ws)
		}
	}
}

// 全文のファイルの有無を調べている間は、ストアを借りず、書き込みのロックも握らない（ファイルの調べは遅いことがあり、
// 握ったままだと同じワークスペースのほかの書き込みが待たされる）。
func TestChat_TranscriptCheckHoldsNeitherTheStoreNorTheWriteLock(t *testing.T) {
	for name, call := range map[string]func(t *testing.T, e *chatEnvT, id string){
		"list":   func(t *testing.T, e *chatEnvT, _ string) { getSessions(t, e.port) },
		"resume": func(t *testing.T, e *chatEnvT, id string) { e.post(t, "/sessions/"+id+"/resume", http.StatusNoContent) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newChatEnv(t)
			old, _ := e.twoSessions(t)
			e.inv.setTranscript(old.in.SessionID)
			entry := e.s.chat.fleet.byName["w"]
			checks := 0
			e.inv.onHasTranscript = func() {
				checks++
				// 借りたままなら、ストアの状態を変える側（Lock）は入れない。
				if !entry.mu.TryLock() {
					t.Error("the store is still leased while the transcript is checked")
				} else {
					entry.mu.Unlock()
				}
				// 書き込みのロックを握ったままなら、別の接続の書き込みは待たされて ErrStoreBusy になる。
				st, err := core.OpenWorkspace(e.ws)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = st.Close() }()
				if _, err := st.ExpireAllChatPrompts(context.Background()); err != nil {
					t.Errorf("a write during the transcript check failed: %v", err)
				}
			}
			call(t, e, old.in.SessionID)
			if checks == 0 {
				t.Fatal("the transcript was never checked")
			}
		})
	}
}

// 続けると、いまの会話を閉じてそのプロセスを止め、選んだ会話を開く。次の発言は --resume で起動する。
func TestChat_ResumeOpensTheSessionAndStopsTheCurrentProcess(t *testing.T) {
	e := newChatEnv(t)
	old, current := e.twoSessions(t)
	e.inv.setTranscript(old.in.SessionID)
	e.post(t, "/sessions/"+old.in.SessionID+"/resume", http.StatusNoContent)
	if !current.isExited() {
		t.Error("the process of the previous session is still running")
	}
	if c := getChat(t, e.port); c.SessionID == nil || *c.SessionID != old.in.SessionID {
		t.Fatalf("open session = %+v", c)
	}
	for _, s := range getSessions(t, e.port) {
		if s.Open != (s.ID == old.in.SessionID) {
			t.Errorf("session = %+v", s)
		}
	}
	next := e.send(t, 2, "続き")
	if !next.in.Resume || next.in.SessionID != old.in.SessionID || next.overlap {
		t.Errorf("start input = %+v (overlap %v), want resume", next.in, next.overlap)
	}
}

// 開いている会話を続けるのは何もせず成功で、返事が進行中でも、会話・返事の行・プロセスを変えない。
func TestChat_ResumeOfTheOpenSessionChangesNothing(t *testing.T) {
	e := newChatEnv(t)
	conv := e.send(t, 0, "x")
	conv.started()
	before := dumpChat(t, e.ws)
	// 全文のファイルが無くても成功する。
	e.post(t, "/sessions/"+conv.in.SessionID+"/resume", http.StatusNoContent)
	if after := dumpChat(t, e.ws); after != before {
		t.Errorf("the record changed:\n%s\n->\n%s", before, after)
	}
	if conv.isExited() {
		t.Error("the process was stopped")
	}
}

// 断るとき（無い識別は 404、返事が進行中は 409、全文のファイルが無ければ 409 transcript_unavailable）は、
// どの会話もプロセスも変えない。返事の進行中は、全文のファイルの有無より先に断る。
func TestChat_ResumeRefusalsChangeNothing(t *testing.T) {
	e := newChatEnv(t)
	old, current := e.twoSessions(t)
	refuse := func(id string, status int, code string) {
		t.Helper()
		before := dumpChat(t, e.ws)
		got, body := apiPost(t, e.port, chatBase+"/sessions/"+id+"/resume", ``)
		if got != status || chatErrCode(t, body) != code {
			t.Errorf("resume %s = %d %s, want %d %s", id, got, body, status, code)
		}
		if after := dumpChat(t, e.ws); after != before {
			t.Errorf("the record changed:\n%s\n->\n%s", before, after)
		}
		if current.isExited() {
			t.Error("the current process was stopped")
		}
	}
	refuse("00000000-0000-4000-8000-000000000000", 404, CodeNotFound)
	refuse(old.in.SessionID, 409, CodeTranscriptUnavailable)
	e.send(t, 1, "進行中にする")
	refuse(old.in.SessionID, 409, CodeConflict)
	e.inv.setTranscript(old.in.SessionID)
	refuse(old.in.SessionID, 409, CodeConflict)
	refuse("00000000-0000-4000-8000-000000000000", 404, CodeNotFound)
}

func getTranscript(t *testing.T, port int, id string) (int, string) {
	t.Helper()
	return apiGet(t, port, chatBase+"/sessions/"+id+"/transcript")
}

// 全文は、実行エンジンが読んだ項目を順のまま返し、ストアを変えない。読むたびに読み直す。
func TestChat_TranscriptReturnsTheEntriesInOrderWithoutWriting(t *testing.T) {
	e := newChatEnv(t)
	old, _ := e.twoSessions(t)
	at := time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC)
	e.inv.setTranscript(old.in.SessionID,
		core.ChatTranscriptEntry{Kind: core.ChatTranscriptOwner, Text: "最初の相談", At: at},
		core.ChatTranscriptEntry{Kind: core.ChatTranscriptAssistant, Text: "見ます"},
		core.ChatTranscriptEntry{Kind: core.ChatTranscriptToolUse, Tool: "Bash", Text: `{"command":"ls"}`},
		core.ChatTranscriptEntry{Kind: core.ChatTranscriptToolResult, Tool: "Bash", Text: "a", Truncated: true},
	)
	before := dumpChat(t, e.ws)
	code, body := getTranscript(t, e.port, old.in.SessionID)
	if code != 200 {
		t.Fatalf("GET transcript = %d %s", code, body)
	}
	var out view.ChatTranscriptResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, 0, len(out.Entries))
	for _, en := range out.Entries {
		kinds = append(kinds, en.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"owner", "assistant", "tool_use", "tool_result"}) {
		t.Fatalf("kinds = %v", kinds)
	}
	if first, last := out.Entries[0], out.Entries[3]; first.Text != "最初の相談" || first.Tool != nil || first.At == nil ||
		last.Tool == nil || *last.Tool != "Bash" || !last.Truncated || last.At != nil {
		t.Errorf("entries = %+v", out.Entries)
	}
	if after := dumpChat(t, e.ws); after != before {
		t.Errorf("the store changed:\n%s\n->\n%s", before, after)
	}
	if ref := e.inv.refs[len(e.inv.refs)-1]; ref != (core.ChatTranscriptRef{SessionID: old.in.SessionID, WorkDir: e.ws}) {
		t.Errorf("ref = %+v", ref)
	}
}

// 全文のファイルが無ければ 404 transcript_unavailable。このエージェントに無い会話は、ファイルがあっても読まずに 404。
func TestChat_TranscriptUnavailableOrUnknownIs404(t *testing.T) {
	e := newChatEnv(t)
	old, _ := e.twoSessions(t)
	if code, body := getTranscript(t, e.port, old.in.SessionID); code != 404 || chatErrCode(t, body) != CodeTranscriptUnavailable {
		t.Errorf("no file = %d %s", code, body)
	}
	const foreign = "00000000-0000-4000-8000-000000000000"
	e.inv.setTranscript(foreign, core.ChatTranscriptEntry{Kind: core.ChatTranscriptOwner, Text: "ほかの場所の会話"})
	if code, body := getTranscript(t, e.port, foreign); code != 404 || errCode(t, body) != CodeNotFound || strings.Contains(body, "ほかの場所") {
		t.Errorf("unknown session = %d %s", code, body)
	}
}

// 対話の欄の実行エンジンが組み込まれていないサーバは、全文のファイルが無いものとして扱う。
func TestChat_SessionsWithoutAnEngine(t *testing.T) {
	ws := initWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.PostChatMessage(context.Background(), "x", nil)
	_ = st.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, port := startChatServer(t, nil, eventsConfig{}, core.FleetWorkspace{Name: "w", Path: ws})
	if got := getSessions(t, port); len(got) != 1 || got[0].TranscriptAvailable {
		t.Errorf("sessions = %+v", got)
	}
	if code, body := getTranscript(t, port, turn.EngineSession); code != 404 || chatErrCode(t, body) != CodeTranscriptUnavailable {
		t.Errorf("transcript = %d %s", code, body)
	}
}

// guardedChat は、答えを待つ確認のある会話と、続けられる閉じた会話を持つ環境を作り、書き込みの 2 つの口を返す。
func guardedChat(t *testing.T) (e *chatEnvT, conv *fakeConv, writes map[string]string, oldID string) {
	t.Helper()
	e = newChatEnv(t)
	old, conv := e.twoSessions(t)
	e.inv.setTranscript(old.in.SessionID)
	// 返事の合間に「続ける」が通る状態にしておく（確認は返事の行に直接残す）。
	coretest.Exec(t, e.ws, `INSERT INTO chat_prompt (message_id, request_id, kind, tool, target, state, created_at)
		VALUES (4, 'r1', 'permission', 'Bash', 'x', 'pending', '2026-11-25T00:00:00.000Z')`)
	writes = map[string]string{
		chatBase + answerPath(1):                               `{"decision":"allow"}`,
		chatBase + "/sessions/" + old.in.SessionID + "/resume": ``,
	}
	return e, conv, writes, old.in.SessionID
}

// 確認への答えと「続ける」は、CSRF の値が無い・誤りの要求を断り、ストアを変えない。
func TestChat_AnswerAndResumeNeedCSRF(t *testing.T) {
	e, conv, writes, _ := guardedChat(t)
	token, _ := fetchToken(t, e.port)
	before := dumpChat(t, e.ws)
	for path := range writes {
		for name, tc := range map[string][2]string{
			"none": {"", ""}, "cookie only": {token, ""}, "header only": {"", token},
			"wrong header": {token, "wrong"}, "wrong pair": {"wrong", "wrong"},
		} {
			if code, body := post(t, e.port, path, tc[0], tc[1]); code != 403 || !strings.Contains(body, CodeForbiddenOrigin) {
				t.Errorf("POST %s (%s) = %d %s, want 403", path, name, code, body)
			}
		}
	}
	if after := dumpChat(t, e.ws); after != before || conv.isExited() {
		t.Errorf("refused posts had effects (exited %v):\n%s\n->\n%s", conv.isExited(), before, after)
	}
	noEngineAnswer(t, conv)
}

// 新しい 4 つの口は、Host か Origin が同じマシンのものでない要求を断る。
func TestChat_NewRoutesRejectBadHostAndOrigin(t *testing.T) {
	e, conv, writes, oldID := guardedChat(t)
	token, _ := fetchToken(t, e.port)
	p := strconv.Itoa(e.port)
	good := "127.0.0.1:" + p
	bad := map[string][2]string{
		"別の Host":        {"evil.example:" + p, ""},
		"別の Origin":      {good, "http://evil.example"},
		"Origin が null":  {good, "null"},
		"Origin のポートが違う": {good, "http://localhost"},
	}
	reads := []string{chatBase + "/sessions", chatBase + "/sessions/" + oldID + "/transcript"}
	before := dumpChat(t, e.ws)
	for name, tc := range bad {
		for path := range writes {
			if code, body := postWith(t, e.port, path, token, tc[0], tc[1]); code != 403 || !strings.Contains(body, CodeForbiddenOrigin) {
				t.Errorf("POST %s (%s) = %d %s, want 403", path, name, code, body)
			}
		}
		for _, path := range reads {
			if code, body := getPath(t, e.port, path, tc[0], tc[1]); code != 403 || !strings.Contains(body, CodeForbiddenOrigin) {
				t.Errorf("GET %s (%s) = %d %s, want 403", path, name, code, body)
			}
		}
	}
	if after := dumpChat(t, e.ws); after != before || conv.isExited() {
		t.Errorf("refused requests had effects (exited %v):\n%s\n->\n%s", conv.isExited(), before, after)
	}
	noEngineAnswer(t, conv)
	// 検査を通る要求は、口まで届く。
	for _, path := range reads {
		if code, body := getPath(t, e.port, path, good, "http://"+good); code != 200 {
			t.Errorf("GET %s = %d %s", path, code, body)
		}
	}
}

// 読み取りの口は POST を、書き込みの口は GET を受けない。
func TestChat_NewRoutesAcceptOnlyTheirMethod(t *testing.T) {
	e, _, writes, oldID := guardedChat(t)
	for path := range writes {
		if code, body := apiGet(t, e.port, path); code != 404 {
			t.Errorf("GET %s = %d %s, want 404", path, code, body)
		}
	}
	for _, path := range []string{chatBase + "/sessions", chatBase + "/sessions/" + oldID + "/transcript"} {
		if code, body := apiPost(t, e.port, path, ``); code != 404 {
			t.Errorf("POST %s = %d %s, want 404", path, code, body)
		}
	}
}
