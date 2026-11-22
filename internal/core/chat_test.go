package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// docs/features/core-redesign-s4d.md S4d-1「会話」「記録」。実ストア（t.TempDir() の SQLite）で確かめる。

var uuidV4Lower = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func chatCount(t *testing.T, s *Store, query string, args ...any) (n int) {
	t.Helper()
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(query, args...).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}

func mustPost(t *testing.T, s *Store, text string, v *ChatViewing) *ChatTurn {
	t.Helper()
	turn, err := s.PostChatMessage(context.Background(), text, v)
	if err != nil {
		t.Fatalf("PostChatMessage(%q): %v", text, err)
	}
	return turn
}

// AC-47: 会話が無いときの最初の発言で、会話が 1 行できて UUID が入り、返事の行ができる。
func TestChat_FirstMessageCreatesTheSessionAndAReplyRow(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()

	empty, err := s.ReadChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Session != nil || len(empty.Messages) != 0 || empty.Replying || empty.CostUSD != 0 {
		t.Fatalf("empty chat = %+v, want no session, no messages", empty)
	}
	if chatCount(t, s, `SELECT COUNT(*) FROM chat_session`) != 0 {
		t.Fatal("reading the chat created a session")
	}

	turn := mustPost(t, s, "状況を教えて", nil)
	if !turn.NewSession || turn.Resume || !uuidV4Lower.MatchString(turn.EngineSession) {
		t.Errorf("turn = %+v, want a new session with a lowercase UUID v4 and Resume=false", turn)
	}
	if chatCount(t, s, `SELECT COUNT(*) FROM chat_session WHERE engine_session = ? AND engine_started = 0 AND closed_at IS NULL`, turn.EngineSession) != 1 {
		t.Error("chat_session row (engine_started=0, open) was not created")
	}

	chat, err := s.ReadChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Session == nil || chat.Session.EngineSession != turn.EngineSession || !chat.Replying || len(chat.Messages) != 2 {
		t.Fatalf("chat = %+v", chat)
	}
	owner, reply := chat.Messages[0], chat.Messages[1]
	if owner.ID != turn.OwnerMessageID || owner.Role != ChatRoleOwner || owner.Body != "状況を教えて" || owner.State != ChatStateDone || owner.EndedAt == nil || owner.Viewing != nil {
		t.Errorf("owner message = %+v", owner)
	}
	if reply.ID != turn.ReplyMessageID || reply.Role != ChatRoleOrchestrator || reply.State != ChatStateReplying || reply.Body != "" || reply.EndedAt != nil || reply.CostUSD != 0 || len(reply.Activity) != 0 || reply.Activity == nil {
		t.Errorf("reply message = %+v", reply)
	}
}

