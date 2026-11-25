package core

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// docs/features/chat-session.md「返事・道具の使用・途中経過」「許可の確認と問い」「費用」「閉じた会話の一覧と、続き」の
// core の口。実ストア（t.TempDir() の SQLite）で確かめる。

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// replyOf は ReadChat の中の ID が id の発言を返す。
func replyOf(t *testing.T, s *Store, id int64) ChatMessage {
	t.Helper()
	chat, err := s.ReadChat(context.Background())
	must(t, err)
	for _, m := range chat.Messages {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("message %d is not in the open chat", id)
	return ChatMessage{}
}

func startTool(t *testing.T, s *Store, reply int64, id, tool, target string) {
	t.Helper()
	must(t, s.StartChatTool(context.Background(), reply, ChatToolUse{ID: id, Tool: tool, Target: target}))
}

// 受入基準: 道具の一覧の要素は識別を持ち、道具の結果は同じ識別の要素だけを変える。
func TestChatTool_ResultChangesOnlyTheElementOfTheSameID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	turn := mustPost(t, s, "q", nil)
	startTool(t, s, turn.ReplyMessageID, "toolu_1", "Bash", "ls")
	startTool(t, s, turn.ReplyMessageID, "toolu_2", "Read", "/tmp/a")
	must(t, s.FinishChatTool(ctx, turn.ReplyMessageID, "toolu_2", true))
	want := []ChatActivity{
		{ID: "toolu_1", Tool: "Bash", Target: "ls", State: ChatActivityRunning},
		{ID: "toolu_2", Tool: "Read", Target: "/tmp/a", State: ChatActivityOK},
	}
	if got := replyOf(t, s, turn.ReplyMessageID).Activity; !reflect.DeepEqual(got, want) {
		t.Fatalf("activity = %+v, want %+v", got, want)
	}
	must(t, s.FinishChatTool(ctx, turn.ReplyMessageID, "toolu_1", false))
	if got := replyOf(t, s, turn.ReplyMessageID).Activity; got[0].State != ChatActivityError || got[1].State != ChatActivityOK {
		t.Fatalf("activity = %+v", got)
	}
}

func TestChatTool_Refusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	turn := mustPost(t, s, "q", nil)
	startTool(t, s, turn.ReplyMessageID, "toolu_1", "Bash", "ls")
	// 過去の形の要素（識別が空）は、道具の結果で変えられない。
	appendLegacyCommand(t, s, turn.ReplyMessageID, "old")
	before := replyOf(t, s, turn.ReplyMessageID).Activity
	for name, c := range map[string]struct {
		err  error
		want error
	}{
		"empty id":        {s.StartChatTool(ctx, turn.ReplyMessageID, ChatToolUse{Tool: "Bash"}), ErrValidation},
		"empty tool":      {s.StartChatTool(ctx, turn.ReplyMessageID, ChatToolUse{ID: "x"}), ErrValidation},
		"duplicate id":    {s.StartChatTool(ctx, turn.ReplyMessageID, ChatToolUse{ID: "toolu_1", Tool: "Bash"}), ErrValidation},
		"unknown result":  {s.FinishChatTool(ctx, turn.ReplyMessageID, "nope", true), ErrNotFound},
		"empty result id": {s.FinishChatTool(ctx, turn.ReplyMessageID, "", false), ErrValidation},
		"owner message":   {s.StartChatTool(ctx, turn.OwnerMessageID, ChatToolUse{ID: "y", Tool: "Bash"}), ErrNotFound},
		"missing message": {s.FinishChatTool(ctx, 999, "toolu_1", true), ErrNotFound},
	} {
		if !errors.Is(c.err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, c.err, c.want)
		}
	}
	if got := replyOf(t, s, turn.ReplyMessageID).Activity; !reflect.DeepEqual(got, before) {
		t.Errorf("a refused call changed the activity: %+v", got)
	}
	must(t, s.FinishChatTool(ctx, turn.ReplyMessageID, "toolu_1", true))
	if err := s.FinishChatTool(ctx, turn.ReplyMessageID, "toolu_1", false); !errors.Is(err, ErrValidation) {
		t.Errorf("a second result for the same tool use: err = %v, want ErrValidation", err)
	}
	must(t, s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, ChatStateDone, nil))
	if err := s.StartChatTool(ctx, turn.ReplyMessageID, ChatToolUse{ID: "late", Tool: "Bash"}); !errors.Is(err, ErrValidation) {
		t.Errorf("start after the reply ended: err = %v, want ErrValidation", err)
	}
}

