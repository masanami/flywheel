package core

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// このファイルは経路 chat（画面の対話の欄から起動された flywheel のコマンド）の本人確認の層を持つ
// （docs/features/core-redesign-s4d.md §欄から受ける承認・回答）。
//
//   - 印（会話の識別・発言の識別）の 2 値を持つ本人確認（NewChatMarkVerifier）。Verify は 2 値が空でないことだけを見る。
//   - 操作を行う core の処理が、同じトランザクションの中で chatGate を呼び、印の検査をして、発言の文面を
//     ストアから引いて記録の手がかり（relay_session・relay_quote）を埋める。
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

// chatMark は経路 chat の印。resolved は chatGate がトランザクションの中で埋める記録の手がかり
// （Session＝会話の識別・Quote＝印の発言の body）。
type chatMark struct {
	session, message string
	resolved         RelayEvidence
}

func (m *chatMark) complete() bool {
	return strings.TrimSpace(m.session) != "" && strings.TrimSpace(m.message) != ""
}

// chatMarkVerifier は経路 chat の Verifier。印の 2 値だけを持つ。
type chatMarkVerifier struct{ session, message string }

// NewChatMarkVerifier は印（会話の識別＝FLYWHEEL_CHAT_SESSION・発言の識別＝FLYWHEEL_CHAT_MESSAGE）から、
// 経路 chat の本人確認を作る。Verify(ChannelChat, …) だけがこれを受ける。
func NewChatMarkVerifier(session, message string) Verifier {
	return &chatMarkVerifier{session: session, message: message}
}

func (*chatMarkVerifier) Method() Verification { return VerificationRelayed }

// Evidence は印を返す（Session＝会話の識別・Quote＝発言の識別）。発言の文面ではない。
func (v *chatMarkVerifier) Evidence() RelayEvidence {
	return RelayEvidence{Session: v.session, Quote: v.message}
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
	att.chat.resolved = RelayEvidence{}
	ev, err := resolveChatMark(ctx, tx, att.chat)
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
		att.chat.resolved = ev
		return nil
	}
	decl, err := LoadAgentDeclaration(s.workspace)
	if err != nil {
		return err
	}
	if !decl.ChatCanApprove(need) {
		return ErrChatNotApprovable
	}
	att.chat.resolved = ev
	return nil
}

// resolveChatMark は印を検査し、記録の手がかりを返す。会話が開いている・印の発言がその会話の最後のオーナーの発言
// である・その発言への返事が進行中である・発言の識別がストアにある、のすべてを満たさなければ ErrChatMarkInvalid。
func resolveChatMark(ctx context.Context, tx *sql.Tx, m *chatMark) (RelayEvidence, error) {
	msgID, err := strconv.ParseInt(strings.TrimSpace(m.message), 10, 64)
	if err != nil || msgID <= 0 {
		return RelayEvidence{}, chatMarkError{reason: "発言の識別の形が誤りです"}
	}
	var (
		sessID    int64
		engine    string
		closed    sql.NullString
		role, msg string
	)
	err = tx.QueryRowContext(ctx, `SELECT s.id, s.engine_session, s.closed_at, m.role, m.body
		FROM chat_message m JOIN chat_session s ON s.id = m.session_id WHERE m.id = ?`, msgID).
		Scan(&sessID, &engine, &closed, &role, &msg)
	if errors.Is(err, sql.ErrNoRows) {
		return RelayEvidence{}, chatMarkError{reason: "印の発言がストアにありません"}
	}
	if err != nil {
		return RelayEvidence{}, err
	}
	if engine != strings.TrimSpace(m.session) {
		return RelayEvidence{}, chatMarkError{reason: "印の発言が印の会話のものではありません"}
	}
	if closed.Valid {
		return RelayEvidence{}, chatMarkError{reason: "印の会話が閉じています"}
	}
	if role != string(ChatRoleOwner) {
		return RelayEvidence{}, chatMarkError{reason: "印の発言がオーナーの発言ではありません"}
	}
	var last int64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(id) FROM chat_message WHERE session_id = ? AND role = ?`, sessID, string(ChatRoleOwner)).Scan(&last); err != nil {
		return RelayEvidence{}, err
	}
	if last != msgID {
		return RelayEvidence{}, chatMarkError{reason: "印の発言が、その会話の最後のオーナーの発言ではありません"}
	}
	var replying int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_message WHERE session_id = ? AND id > ? AND state = ?`, sessID, msgID, string(ChatStateReplying)).Scan(&replying); err != nil {
		return RelayEvidence{}, err
	}
	if replying == 0 {
		return RelayEvidence{}, chatMarkError{reason: "印の発言への返事が進行中ではありません"}
	}
	return RelayEvidence{Session: engine, Quote: msg}, nil
}