// chatTaskFixture は課題 2 つと、1 つ目の課題のタスク 1 つ（T-1）を作る。
func chatTaskFixture(t *testing.T, s *Store) {
	t.Helper()
	mustCreateChallenge(t, s, "最初の課題")
	mustCreateChallenge(t, s, "2 つ目の課題")
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
			VALUES (1, 1, 'a', 'タスクの題名', 'b', 'd', 'o/r', 'pull_request', 'pending', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// 識別だけを受け、題名は core が引く（呼び出し元が付けた題名は使わない）。
func TestChat_ViewingTitlesAreLookedUpByCore(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	chatTaskFixture(t, s)
	turn := mustPost(t, s, "これは？", &ChatViewing{ChallengeID: "C-1", TaskID: "T-1", Title: "偽の題名", TaskTitle: "偽のタスク名"})
	want := &ChatViewing{ChallengeID: "C-1", TaskID: "T-1", Title: "最初の課題", TaskTitle: "タスクの題名"}
	if !reflect.DeepEqual(turn.Viewing, want) {
		t.Errorf("turn.Viewing = %+v, want %+v", turn.Viewing, want)
	}
	chat, _ := s.ReadChat(context.Background())
	if got, want := chat.Messages[0].Viewing, (&ChatViewing{ChallengeID: "C-1", TaskID: "T-1", Title: "最初の課題"}); !reflect.DeepEqual(got, want) {
		t.Errorf("stored viewing = %+v, want %+v (the task title is not stored)", got, want)
	}

	s2, _ := newWorkspaceStore(t)
	chatTaskFixture(t, s2)
	turn = mustPost(t, s2, "課題だけ", &ChatViewing{ChallengeID: "C-2"})
	if want := (&ChatViewing{ChallengeID: "C-2", Title: "2 つ目の課題"}); !reflect.DeepEqual(turn.Viewing, want) {
		t.Errorf("challenge-only viewing = %+v, want %+v", turn.Viewing, want)
	}

	for name, v := range map[string]*ChatViewing{"nil": nil, "empty": {}, "titles_only": {Title: "x"}} {
		s3, _ := newWorkspaceStore(t)
		turn := mustPost(t, s3, "何も見ていない", v)
		chat, _ := s3.ReadChat(context.Background())
		if turn.Viewing != nil || chat.Messages[0].Viewing != nil {
			t.Errorf("%s: viewing = %+v / %+v, want none", name, turn.Viewing, chat.Messages[0].Viewing)
		}
	}
}

// 引けない識別は断り、会話も発言の行も作らない。
func TestChat_UnresolvableViewingIsRefusedWithoutWriting(t *testing.T) {
	t.Parallel()
	cases := map[string]*ChatViewing{
		"missing_challenge":     {ChallengeID: "C-99"},
		"malformed_challenge":   {ChallengeID: "x"},
		"missing_task":          {ChallengeID: "C-1", TaskID: "T-99"},
		"malformed_task":        {ChallengeID: "C-1", TaskID: "t1"},
		"task_of_other_chall":   {ChallengeID: "C-2", TaskID: "T-1"},
		"task_without_challnge": {TaskID: "T-1"},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newWorkspaceStore(t)
			chatTaskFixture(t, s)
			if _, err := s.PostChatMessage(context.Background(), "q", v); !errors.Is(err, ErrValidation) {
				t.Fatalf("error = %v, want ErrValidation", err)
			}
			if n := chatCount(t, s, `SELECT (SELECT COUNT(*) FROM chat_session) + (SELECT COUNT(*) FROM chat_message)`); n != 0 {
				t.Errorf("a refused post left %d rows", n)
			}
		})
	}
}