// 受入基準: 200 文字を超える対象の 1 行は、200 文字で切って記録される。
func TestChatTool_TargetIsCutAt200Characters(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	turn := mustPost(t, s, "q", nil)
	startTool(t, s, turn.ReplyMessageID, "long", "Bash", strings.Repeat("あ", 201))
	startTool(t, s, turn.ReplyMessageID, "exact", "Bash", strings.Repeat("い", 200))
	got := replyOf(t, s, turn.ReplyMessageID).Activity
	if got[0].Target != strings.Repeat("あ", 200) || got[1].Target != strings.Repeat("い", 200) {
		t.Errorf("targets have %d and %d characters", len([]rune(got[0].Target)), len([]rune(got[1].Target)))
	}
}

// 過去の形の要素（command・ok）は、識別が空の Bash の要素として読む。
func TestChatTool_LegacyActivityIsReadAsBash(t *testing.T) {
	t.Parallel()
	s, dir := newWorkspaceStore(t)
	turn := mustPost(t, s, "q", nil)
	coretest.Exec(t, dir, `UPDATE chat_message SET activity = '[{"command":"flywheel status","ok":true},{"command":"x","ok":false}]' WHERE id = ?`, turn.ReplyMessageID)
	startTool(t, s, turn.ReplyMessageID, "toolu_1", "Read", "f")
	want := []ChatActivity{
		{Tool: "Bash", Target: "flywheel status", State: ChatActivityOK},
		{Tool: "Bash", Target: "x", State: ChatActivityError},
		{ID: "toolu_1", Tool: "Read", Target: "f", State: ChatActivityRunning},
	}
	if got := replyOf(t, s, turn.ReplyMessageID).Activity; !reflect.DeepEqual(got, want) {
		t.Errorf("activity = %+v, want %+v", got, want)
	}
}

func permissionRequest(requestID string) ChatPromptRequest {
	return ChatPromptRequest{RequestID: requestID, Kind: ChatPromptPermission, Tool: "Bash", Target: strings.Repeat("c", 5000)}
}

func questionRequest(requestID string) ChatPromptRequest {
	return ChatPromptRequest{RequestID: requestID, Kind: ChatPromptQuestion, Tool: "AskUserQuestion", Questions: []ChatQuestion{
		{Question: "色は？", Options: []ChatQuestionOption{{Label: "赤", Description: "red"}, {Label: "青"}}},
		{Question: "形は？", Options: []ChatQuestionOption{{Label: "丸"}}, MultiSelect: true},
	}}
}

func mustPrompt(t *testing.T, s *Store, reply int64, req ChatPromptRequest) int64 {
	t.Helper()
	id, err := s.RecordChatPrompt(context.Background(), reply, req)
	must(t, err)
	return id
}

func TestChatPrompt_RecordedPendingOnTheReplyWithTheWholeTarget(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	turn := mustPost(t, s, "q", nil)
	p := mustPrompt(t, s, turn.ReplyMessageID, permissionRequest("req-1"))
	q := mustPrompt(t, s, turn.ReplyMessageID, questionRequest("req-2"))
	got := replyOf(t, s, turn.ReplyMessageID).Prompts
	if len(got) != 2 || got[0].ID != p || got[1].ID != q {
		t.Fatalf("prompts = %+v", got)
	}
	if got[0].MessageID != turn.ReplyMessageID || got[0].RequestID != "req-1" || got[0].Kind != ChatPromptPermission ||
		got[0].State != ChatPromptPending || got[0].Tool != "Bash" || got[0].Target != strings.Repeat("c", 5000) ||
		len(got[0].Questions) != 0 || got[0].Answer != nil || got[0].AnsweredAt != nil || got[0].CreatedAt.IsZero() {
		t.Errorf("permission = %+v", got[0])
	}
	if got[1].Kind != ChatPromptQuestion || !reflect.DeepEqual(got[1].Questions, questionRequest("").Questions) {
		t.Errorf("question = %+v", got[1])
	}
	if owner := replyOf(t, s, turn.OwnerMessageID); len(owner.Prompts) != 0 {
		t.Errorf("the owner message has prompts: %+v", owner.Prompts)
	}
	if n := chatCount(t, s, `SELECT COUNT(*) FROM chat_prompt WHERE kind = 'permission' AND questions IS NULL AND answer IS NULL`); n != 1 {
		t.Errorf("permission row with NULL questions = %d, want 1", n)
	}
}

