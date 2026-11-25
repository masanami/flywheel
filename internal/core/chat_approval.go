package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// このファイルは経路 chat（画面の対話の欄から起動された flywheel のコマンド）の本人確認の層を持つ
// （docs/features/core-redesign-s4d.md §欄から受ける承認・回答）。
//
//   - 印（会話の識別だけ）を持つ本人確認（NewChatMarkVerifier）。Verify は印が空でないことだけを見る。
//   - 操作を行う core の処理が、同じトランザクションの中で chatGate を呼び、印の検査をして、最後のオーナーの発言を
//     ストアから引いて記録の手がかり（relay_session・relay_quote）を埋める。
//   - 操作が成立したら、同じトランザクションの中で会話と課題を紐づける（chat_link.go）。
//   - 不可逆操作の承認と、反映の承認を含む完了の承認は、宣言の chat_approvable_operations にある種類だけを受ける。

// ErrChatMarkInvalid は経路 chat の印の検査に通らなかったことを表す。errors.Is(err, ErrVerificationRejected) も真。
var ErrChatMarkInvalid error = chatMarkError{}

// ErrChatNotApprovable は経路 chat で、宣言の chat_approvable_operations に無い種類の承認を求められたことを表す。
// errors.Is(err, ErrVerificationRejected) も真。
var ErrChatNotApprovable error = chatNotApprovableError{}

type chatMarkError struct{ reason string }

func (e chatMarkError) Error() string {
	if e.reason == "" {
		return "core: the chat mark is not valid"
	}
	return "core: the chat mark is not valid: " + e.reason
}

func (chatMarkError) Unwrap() error { return ErrVerificationRejected }

func (chatMarkError) Is(target error) bool { _, ok := target.(chatMarkError); return ok }

type chatNotApprovableError struct{}

func (chatNotApprovableError) Error() string {
	return "対話の欄からは承認できません。ターミナルで行ってください"
}

func (chatNotApprovableError) Unwrap() error { return ErrVerificationRejected }

// ChatSessionMark は対話の欄の印（会話の識別＝FLYWHEEL_CHAT_SESSION の値）。本人確認を通らない記録の口
// （作成・指定つきの取り込み）へ渡す。
type ChatSessionMark struct {
	Session string
}

// chatMark は経路 chat の本人確認が持つ印。resolved は chatGate がトランザクションの中で埋める。
type chatMark struct {
	session  string
	resolved resolvedChatMark
}

func (m *chatMark) complete() bool { return strings.TrimSpace(m.session) != "" }

// resolvedChatMark は印の検査を通った結果。
type resolvedChatMark struct {
	// evidence は記録の手がかり（Session＝会話の識別・Quote＝記録の「発言」）。
	evidence RelayEvidence
	// sessionID は会話の行の ID。replyMessageID は進行中の返事の行の ID。
	sessionID, replyMessageID int64
}

// chatMarkVerifier は経路 chat の Verifier。印だけを持つ。
type chatMarkVerifier struct{ session string }

// NewChatMarkVerifier は印（会話の識別＝FLYWHEEL_CHAT_SESSION）から、経路 chat の本人確認を作る。
// Verify(ChannelChat, …) だけがこれを受ける。
func NewChatMarkVerifier(session string) Verifier {
	return &chatMarkVerifier{session: session}
}

func (*chatMarkVerifier) Method() Verification { return VerificationRelayed }

// Evidence は印を返す（Session＝会話の識別）。発言の文面は持たない。
func (v *chatMarkVerifier) Evidence() RelayEvidence {
	return RelayEvidence{Session: v.session}
}

// Confirm は端末を使わず、OS のログインユーザー名を actor として返す。
func (*chatMarkVerifier) Confirm(_, _ string) (string, error) { return CurrentActor() }