// 返事の終わり方が session_lost なら、同じ書き込みで会話が閉じ、次の発言は新しい会話になる。
func TestChat_SessionLostClosesTheSession(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()
	first := mustPost(t, s, "q", nil)
	if err := s.MarkChatEngineStarted(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChatReply(ctx, first.ReplyMessageID, ChatStateSessionLost, 0); err != nil {
		t.Fatal(err)
	}
	if n := chatCount(t, s, `SELECT COUNT(*) FROM chat_session WHERE closed_at IS NOT NULL`); n != 1 {
		t.Fatalf("closed sessions = %d, want 1", n)
	}
	next := mustPost(t, s, "もう一度", nil)
	if !next.NewSession || next.Resume || next.SessionID == first.SessionID {
		t.Errorf("next turn = %+v, want a new session", next)
	}
}

func TestChat_EmptySpeechOnAFinishedReplyIsStillRefused(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()
	turn := mustPost(t, s, "q", nil)
	if err := s.AppendChatSpeech(ctx, turn.ReplyMessageID, " "); err != nil {
		t.Errorf("empty speech on a replying message = %v, want nil", err)
	}
	if err := s.FinishChatReply(ctx, turn.ReplyMessageID, ChatStateDone, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChatSpeech(ctx, turn.ReplyMessageID, ""); !errors.Is(err, ErrValidation) {
		t.Errorf("empty speech after finish = %v, want ErrValidation", err)
	}
	if err := s.AppendChatSpeech(ctx, 999, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty speech on a missing message = %v, want ErrNotFound", err)
	}
}

func TestChat_EmptyTextIsRefusedWithoutWriting(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	for _, text := range []string{"", "  \n\t"} {
		if _, err := s.PostChatMessage(context.Background(), text, nil); !errors.Is(err, ErrValidation) {
			t.Errorf("PostChatMessage(%q) = %v, want ErrValidation", text, err)
		}
	}
	if chatCount(t, s, `SELECT (SELECT COUNT(*) FROM chat_session) + (SELECT COUNT(*) FROM chat_message)`) != 0 {
		t.Error("a refused message left rows")
	}
}

// 返事が進行中なら、次の発言は断る（行は増えない）。終えれば受ける。
func TestChat_PostWhileReplyingIsRefused(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()
	turn := mustPost(t, s, "1 通目", nil)
	if _, err := s.PostChatMessage(ctx, "2 通目", nil); !errors.Is(err, ErrChatReplying) {
		t.Fatalf("second post while replying = %v, want ErrChatReplying", err)
	}
	if n := chatCount(t, s, `SELECT COUNT(*) FROM chat_message`); n != 2 {
		t.Errorf("messages = %d, want 2 (the refused post wrote nothing)", n)
	}
	if err := s.FinishChatReply(ctx, turn.ReplyMessageID, ChatStateDone, 0.5); err != nil {
		t.Fatal(err)
	}
	second := mustPost(t, s, "2 通目", nil)
	if second.NewSession || second.SessionID != turn.SessionID || second.EngineSession != turn.EngineSession {
		t.Errorf("second turn = %+v, want the same session", second)
	}
}

// 続きかどうかは engine_started で決まる。返事の終わり方（limit・stopped・failed）には依らない。
func TestChat_ResumeFollowsEngineStartedNotTheEnding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name    string
		started bool
		end     ChatMessageState
		resume  bool
	}{
		{"limit_after_start", true, ChatStateLimit, true},
		{"stopped_after_start", true, ChatStateStopped, true},
		{"failed_without_start", false, ChatStateFailed, false},
		{"failed_after_start", true, ChatStateFailed, true},
		{"done", true, ChatStateDone, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newWorkspaceStore(t)
			first := mustPost(t, s, "1", nil)
			if first.Resume {
				t.Fatal("the first message resumed")
			}
			if c.started {
				if err := s.MarkChatEngineStarted(ctx, first.SessionID); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.FinishChatReply(ctx, first.ReplyMessageID, c.end, 0); err != nil {
				t.Fatal(err)
			}
			if second := mustPost(t, s, "2", nil); second.Resume != c.resume {
				t.Errorf("Resume = %v, want %v", second.Resume, c.resume)
			}
		})
	}
}

func TestChat_MarkEngineStartedOnAMissingSession(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	if err := s.MarkChatEngineStarted(context.Background(), 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkChatEngineStarted(99) = %v, want ErrNotFound", err)
	}
}