func TestChatPrompt_RecordRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	turn := mustPost(t, s, "q", nil)
	noReqID, noTool, badKind := permissionRequest(""), permissionRequest("r"), permissionRequest("r")
	noTool.Tool, badKind.Kind = "", "other"
	noQuestions, permWithQuestions := questionRequest("r"), permissionRequest("r")
	noQuestions.Questions = nil
	permWithQuestions.Questions = questionRequest("r").Questions
	for name, req := range map[string]ChatPromptRequest{
		"no request id": noReqID, "no tool": noTool, "unknown kind": badKind,
		"question without questions": noQuestions, "permission with questions": permWithQuestions,
	} {
		if _, err := s.RecordChatPrompt(ctx, turn.ReplyMessageID, req); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
	must(t, s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, ChatStateDone, nil))
	if _, err := s.RecordChatPrompt(ctx, turn.ReplyMessageID, permissionRequest("r")); !errors.Is(err, ErrValidation) {
		t.Errorf("record after the reply ended: err = %v, want ErrValidation", err)
	}
	if n := chatCount(t, s, `SELECT COUNT(*) FROM chat_prompt`); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}

// 確認への答えは、答えを待っている確認へ 1 回だけ受ける。
func TestChatPrompt_AnsweredExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	answers := map[string]string{"色は？": "赤", "形は？": "丸, 三角"}
	for _, c := range []struct {
		name      string
		req       ChatPromptRequest
		answer    ChatPromptAnswer
		wantState ChatPromptState
		wantJSON  string
	}{
		{"allow", permissionRequest("a"), ChatPromptAnswer{Decision: ChatDecisionAllow}, ChatPromptAllowed, `{"decision":"allow","message":null}`},
		{"deny", permissionRequest("b"), ChatPromptAnswer{Decision: ChatDecisionDeny}, ChatPromptDenied, `{"decision":"deny","message":null}`},
		{"deny with message", permissionRequest("c"), ChatPromptAnswer{Decision: ChatDecisionDeny, Message: "別の手で <&>"}, ChatPromptDenied, `{"decision":"deny","message":"別の手で <&>"}`},
		{"question", questionRequest("d"), ChatPromptAnswer{Answers: answers}, ChatPromptAnswered, `{"answers":{"形は？":"丸, 三角","色は？":"赤"}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newWorkspaceStore(t)
			turn := mustPost(t, s, "q", nil)
			id := mustPrompt(t, s, turn.ReplyMessageID, c.req)
			checked, err := s.CheckChatPromptAnswer(ctx, id, c.answer)
			must(t, err)
			if checked.State != ChatPromptPending || checked.RequestID != c.req.RequestID {
				t.Fatalf("checked = %+v", checked)
			}
			got, err := s.AnswerChatPrompt(ctx, id, c.answer)
			must(t, err)
			if got.State != c.wantState || got.AnsweredAt == nil || got.Answer == nil || !reflect.DeepEqual(*got.Answer, c.answer) {
				t.Fatalf("answered = %+v (answer %+v)", got, got.Answer)
			}
			if stored := replyOf(t, s, turn.ReplyMessageID).Prompts[0]; !reflect.DeepEqual(stored, *got) {
				t.Errorf("stored = %+v, returned = %+v", stored, *got)
			}
			var raw string
			must(t, s.db.Read(ctx, func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT answer FROM chat_prompt WHERE id = ?`, id).Scan(&raw)
			}))
			if raw != c.wantJSON {
				t.Errorf("stored answer = %s, want %s", raw, c.wantJSON)
			}
			for name, err := range map[string]error{
				"second answer": errOf(s.AnswerChatPrompt(ctx, id, c.answer)),
				"second check":  errOf(s.CheckChatPromptAnswer(ctx, id, c.answer)),
			} {
				if !errors.Is(err, ErrChatPromptNotPending) {
					t.Errorf("%s: err = %v, want ErrChatPromptNotPending", name, err)
				}
			}
			if after := replyOf(t, s, turn.ReplyMessageID).Prompts[0]; !reflect.DeepEqual(after, *got) {
				t.Errorf("a refused answer changed the row: %+v", after)
			}
		})
	}
}

