package core

import (
	"context"
	"database/sql"
	"errors"
)

// このファイルは、会話の実行エンジンのプロセスを持つ呼び出し元（サーバ）が使う core の口を持つ
// （docs/features/chat-session.md「会話と Claude Code のプロセス」）。

// HasInterruptedChat は、起動の片付け（StopInterruptedChatReplies）の対象があるかを返す（何も書かない）。
// 対象は、進行中のまま残った返事と、答えを待ったまま残った確認。閉じた会話の分も数える。
func (s *Store) HasInterruptedChat(ctx context.Context) (bool, error) {
	var found bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM chat_message WHERE state = ?) OR EXISTS (SELECT 1 FROM chat_prompt WHERE state = ?)`,
			string(ChatStateReplying), string(ChatPromptPending)).Scan(&found)
	})
	if err != nil {
		return false, classifyReadWriteErr(err)
	}
	return found, nil
}

// RecordDeniedChatPrompt は、返事の合間に届いて断った確認を、会話の最後の返事の行に denied で 1 件記録する
// （答えを待つ確認は作らない）。message は断る答えに添えた文。返事の行の状態は問わない。形の誤った確認は
// ErrValidation、会話に返事の行が無ければ ErrNotFound で、何も書かない。
func (s *Store) RecordDeniedChatPrompt(ctx context.Context, sessionID int64, req ChatPromptRequest, message string) error {
	if !req.valid() {
		return ErrValidation
	}
	var questions any
	if req.Kind == ChatPromptQuestion {
		b, err := marshalChatJSON(req.Questions)
		if err != nil {
			return err
		}
		questions = b
	}
	pa := storedPermissionAnswer{Decision: ChatDecisionDeny}
	if message != "" {
		pa.Message = &message
	}
	answer, err := marshalChatJSON(pa)
	if err != nil {
		return err
	}
	return s.chatWrite(ctx, func(tx *sql.Tx) error {
		var replyID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM chat_message WHERE session_id = ? AND role = ? ORDER BY id DESC LIMIT 1`,
			sessionID, string(ChatRoleOrchestrator)).Scan(&replyID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		at, err := s.nextChatAnsweredAt(ctx, tx, replyID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO chat_prompt (message_id, request_id, kind, tool, target, questions, state, answer, created_at, answered_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, replyID, req.RequestID, string(req.Kind), req.Tool, req.Target, questions,
			string(ChatPromptDenied), answer, formatTimestamp(s.currentTime()), at)
		return err
	})
}
