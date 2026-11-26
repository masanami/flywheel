package core

import (
	"context"
	"database/sql"
)

// このファイルは会話と課題の紐づけと、仕様づくりの費用の振り分け（docs/features/chat-session.md
// 「会話を課題に紐づける」「費用（仕様づくり）」）を持つ。仕様づくりの費用は返事の行の数える先の足し算で読み、
// 課題の予算とサイクルの費用には数えない。

// chatLinkAction は紐づけを起こした操作の種類（chat_link.action）。
type chatLinkAction string

const (
	chatLinkCreate   chatLinkAction = "create"
	chatLinkIngest   chatLinkAction = "ingest"
	chatLinkApprove  chatLinkAction = "approve"
	chatLinkReject   chatLinkAction = "reject"
	chatLinkAnswer   chatLinkAction = "answer"
	chatLinkWithdraw chatLinkAction = "withdraw"
	chatLinkBudget   chatLinkAction = "budget"
)

// chatLinkActionOfTransition は本人確認つきの遷移の操作の種類から、紐づけの操作の種類を返す。
func chatLinkActionOfTransition(act attestedAction) (chatLinkAction, bool) {
	switch act {
	case actionApprovePlan, actionApproveCompletion, actionApproveCompletionHold:
		return chatLinkApprove, true
	case actionReject:
		return chatLinkReject, true
	case actionAnswer, actionAnswerPlan:
		return chatLinkAnswer, true
	case actionWithdraw:
		return chatLinkWithdraw, true
	}
	return "", false
}

// linkChatOperation は経路 chat で成立した操作の会話と課題を紐づける。経路が chat でなければ何もしない。
// 操作の書き込みと同じトランザクションの中で、chatGate を通った後、操作が成立するところで呼ぶ。
func linkChatOperation(ctx context.Context, tx *sql.Tx, att Attestation, challengeID int64, action chatLinkAction, at string) error {
	if att.chat == nil {
		return nil
	}
	return linkChatChallenge(ctx, tx, att.chat.resolved, challengeID, action, at)
}

// linkChatChallenge は会話と課題を紐づけ（組ごとに 1 つ。2 回目からは最初の値を残す）、会話のまだ数える先の
// 決まっていない返事の行（進行中の返事を含む）の数える先をこの課題にする。2 回目の紐づけでも振り分けは行う。
func linkChatChallenge(ctx context.Context, tx *sql.Tx, mark resolvedChatMark, challengeID int64, action chatLinkAction, at string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO chat_link (session_id, challenge_id, action, message_id, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (session_id, challenge_id) DO NOTHING`, mark.sessionID, challengeID, string(action), mark.replyMessageID, at); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE chat_message SET charged_challenge_id = ? WHERE session_id = ? AND role = ? AND charged_challenge_id IS NULL`,
		challengeID, mark.sessionID, string(ChatRoleOrchestrator))
	return err
}

// loadChallengeSpec は課題の仕様づくりの費用と、紐づいた会話（紐づいた順。無ければ空のスライス）を読む。
func loadChallengeSpec(ctx context.Context, tx *sql.Tx, challengeID int64) (float64, []ChallengeChatSession, error) {
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_usd), 0) FROM chat_message WHERE charged_challenge_id = ?`, challengeID).Scan(&total); err != nil {
		return 0, nil, err
	}
	var drafting int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_usd), 0) FROM run WHERE challenge_id = ? AND budget_bucket = ?`,
		challengeID, string(budgetBucketSpec)).Scan(&drafting); err != nil {
		return 0, nil, err
	}
	total += drafting
	sessions := []ChallengeChatSession{}
	err := forEachRow(ctx, tx, `SELECT s.engine_session, s.started_at,
			COALESCE((SELECT body FROM chat_message WHERE session_id = s.id AND role = ? ORDER BY id LIMIT 1), ''),
			COALESCE((SELECT SUM(cost_usd) FROM chat_message WHERE session_id = s.id AND charged_challenge_id = l.challenge_id), 0)
		FROM chat_link l JOIN chat_session s ON s.id = l.session_id WHERE l.challenge_id = ? ORDER BY l.id`,
		[]any{string(ChatRoleOwner), challengeID}, func(rows *sql.Rows) error {
			var (
				c              ChallengeChatSession
				started, first string
				micros         int64
			)
			if err := rows.Scan(&c.EngineSession, &started, &first, &micros); err != nil {
				return err
			}
			var err error
			if c.StartedAt, err = parseTimestamp(started); err != nil {
				return err
			}
			c.FirstMessage, c.CostUSD = chatFirstMessageHead(first), microsToUSD(micros)
			sessions = append(sessions, c)
			return nil
		})
	if err != nil {
		return 0, nil, err
	}
	return microsToUSD(total), sessions, nil
}