func errOf[T any](_ T, err error) error { return err }

func TestChatPrompt_MalformedAnswersAndUnknownIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	turn := mustPost(t, s, "q", nil)
	perm := mustPrompt(t, s, turn.ReplyMessageID, permissionRequest("a"))
	ques := mustPrompt(t, s, turn.ReplyMessageID, questionRequest("b"))
	full := map[string]string{"色は？": "赤", "形は？": "丸"}
	for name, c := range map[string]struct {
		id     int64
		answer ChatPromptAnswer
	}{
		"permission: empty":                {perm, ChatPromptAnswer{}},
		"permission: answers only":         {perm, ChatPromptAnswer{Answers: full}},
		"permission: decision and answers": {perm, ChatPromptAnswer{Decision: ChatDecisionAllow, Answers: full}},
		"permission: unknown decision":     {perm, ChatPromptAnswer{Decision: "always"}},
		"permission: allow with a message": {perm, ChatPromptAnswer{Decision: ChatDecisionAllow, Message: "m"}},
		"question: empty":                  {ques, ChatPromptAnswer{}},
		"question: decision only":          {ques, ChatPromptAnswer{Decision: ChatDecisionAllow}},
		"question: answers and decision":   {ques, ChatPromptAnswer{Decision: ChatDecisionAllow, Answers: full}},
		"question: answers and a message":  {ques, ChatPromptAnswer{Message: "m", Answers: full}},
		"question: empty map":              {ques, ChatPromptAnswer{Answers: map[string]string{}}},
		"question: blank answer":           {ques, ChatPromptAnswer{Answers: map[string]string{"色は？": " ", "形は？": "丸"}}},
		"question: one left unanswered":    {ques, ChatPromptAnswer{Answers: map[string]string{"色は？": "赤"}}},
		"question: unknown question text":  {ques, ChatPromptAnswer{Answers: map[string]string{"色は？": "赤", "ほか": "丸"}}},
	} {
		for op, err := range map[string]error{
			"check":  errOf(s.CheckChatPromptAnswer(ctx, c.id, c.answer)),
			"answer": errOf(s.AnswerChatPrompt(ctx, c.id, c.answer)),
		} {
			if !errors.Is(err, ErrValidation) {
				t.Errorf("%s (%s): err = %v, want ErrValidation", name, op, err)
			}
		}
	}
	for op, err := range map[string]error{
		"check":  errOf(s.CheckChatPromptAnswer(ctx, 999, ChatPromptAnswer{Decision: ChatDecisionAllow})),
		"answer": errOf(s.AnswerChatPrompt(ctx, 999, ChatPromptAnswer{Decision: ChatDecisionAllow})),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown id (%s): err = %v, want ErrNotFound", op, err)
		}
	}
	if n := chatCount(t, s, `SELECT COUNT(*) FROM chat_prompt WHERE state = 'pending' AND answer IS NULL AND answered_at IS NULL`); n != 2 {
		t.Errorf("untouched pending rows = %d, want 2", n)
	}
}