func TestChat_SpeechesAndCommandsAccumulateInOrder(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()
	turn := mustPost(t, s, "q", nil)
	id := turn.ReplyMessageID
	steps := []func() error{
		func() error { return s.AppendChatSpeech(ctx, id, "確かめます。") },
		func() error { return s.AppendChatCommand(ctx, id, ChatCommand{Command: "flywheel status", OK: true}) },
		func() error { return s.AppendChatCommand(ctx, id, ChatCommand{Command: "flywheel nosuch", OK: false}) },
		func() error { return s.AppendChatSpeech(ctx, id, "") }, // 空は足さない
		func() error { return s.AppendChatSpeech(ctx, id, "1 件ありました。") },
	}
	for i, f := range steps {
		if err := f(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	chat, _ := s.ReadChat(ctx)
	reply := chat.Messages[1]
	if reply.Body != "確かめます。\n\n1 件ありました。" {
		t.Errorf("body = %q", reply.Body)
	}
	if want := []ChatCommand{{"flywheel status", true}, {"flywheel nosuch", false}}; !reflect.DeepEqual(reply.Activity, want) {
		t.Errorf("activity = %+v, want %+v", reply.Activity, want)
	}
	if reply.State != ChatStateReplying {
		t.Errorf("state = %s, want replying until finished", reply.State)
	}
}

func TestChat_FinishRecordsEndingCostAndTime(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()
	fixed := time.Date(2026, 11, 22, 3, 4, 5, 0, time.UTC)
	s.now = func() time.Time { return fixed }
	turn := mustPost(t, s, "q", nil)
	if err := s.FinishChatReply(ctx, turn.ReplyMessageID, ChatStateRateLimited, 1.234567); err != nil {
		t.Fatal(err)
	}
	chat, _ := s.ReadChat(ctx)
	reply := chat.Messages[1]
	if reply.State != ChatStateRateLimited || reply.CostUSD != 1.234567 || reply.EndedAt == nil || !reply.EndedAt.Equal(fixed) || chat.Replying {
		t.Errorf("reply = %+v, chat.Replying = %v", reply, chat.Replying)
	}
	if chat.CostUSD != 1.234567 {
		t.Errorf("chat cost = %v, want the sum 1.234567", chat.CostUSD)
	}
}

func TestChat_FinishAndAppendRefusals(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()
	turn := mustPost(t, s, "q", nil)
	id := turn.ReplyMessageID

	for _, bad := range []ChatMessageState{ChatStateReplying, "unknown", ""} {
		if err := s.FinishChatReply(ctx, id, bad, 0); !errors.Is(err, ErrValidation) {
			t.Errorf("FinishChatReply(%q) = %v, want ErrValidation", bad, err)
		}
	}
	if err := s.FinishChatReply(ctx, id, ChatStateDone, -1); !errors.Is(err, ErrValidation) {
		t.Errorf("negative cost = %v, want ErrValidation", err)
	}
	if err := s.FinishChatReply(ctx, turn.OwnerMessageID, ChatStateDone, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("finishing an owner message = %v, want ErrNotFound", err)
	}
	if err := s.FinishChatReply(ctx, 999, ChatStateDone, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("finishing a missing message = %v, want ErrNotFound", err)
	}
	if err := s.FinishChatReply(ctx, id, ChatStateStopped, 0.1); err != nil {
		t.Fatal(err)
	}
	// 終えた後の足し込み・二重の終わりは断る（止めた後に遅れて届いた分で、止めた返事を書き換えない）。
	if err := s.FinishChatReply(ctx, id, ChatStateDone, 9); !errors.Is(err, ErrValidation) {
		t.Errorf("finishing twice = %v, want ErrValidation", err)
	}
	if err := s.AppendChatSpeech(ctx, id, "遅れた発言"); !errors.Is(err, ErrValidation) {
		t.Errorf("speech after finish = %v, want ErrValidation", err)
	}
	if err := s.AppendChatCommand(ctx, id, ChatCommand{Command: "x", OK: true}); !errors.Is(err, ErrValidation) {
		t.Errorf("command after finish = %v, want ErrValidation", err)
	}
	chat, _ := s.ReadChat(ctx)
	if r := chat.Messages[1]; r.State != ChatStateStopped || r.Body != "" || r.CostUSD != 0.1 || len(r.Activity) != 0 {
		t.Errorf("the stopped reply was rewritten: %+v", r)
	}
}

func TestChat_CloseStartsANewSessionOnTheNextMessage(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()
	first := mustPost(t, s, "旧", nil)
	if err := s.CloseChat(ctx); !errors.Is(err, ErrChatReplying) {
		t.Fatalf("closing while replying = %v, want ErrChatReplying", err)
	}
	if err := s.MarkChatEngineStarted(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChatReply(ctx, first.ReplyMessageID, ChatStateDone, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseChat(ctx); err != nil {
		t.Fatal(err)
	}
	if chat, _ := s.ReadChat(ctx); chat.Session != nil || len(chat.Messages) != 0 || chat.CostUSD != 0 {
		t.Errorf("chat after closing = %+v, want empty", chat)
	}
	if err := s.CloseChat(ctx); err != nil {
		t.Errorf("closing with no open session = %v, want nil", err)
	}
	next := mustPost(t, s, "新", nil)
	if !next.NewSession || next.Resume || next.SessionID == first.SessionID || next.EngineSession == first.EngineSession {
		t.Errorf("next turn = %+v, want a fresh session distinct from %+v", next, first)
	}
	if n := chatCount(t, s, `SELECT COUNT(*) FROM chat_session`); n != 2 {
		t.Errorf("sessions = %d, want 2 (old closed, new open)", n)
	}
	if n := chatCount(t, s, `SELECT COUNT(*) FROM chat_session WHERE closed_at IS NULL`); n != 1 {
		t.Errorf("open sessions = %d, want 1", n)
	}
}

// AC-176: 開いている会話は高々 1 つ。ストアが 2 つ目を断る。
func TestChat_StoreRefusesASecondOpenSession(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	mustPost(t, s, "q", nil)
	insert := func(closed any) error {
		return s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO chat_session (engine_session, started_at, closed_at) VALUES ('33333333-3333-4333-8333-333333333333', '2026-11-22T00:00:00.000Z', ?)`, closed)
			return err
		})
	}
	// 同じ文が、閉じた会話なら通る（文の誤りで落ちているのではない）。
	if err := insert("2026-11-22T01:00:00.000Z"); err != nil {
		t.Fatalf("inserting a closed session: %v", err)
	}
	err := insert(nil)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("a second open chat_session: err = %v, want a UNIQUE constraint failure", err)
	}
}

func TestChat_StopInterruptedRepliesStopsOnlyTheReplying(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()
	fixed := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }

	if n, err := s.StopInterruptedChatReplies(ctx); err != nil || n != 0 {
		t.Fatalf("with nothing to stop = %d, %v", n, err)
	}
	first := mustPost(t, s, "1", nil)
	if err := s.FinishChatReply(ctx, first.ReplyMessageID, ChatStateDone, 2); err != nil {
		t.Fatal(err)
	}
	second := mustPost(t, s, "2", nil)
	if err := s.AppendChatSpeech(ctx, second.ReplyMessageID, "途中まで"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.StopInterruptedChatReplies(ctx); err != nil || n != 1 {
		t.Fatalf("StopInterruptedChatReplies = %d, %v; want 1", n, err)
	}
	chat, _ := s.ReadChat(ctx)
	var states []ChatMessageState
	for _, m := range chat.Messages {
		states = append(states, m.State)
	}
	if want := []ChatMessageState{ChatStateDone, ChatStateDone, ChatStateDone, ChatStateStopped}; !reflect.DeepEqual(states, want) {
		t.Errorf("states = %v, want %v", states, want)
	}
	if r := chat.Messages[3]; r.Body != "途中まで" || r.EndedAt == nil || !r.EndedAt.Equal(fixed) {
		t.Errorf("stopped reply = %+v, want the partial body kept and ended_at set", r)
	}
	if chat.Replying {
		t.Error("chat still replying")
	}
	// 止めた後は、次の発言を受ける。
	if _, err := s.PostChatMessage(ctx, "3", nil); err != nil {
		t.Errorf("post after stopping the interrupted reply: %v", err)
	}
}

func TestChat_StatesAreTheSevenValues(t *testing.T) {
	t.Parallel()
	var got []string
	for _, st := range ChatMessageStates() {
		got = append(got, string(st))
	}
	want := []string{"replying", "done", "stopped", "limit", "rate_limited", "session_lost", "failed"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChatMessageStates = %v, want %v", got, want)
	}
	if ends := ChatEndStates(); len(ends) != 6 || ends[0] == ChatStateReplying {
		t.Errorf("ChatEndStates = %v", ends)
	}
}

func TestChat_MarkEnvUsesTheNamesInOnePlace(t *testing.T) {
	t.Parallel()
	got := ChatMarkEnv("uuid-1", 42, ChatToolsFull)
	want := []string{"FLYWHEEL_CHAT_SESSION=uuid-1", "FLYWHEEL_CHAT_MESSAGE=42", "FLYWHEEL_CHAT_TOOLS=full"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChatMarkEnv = %v, want %v", got, want)
	}
	if ChatEnvSession != "FLYWHEEL_CHAT_SESSION" || ChatEnvMessage != "FLYWHEEL_CHAT_MESSAGE" || ChatEnvTools != "FLYWHEEL_CHAT_TOOLS" {
		t.Error("an environment variable name changed")
	}
}

// AC-177・AC-178: 会話と発言の行は、書き出して読み込むと元と同じ（開いた会話・閉じた会話・費用・コマンドの記録を含む）。
func TestChat_ExportImportRoundTripKeepsTheRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src, _ := newWorkspaceStore(t)
	chatTaskFixture(t, src)
	old := mustPost(t, src, "旧 \"quoted\"", &ChatViewing{ChallengeID: "C-1"})
	for _, err := range []error{
		src.MarkChatEngineStarted(ctx, old.SessionID),
		src.AppendChatSpeech(ctx, old.ReplyMessageID, "返事"),
		src.AppendChatCommand(ctx, old.ReplyMessageID, ChatCommand{Command: "flywheel status", OK: true}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := src.FinishChatReply(ctx, old.ReplyMessageID, ChatStateLimit, 0.75); err != nil {
		t.Fatal(err)
	}
	if err := src.CloseChat(ctx); err != nil {
		t.Fatal(err)
	}
	mustPost(t, src, "新", nil)

	exported := mustExport(t, src)
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(ctx, exported); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if again := mustExport(t, dst); string(again) != string(exported) {
		t.Fatal("export after import differs")
	}
	a, _ := src.ReadChat(ctx)
	b, _ := dst.ReadChat(ctx)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("open chat differs after the round trip:\n%+v\n%+v", a, b)
	}
	if n := chatCount(t, dst, `SELECT COUNT(*) FROM chat_session WHERE engine_started = 1 AND closed_at IS NOT NULL`); n != 1 {
		t.Errorf("closed started sessions after import = %d, want 1", n)
	}
	if n := chatCount(t, dst, `SELECT COUNT(*) FROM chat_message WHERE body = '返事'`); n != 1 {
		t.Errorf("reply body lost after import (matched %d)", n)
	}
	if n := chatCount(t, dst, `SELECT COUNT(*) FROM chat_session`); n != 2 {
		t.Errorf("sessions after import = %d, want 2", n)
	}
	if n := chatCount(t, dst, `SELECT COUNT(*) FROM chat_message WHERE cost_usd = 750000 AND activity LIKE '%flywheel status%' AND state = 'limit'`); n != 1 {
		t.Errorf("the closed session's reply lost a column after the round trip (matched %d)", n)
	}
	if n := chatCount(t, dst, `SELECT COUNT(*) FROM chat_message WHERE viewing_challenge_id = 'C-1' AND viewing_title = '最初の課題' AND viewing_task_id IS NULL`); n != 1 {
		t.Errorf("the owner message lost its viewing columns after the round trip (matched %d)", n)
	}
	if !strings.Contains(string(exported), `"chat_session"`) || !strings.Contains(string(exported), `"chat_message"`) {
		t.Error("the export lacks the chat tables")
	}
}

func TestChatToolsFromWorkspace_ReadsTheDeclarationFailClosed(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, content string) string {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
			t.Fatal(err)
		}
		if content == "NOSTORE" {
			content = `{"chat_tools":"full"}`
		} else if err := os.WriteFile(filepath.Join(dir, ".flywheel", "flywheel.db"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if content != "" {
			if err := os.WriteFile(filepath.Join(dir, ".flywheel", "agent.json"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	for _, tc := range []struct {
		name, content string
		want          ChatTools
	}{
		{"full", `{"chat_tools":"full"}`, ChatToolsFull},
		{"restricted", `{"chat_tools":"restricted"}`, ChatToolsRestricted},
		{"missing key", `{}`, ChatToolsRestricted},
		{"no file", "", ChatToolsRestricted},
		{"no store", "NOSTORE", ChatToolsRestricted},
		{"invalid", `{"chat_tools":"bogus"}`, ChatToolsRestricted},
		{"broken json", `{`, ChatToolsRestricted},
	} {
		if got := ChatToolsFromWorkspace(write(t, tc.content)); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