// chatGate は経路 chat の操作の入口の検査。経路が chat でなければ何もしない。操作を行う書き込みトランザクション
// の中（何も書く前）で呼ぶ。印を検査して att の手がかりを埋め、act が不可逆操作の承認（kind はその種類）
// または反映の承認を含む完了の承認なら、宣言の chat_approvable_operations を照らす。断るときは何も書かない。
func (s *Store) chatGate(ctx context.Context, tx *sql.Tx, att Attestation, act attestedAction, kind OperationKind) error {
	if att.channel != ChannelChat {
		return nil
	}
	if att.chat == nil || !att.chat.complete() {
		return chatMarkError{reason: "印が空です"}
	}
	att.chat.resolved = resolvedChatMark{}
	resolved, err := resolveChatMark(ctx, tx, att.chat.session)
	if err != nil {
		return err
	}
	var need OperationKind
	switch act {
	case actionOperationApprove:
		need = kind
	case actionApproveCompletion:
		need = OperationKindRelease
	default:
		att.chat.resolved = resolved
		return nil
	}
	decl, err := LoadAgentDeclaration(s.workspace)
	if err != nil {
		return err
	}
	if !decl.ChatCanApprove(need) {
		return ErrChatNotApprovable
	}
	att.chat.resolved = resolved
	return nil
}

// resolveChatMark は印を検査する。印の会話が開いていて、その会話の最後のオーナーの発言への返事が進行中で
// なければ ErrChatMarkInvalid。発言は印からではなくストアで引く。
func resolveChatMark(ctx context.Context, tx *sql.Tx, session string) (resolvedChatMark, error) {
	var none resolvedChatMark
	session = strings.TrimSpace(session)
	if session == "" {
		return none, chatMarkError{reason: "印が空です"}
	}
	var (
		out    resolvedChatMark
		closed sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT id, closed_at FROM chat_session WHERE engine_session = ?`, session).Scan(&out.sessionID, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return none, chatMarkError{reason: "印の会話がストアにありません"}
	}
	if err != nil {
		return none, err
	}
	if closed.Valid {
		return none, chatMarkError{reason: "印の会話が閉じています"}
	}
	var (
		ownerID int64
		body    string
	)
	err = tx.QueryRowContext(ctx, `SELECT id, body FROM chat_message WHERE session_id = ? AND role = ? ORDER BY id DESC LIMIT 1`,
		out.sessionID, string(ChatRoleOwner)).Scan(&ownerID, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return none, chatMarkError{reason: "印の会話にオーナーの発言がありません"}
	}
	if err != nil {
		return none, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM chat_message WHERE session_id = ? AND id > ? AND state = ? ORDER BY id LIMIT 1`,
		out.sessionID, ownerID, string(ChatStateReplying)).Scan(&out.replyMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return none, chatMarkError{reason: "印の会話の最後のオーナーの発言への返事が進行中ではありません"}
	}
	if err != nil {
		return none, err
	}
	quote, err := chatRelayQuote(ctx, tx, body, out.replyMessageID)
	if err != nil {
		return none, err
	}
	out.evidence = RelayEvidence{Session: session, Quote: quote}
	return out, nil
}

// chatRelayQuote は経路 chat の記録の「発言」を作る。オーナーの発言の文面に、その返事の中で答えた問いを答えた順に
// つなげる（問いごとに空行を挟んで「問い: <問いの文>」「答え: <答えの文>」の 2 行）。1 つの確認が複数の問いを持つ
// ときは、問われた順に並べる。
func chatRelayQuote(ctx context.Context, tx *sql.Tx, body string, replyMessageID int64) (string, error) {
	var quote strings.Builder
	quote.WriteString(body)
	err := forEachRow(ctx, tx, `SELECT `+chatPromptSelectColumns+` FROM chat_prompt p
		WHERE p.message_id = ? AND p.kind = ? AND p.state = ? ORDER BY p.answered_at, p.id`,
		[]any{replyMessageID, string(ChatPromptQuestion), string(ChatPromptAnswered)}, func(rows *sql.Rows) error {
			p, err := scanChatPrompt(rows)
			if err != nil {
				return err
			}
			if p.Answer == nil {
				return nil
			}
			for _, q := range p.Questions {
				fmt.Fprintf(&quote, "\n\n問い: %s\n答え: %s", q.Question, p.Answer.Answers[q.Question])
			}
			return nil
		})
	if err != nil {
		return "", err
	}
	return quote.String(), nil
}

// CheckChatMark は印の検査だけを行う（何も書かない）。呼び出し元が、操作の前（上流を読む前など）に印を確かめる
// ための口。検査の内容は本人確認を通る操作の検査と同じ（resolveChatMark）。満たさなければ ErrChatMarkInvalid
// （errors.Is(err, ErrVerificationRejected) も真）。
func (s *Store) CheckChatMark(ctx context.Context, session string) error {
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		_, err := resolveChatMark(ctx, tx, session)
		return err
	})
	return classifyReadWriteErr(err)
}