// replyWithOpenWork は、running の道具 1 つ・成功した道具 1 つ・答えを待つ確認 1 つ・答えた確認 1 つを持つ進行中の返事を作る。
func replyWithOpenWork(t *testing.T, s *Store) *ChatTurn {
	t.Helper()
	ctx := context.Background()
	turn := mustPost(t, s, "q", nil)
	startTool(t, s, turn.ReplyMessageID, "done", "Bash", "a")
	startTool(t, s, turn.ReplyMessageID, "open", "Bash", "b")
	must(t, s.FinishChatTool(ctx, turn.ReplyMessageID, "done", true))
	answered := mustPrompt(t, s, turn.ReplyMessageID, permissionRequest("x"))
	must(t, errOf(s.AnswerChatPrompt(ctx, answered, ChatPromptAnswer{Decision: ChatDecisionAllow})))
	mustPrompt(t, s, turn.ReplyMessageID, permissionRequest("y"))
	return turn
}

func assertOpenWorkClosed(t *testing.T, s *Store, reply int64) {
	t.Helper()
	m := replyOf(t, s, reply)
	if m.Activity[0].State != ChatActivityOK || m.Activity[1].State != ChatActivityError {
		t.Errorf("activity = %+v, want ok and error", m.Activity)
	}
	if m.Prompts[0].State != ChatPromptAllowed || m.Prompts[1].State != ChatPromptExpired || m.Prompts[1].Answer != nil || m.Prompts[1].AnsweredAt != nil {
		t.Errorf("prompts = %+v, want allowed and expired (unanswered)", m.Prompts)
	}
	if err := errOf(s.AnswerChatPrompt(context.Background(), m.Prompts[1].ID, ChatPromptAnswer{Decision: ChatDecisionAllow})); !errors.Is(err, ErrChatPromptNotPending) {
		t.Errorf("answer to an expired prompt: err = %v", err)
	}
}

// 受入基準: 返事が stopped・failed で終わると、running の要素は error に、答えを待っていた確認は expired になる。
func TestChatReply_StoppedOrFailedClosesRunningToolsAndPendingPrompts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	finishers := map[string]func(s *Store, id int64, st ChatMessageState) error{
		"cumulative": func(s *Store, id int64, st ChatMessageState) error {
			return s.FinishChatReplyWithTotal(ctx, id, st, nil)
		},
	}
	for how, finish := range finishers {
		for _, state := range []ChatMessageState{ChatStateStopped, ChatStateFailed} {
			t.Run(how+"/"+string(state), func(t *testing.T) {
				s, _ := newWorkspaceStore(t)
				turn := replyWithOpenWork(t, s)
				must(t, finish(s, turn.ReplyMessageID, state))
				assertOpenWorkClosed(t, s, turn.ReplyMessageID)
			})
		}
	}
	t.Run("done keeps the activity and leaves no pending prompt", func(t *testing.T) {
		s, _ := newWorkspaceStore(t)
		turn := replyWithOpenWork(t, s)
		must(t, s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, ChatStateDone, nil))
		m := replyOf(t, s, turn.ReplyMessageID)
		if m.Activity[1].State != ChatActivityRunning || m.Prompts[1].State != ChatPromptExpired {
			t.Errorf("activity = %+v, prompts = %+v", m.Activity, m.Prompts)
		}
	})
	t.Run("startup cleanup", func(t *testing.T) {
		s, _ := newWorkspaceStore(t)
		turn := replyWithOpenWork(t, s)
		n, err := s.StopInterruptedChatReplies(ctx)
		must(t, err)
		if n != 1 || replyOf(t, s, turn.ReplyMessageID).State != ChatStateStopped {
			t.Fatalf("stopped = %d", n)
		}
		assertOpenWorkClosed(t, s, turn.ReplyMessageID)
	})
}

