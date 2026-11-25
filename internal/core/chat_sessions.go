package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// このファイルは会話の一覧と、閉じた会話の続き（docs/features/chat-session.md「閉じた会話の一覧と、続き」）の
// core の口を持つ。全文のファイルの有無は core では分からないので、呼び出し元が確かめる関数を渡す。

// ChatFirstMessageLimit は、会話の一覧に出す最初の発言の先頭の長さ（文字数）。
const ChatFirstMessageLimit = 80

// ChatSessionChallenge は会話に紐づいた課題。
type ChatSessionChallenge struct {
	ChallengeID string
	Title       string
}

// ChatSessionSummary は会話の一覧の要素。
type ChatSessionSummary struct {
	// EngineSession は会話の識別。
	EngineSession string
	StartedAt     time.Time
	ClosedAt      *time.Time
	// FirstMessage は最初のオーナーの発言の先頭（空白の並びは 1 つの空白にし、ChatFirstMessageLimit 文字で切る）。
	FirstMessage string
	// CostUSD は会話の発言の費用の合計。
	CostUSD float64
	// Challenges は紐づいた課題（紐づいた順。無ければ空のスライス）。
	Challenges []ChatSessionChallenge
	Open       bool
	// TranscriptAvailable は全文のファイルが見つかるか。
	TranscriptAvailable bool
}

func chatFirstMessageHead(body string) string {
	return cutRunes(strings.Join(strings.Fields(body), " "), ChatFirstMessageLimit)
}

// ListChatSessions は閉じた会話を含むすべての会話を、新しい順に返す（無ければ空のスライス）。
// transcriptAvailable は会話の識別ごとに全文のファイルが見つかるかを返す関数で、読み取りの後に呼ぶ。
// nil なら、どの会話も TranscriptAvailable は偽。
func (s *Store) ListChatSessions(ctx context.Context, transcriptAvailable func(engineSession string) bool) ([]ChatSessionSummary, error) {
	out := []ChatSessionSummary{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		index := map[int64]int{}
		if err := forEachRow(ctx, tx, `SELECT s.id, s.engine_session, s.started_at, s.closed_at,
				COALESCE((SELECT body FROM chat_message WHERE session_id = s.id AND role = ? ORDER BY id LIMIT 1), ''),
				COALESCE((SELECT SUM(cost_usd) FROM chat_message WHERE session_id = s.id), 0)
			FROM chat_session s ORDER BY s.id DESC`, []any{string(ChatRoleOwner)}, func(rows *sql.Rows) error {
			var (
				id             int64
				sum            ChatSessionSummary
				started, first string
				closed         sql.NullString
				micros         int64
			)
			if err := rows.Scan(&id, &sum.EngineSession, &started, &closed, &first, &micros); err != nil {
				return err
			}
			var err error
			if sum.StartedAt, err = parseTimestamp(started); err != nil {
				return err
			}
			if closed.Valid {
				t, err := parseTimestamp(closed.String)
				if err != nil {
					return err
				}
				sum.ClosedAt = &t
			}
			sum.Open = !closed.Valid
			sum.FirstMessage, sum.CostUSD, sum.Challenges = chatFirstMessageHead(first), microsToUSD(micros), []ChatSessionChallenge{}
			index[id] = len(out)
			out = append(out, sum)
			return nil
		}); err != nil {
			return err
		}
		return forEachRow(ctx, tx, `SELECT l.session_id, c.id, c.title FROM chat_link l JOIN challenge c ON c.id = l.challenge_id ORDER BY l.id`,
			nil, func(rows *sql.Rows) error {
				var sessionID, challengeID int64
				var title string
				if err := rows.Scan(&sessionID, &challengeID, &title); err != nil {
					return err
				}
				if i, ok := index[sessionID]; ok {
					out[i].Challenges = append(out[i].Challenges, ChatSessionChallenge{ChallengeID: formatChallengeID(challengeID), Title: title})
				}
				return nil
			})
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	if transcriptAvailable != nil {
		for i := range out {
			out[i].TranscriptAvailable = transcriptAvailable(out[i].EngineSession)
		}
	}
	return out, nil
}

func forEachRow(ctx context.Context, tx *sql.Tx, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ResumeChatSession は閉じた会話を開いた会話にする（いまの開いている会話は閉じる）。判定の順は、無い識別は
// ErrNotFound、すでに開いている会話は何もせず成功（返事が進行中でも）、それ以外でいまの会話の返事が進行中なら
// ErrChatReplying、それ以外で全文のファイルが見つからなければ ErrChatTranscriptUnavailable。断るときは何も変えない。
//
// transcriptAvailable は全文のファイルが見つかるかを返す関数で、書き込みの中で呼ぶ。ストアを触らず、すぐ返すこと。
// nil は「見つからない」として扱う。
func (s *Store) ResumeChatSession(ctx context.Context, engineSession string, transcriptAvailable func(engineSession string) bool) error {
	return s.chatWrite(ctx, func(tx *sql.Tx) error {
		var (
			targetID int64
			closed   sql.NullString
		)
		err := tx.QueryRowContext(ctx, `SELECT id, closed_at FROM chat_session WHERE engine_session = ?`, engineSession).Scan(&targetID, &closed)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !closed.Valid {
			return nil
		}
		current, err := openChatSession(ctx, tx)
		if err != nil {
			return err
		}
		if current != nil {
			if err := requireNoReply(ctx, tx, current.ID); err != nil {
				return err
			}
		}
		if transcriptAvailable == nil || !transcriptAvailable(engineSession) {
			return ErrChatTranscriptUnavailable
		}
		if current != nil {
			if err := closeChatSession(ctx, tx, current.ID, formatTimestamp(s.currentTime())); err != nil {
				return err
			}
		}
		return requireOneRow(tx.ExecContext(ctx, `UPDATE chat_session SET closed_at = NULL WHERE id = ?`, targetID))
	})
}

// requireNoReply は会話に進行中の返事が無いことを確かめる。あれば ErrChatReplying。
func requireNoReply(ctx context.Context, tx *sql.Tx, sessionID int64) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_message WHERE session_id = ? AND state = ?`, sessionID, string(ChatStateReplying)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrChatReplying
	}
	return nil
}

// closeChatSession は開いている会話を閉じる（閉じている会話は変えない）。
func closeChatSession(ctx context.Context, tx *sql.Tx, sessionID int64, at string) error {
	_, err := tx.ExecContext(ctx, `UPDATE chat_session SET closed_at = ? WHERE id = ? AND closed_at IS NULL`, at, sessionID)
	return err
}
