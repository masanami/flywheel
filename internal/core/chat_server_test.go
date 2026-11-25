package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// このファイルは、サーバがプロセスの寿命のために使う core の口の試験（実ストア）。

func TestChat_HasInterruptedChat(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()
	want := func(w bool) {
		t.Helper()
		if got, err := s.HasInterruptedChat(ctx); err != nil || got != w {
			t.Fatalf("HasInterruptedChat = %v, %v; want %v", got, err, w)
		}
	}
	want(false)
	turn := mustPost(t, s, "q", nil)
	want(true)
	must(t, s.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, ChatStateDone, nil))
	want(false)
	// 返事が進行中でないのに答えを待ったまま残った確認も、片付けの対象。
	must(t, s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO chat_prompt (message_id, request_id, kind, tool, target, state, created_at)
			VALUES (?, 'r', 'permission', 'Bash', 'ls', 'pending', '2026-11-25T00:00:00.000Z')`, turn.ReplyMessageID)
		return err
	}))
	want(true)
	if _, err := s.StopInterruptedChatReplies(ctx); err != nil {
		t.Fatal(err)
	}
	want(false)
}

func TestChat_RecordDeniedChatPromptOnTheLastReply(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	ctx := context.Background()
	first := mustPost(t, s, "1", nil)
	must(t, s.FinishChatReplyWithTotal(ctx, first.ReplyMessageID, ChatStateDone, nil))
	second := mustPost(t, s, "2", nil)
	must(t, s.FinishChatReplyWithTotal(ctx, second.ReplyMessageID, ChatStateDone, nil))

	permission := ChatPromptRequest{RequestID: "r1", Kind: ChatPromptPermission, Tool: "Bash", Target: "rm x"}
	question := ChatPromptRequest{RequestID: "r2", Kind: ChatPromptQuestion, Tool: "AskUserQuestion", Questions: []ChatQuestion{{Question: "どちら?", Options: []ChatQuestionOption{}}}}
	for _, req := range []ChatPromptRequest{permission, question} {
		must(t, s.RecordDeniedChatPrompt(ctx, first.SessionID, req, "後で"))
	}
	chat, err := s.ReadChat(ctx)
	must(t, err)
	last := chat.Messages[len(chat.Messages)-1]
	if last.ID != second.ReplyMessageID || len(last.Prompts) != 2 || len(chat.Messages[1].Prompts) != 0 {
		t.Fatalf("prompts = %+v", chat.Messages)
	}
	for _, p := range last.Prompts {
		if p.State != ChatPromptDenied || p.AnsweredAt == nil || p.Answer == nil || p.Answer.Decision != ChatDecisionDeny || p.Answer.Message != "後で" {
			t.Errorf("prompt = %+v", p)
		}
	}
	if err := s.RecordDeniedChatPrompt(ctx, 999, permission, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session = %v, want ErrNotFound", err)
	}
	if err := s.RecordDeniedChatPrompt(ctx, first.SessionID, ChatPromptRequest{}, "x"); !errors.Is(err, ErrValidation) {
		t.Errorf("invalid request = %v, want ErrValidation", err)
	}
}