func TestChatPrompt_ExpireByReplyAndAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dir := newWorkspaceStore(t)
	first := mustPost(t, s, "1", nil)
	must(t, s.FinishChatReplyWithTotal(ctx, first.ReplyMessageID, ChatStateDone, nil))
	second := mustPost(t, s, "2", nil)
	mustPrompt(t, s, second.ReplyMessageID, permissionRequest("a"))
	answered := mustPrompt(t, s, second.ReplyMessageID, permissionRequest("b"))
	must(t, errOf(s.AnswerChatPrompt(ctx, answered, ChatPromptAnswer{Decision: ChatDecisionDeny})))
	// 終わった返事に残った pending の行（過去の不整合）を作り、返事ごとの口が別の返事の行を変えないことを確かめる。
	coretest.Exec(t, dir, `INSERT INTO chat_prompt (message_id, request_id, kind, tool, target, state, created_at)
		VALUES (?, 'z', 'permission', 'Bash', 'z', 'pending', '2026-11-25T00:00:00.000Z')`, first.ReplyMessageID)

	n, err := s.ExpireChatReplyPrompts(ctx, second.ReplyMessageID)
	must(t, err)
	if n != 1 {
		t.Errorf("expired by reply = %d, want 1", n)
	}
	if got := chatCount(t, s, `SELECT COUNT(*) FROM chat_prompt WHERE state = 'pending'`); got != 1 {
		t.Errorf("pending after per-reply expiry = %d, want the other reply's 1", got)
	}
	mustPrompt(t, s, second.ReplyMessageID, permissionRequest("c"))
	n, err = s.ExpireAllChatPrompts(ctx)
	must(t, err)
	if n != 2 {
		t.Errorf("expired all = %d, want 2", n)
	}
	if got := chatCount(t, s, `SELECT COUNT(*) FROM chat_prompt WHERE state = 'denied'`); got != 1 {
		t.Errorf("the answered prompt changed")
	}
	// 起動の片付けは、進行中の返事が無くても、残った pending の確認を expired にする。
	coretest.Exec(t, dir, `UPDATE chat_prompt SET state = 'pending' WHERE request_id = 'z'`)
	must(t, errOf(s.StopInterruptedChatReplies(ctx)))
	if got := chatCount(t, s, `SELECT COUNT(*) FROM chat_prompt WHERE state = 'pending'`); got != 0 {
		t.Errorf("pending after startup cleanup = %d", got)
	}
}

func usd(v float64) *float64 { return &v }

func sessionTotalMicros(t *testing.T, s *Store, sessionID int64) int {
	t.Helper()
	return chatCount(t, s, `SELECT engine_cost_total FROM chat_session WHERE id = ?`, sessionID)
}

// 受入基準: 費用は累計で受け、前の累計との差を返事の費用にする。
func TestChatCost_CountedFromTheCumulativeTotal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	finish := func(state ChatMessageState, total *float64) (ChatMessage, int) {
		t.Helper()
		turn := mustPost(t, s, "q", nil)
		must(t, s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, state, total))
		return replyOf(t, s, turn.ReplyMessageID), sessionTotalMicros(t, s, turn.SessionID)
	}
	for _, c := range []struct {
		name      string
		state     ChatMessageState
		total     *float64
		wantCost  float64
		wantTotal int
	}{
		{"first reply 0.10", ChatStateDone, usd(0.10), 0.10, 100000},
		{"second reply 0.25", ChatStateDone, usd(0.25), 0.15, 250000},
		{"after --resume 0.40", ChatStateDone, usd(0.40), 0.15, 400000},
		{"back to 0.25", ChatStateDone, usd(0.25), 0.25, 250000},
		{"smaller than before 0.05", ChatStateDone, usd(0.05), 0.05, 50000},
		{"0.10", ChatStateDone, usd(0.10), 0.05, 100000},
		{"stopped without the result line", ChatStateStopped, nil, 0, 100000},
		{"next reply 0.30", ChatStateDone, usd(0.30), 0.20, 300000},
	} {
		m, total := finish(c.state, c.total)
		if math.Abs(m.CostUSD-c.wantCost) > 1e-9 || total != c.wantTotal {
			t.Errorf("%s: cost = %v total = %d, want %v and %d", c.name, m.CostUSD, total, c.wantCost, c.wantTotal)
		}
	}
	chat, err := s.ReadChat(ctx)
	must(t, err)
	if math.Abs(chat.CostUSD-0.95) > 1e-9 {
		t.Errorf("chat cost = %v, want 0.95", chat.CostUSD)
	}
}

// 受入基準の値の並び（累計 0.10 の後に止めた返事は 0、次の累計 0.30 で 0.20）と、会話をまたがないこと。
func TestChatCost_TotalsAreKeptPerSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	first := mustPost(t, s, "a", nil)
	must(t, s.FinishChatReplyWithTotal(ctx, first.ReplyMessageID, ChatStateDone, usd(0.10)))
	must(t, s.CloseChat(ctx))
	other := mustPost(t, s, "b", nil)
	must(t, s.FinishChatReplyWithTotal(ctx, other.ReplyMessageID, ChatStateDone, usd(0.30)))
	if got := replyOf(t, s, other.ReplyMessageID).CostUSD; got != 0.30 {
		t.Errorf("a new session starts from 0: cost = %v", got)
	}
	if got := sessionTotalMicros(t, s, first.SessionID); got != 100000 {
		t.Errorf("the first session's total = %d", got)
	}
}

func TestChatCost_Refusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	turn := mustPost(t, s, "q", nil)
	for name, err := range map[string]error{
		"negative":   s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, ChatStateDone, usd(-0.1)),
		"NaN":        s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, ChatStateDone, usd(math.NaN())),
		"infinite":   s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, ChatStateDone, usd(math.Inf(1))),
		"not an end": s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, ChatStateReplying, usd(1)),
	} {
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	must(t, s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, ChatStateSessionLost, usd(0.2)))
	if err := s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, ChatStateDone, usd(0.5)); !errors.Is(err, ErrValidation) {
		t.Errorf("second finish: err = %v", err)
	}
	if got := sessionTotalMicros(t, s, turn.SessionID); got != 200000 {
		t.Errorf("total = %d, want 200000", got)
	}
	if n := chatCount(t, s, `SELECT COUNT(*) FROM chat_session WHERE closed_at IS NOT NULL`); n != 1 {
		t.Errorf("session_lost must close the session")
	}
}

func TestChatSessions_ListedNewestFirstWithTheirValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dir := newWorkspaceStore(t)
	empty, err := s.ListChatSessions(ctx, nil)
	must(t, err)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty list = %#v, want an empty non-nil slice", empty)
	}
	chatTaskFixture(t, s)
	old := mustPost(t, s, "最初の\n発言 "+strings.Repeat("長", 200), nil)
	must(t, s.FinishChatReplyWithTotal(ctx, old.ReplyMessageID, ChatStateDone, usd(0.10)))
	two := mustPost(t, s, "二通目", nil)
	must(t, s.FinishChatReplyWithTotal(ctx, two.ReplyMessageID, ChatStateDone, usd(0.25)))
	coretest.Exec(t, dir, `INSERT INTO chat_link (session_id, challenge_id, action, message_id, created_at)
		VALUES (?, 1, 'create', ?, '2026-11-25T00:00:00.000Z')`, old.SessionID, old.ReplyMessageID)
	must(t, s.CloseChat(ctx))
	cur := mustPost(t, s, "新しい会話", nil)

	got, err := s.ListChatSessions(ctx, func(id string) bool { return id == old.EngineSession })
	must(t, err)
	if len(got) != 2 {
		t.Fatalf("sessions = %+v", got)
	}
	if got[0].EngineSession != cur.EngineSession || !got[0].Open || got[0].ClosedAt != nil || got[0].FirstMessage != "新しい会話" ||
		got[0].CostUSD != 0 || got[0].TranscriptAvailable || got[0].Challenges == nil || len(got[0].Challenges) != 0 {
		t.Errorf("newest = %+v", got[0])
	}
	wantHead := string([]rune("最初の 発言 " + strings.Repeat("長", 200))[:ChatFirstMessageLimit])
	if got[1].EngineSession != old.EngineSession || got[1].Open || got[1].ClosedAt == nil || got[1].StartedAt.IsZero() ||
		got[1].FirstMessage != wantHead || math.Abs(got[1].CostUSD-0.25) > 1e-9 || !got[1].TranscriptAvailable ||
		!reflect.DeepEqual(got[1].Challenges, []ChatSessionChallenge{{ChallengeID: "C-1", Title: "最初の課題"}}) {
		t.Errorf("oldest = %+v", got[1])
	}
	none, err := s.ListChatSessions(ctx, nil)
	must(t, err)
	if none[1].TranscriptAvailable {
		t.Error("without a checker no transcript is reported as available")
	}
}

func openEngineSession(t *testing.T, s *Store) string {
	t.Helper()
	chat, err := s.ReadChat(context.Background())
	must(t, err)
	if chat.Session == nil {
		return ""
	}
	return chat.Session.EngineSession
}

// 判定の順は、無い識別 → すでに開いている（何もせず成功）→ 返事が進行中 → 全文が無い。断るときは何も変えない。
func TestChatSessions_ResumeChecksInOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	yes, no := func(string) bool { return true }, func(string) bool { return false }
	old := mustPost(t, s, "a", nil)
	must(t, s.MarkChatEngineStarted(ctx, old.SessionID))
	must(t, s.FinishChatReplyWithTotal(ctx, old.ReplyMessageID, ChatStateDone, usd(0.25)))
	must(t, s.CloseChat(ctx))
	cur := mustPost(t, s, "b", nil)

	dump := func() []byte { return mustExport(t, s) }
	before := dump()
	if err := s.ResumeChatSession(ctx, "00000000-0000-4000-8000-000000000000", no); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: err = %v", err)
	}
	if err := s.ResumeChatSession(ctx, cur.EngineSession, no); err != nil {
		t.Errorf("already open (replying, no transcript): err = %v", err)
	}
	if err := s.ResumeChatSession(ctx, old.EngineSession, no); !errors.Is(err, ErrChatReplying) {
		t.Errorf("replying: err = %v", err)
	}
	if string(dump()) != string(before) {
		t.Fatal("a refused or no-op resume changed the store")
	}
	must(t, s.FinishChatReplyWithTotal(ctx, cur.ReplyMessageID, ChatStateDone, nil))
	before = dump()
	if err := s.ResumeChatSession(ctx, old.EngineSession, no); !errors.Is(err, ErrChatTranscriptUnavailable) {
		t.Errorf("no transcript: err = %v", err)
	}
	if err := s.ResumeChatSession(ctx, old.EngineSession, nil); !errors.Is(err, ErrChatTranscriptUnavailable) {
		t.Errorf("no checker: err = %v", err)
	}
	if string(dump()) != string(before) {
		t.Fatal("a refused resume changed the store")
	}
	must(t, s.ResumeChatSession(ctx, old.EngineSession, yes))
	if got := openEngineSession(t, s); got != old.EngineSession {
		t.Fatalf("open session = %q, want %q", got, old.EngineSession)
	}
	next := mustPost(t, s, "続き", nil)
	if next.EngineSession != old.EngineSession || !next.Resume || next.NewSession {
		t.Errorf("next turn = %+v", next)
	}
	// 続けた会話の費用の累計は引き継ぐ。
	must(t, s.FinishChatReplyWithTotal(ctx, next.ReplyMessageID, ChatStateDone, usd(0.40)))
	if got := replyOf(t, s, next.ReplyMessageID).CostUSD; math.Abs(got-0.15) > 1e-9 {
		t.Errorf("cost after resume = %v, want 0.15", got)
	}
}

func TestChatSessions_ResumeWithNoOpenSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	old := mustPost(t, s, "a", nil)
	must(t, s.FinishChatReplyWithTotal(ctx, old.ReplyMessageID, ChatStateDone, nil))
	must(t, s.CloseChat(ctx))
	must(t, s.ResumeChatSession(ctx, old.EngineSession, func(string) bool { return true }))
	if got := openEngineSession(t, s); got != old.EngineSession {
		t.Errorf("open session = %q", got)
	}
}

// 欄の印は会話の識別だけ。
func TestChat_SessionMarkEnvHasOnlyTheSession(t *testing.T) {
	t.Parallel()
	if got, want := ChatSessionMarkEnv("uuid-1"), []string{"FLYWHEEL_CHAT_SESSION=uuid-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChatSessionMarkEnv = %v, want %v", got, want)
	}
}

// appendLegacyCommand は、過去の形の道具の一覧の要素（command・ok）を返事の行へ直に足す。
func appendLegacyCommand(t *testing.T, s *Store, messageID int64, command string) {
	t.Helper()
	must(t, s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE chat_message SET activity = json_insert(activity, '$[#]', json_object('command', ?, 'ok', json('true'))) WHERE id = ?`, command, messageID)
		return err
	}))
}
