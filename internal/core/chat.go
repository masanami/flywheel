package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// このファイルは画面の対話の欄（docs/features/core-redesign-s4d.md S4d-1）の会話・発言の core の口を持つ。
// 会話（chat_session）と発言（chat_message）の正本はストアで、実行エンジンの側の会話のファイルには頼らない。
// 実行エンジンの起動（チャットの実行）は internal/invoker が実装する ChatInvoker の仕事で、ここは記録の規則だけを持つ。

// ErrChatReplying は返事が進行中（state が replying の発言がある）のとき、次の発言を受けないことを表す。
var ErrChatReplying = errors.New("core: a chat reply is still in progress")

// 印の環境変数の名前（欄から起動された実行エンジンの環境に入れる）。名前はここだけが持つ。
const (
	// ChatEnvSession の値は、会話の識別（ChatSession.EngineSession）。
	ChatEnvSession = "FLYWHEEL_CHAT_SESSION"
	// ChatEnvMessage の値は、いま答えているオーナーの発言の識別（ChatMessage.ID の 10 進表記）。
	ChatEnvMessage = "FLYWHEEL_CHAT_MESSAGE"
	// ChatEnvTools の値は、道具の範囲（restricted・full）。
	ChatEnvTools = "FLYWHEEL_CHAT_TOOLS"
)

// ChatMarkEnv は欄から起動した実行エンジンの環境に足す印（KEY=VALUE の一覧）を返す。
func ChatMarkEnv(engineSession string, ownerMessageID int64, tools ChatTools) []string {
	return []string{
		ChatEnvSession + "=" + engineSession,
		ChatEnvMessage + "=" + strconv.FormatInt(ownerMessageID, 10),
		ChatEnvTools + "=" + string(tools),
	}
}

// ChatRole は発言の主。
type ChatRole string

// ChatRole の 2 値。
const (
	ChatRoleOwner        ChatRole = "owner"
	ChatRoleOrchestrator ChatRole = "orchestrator"
)

// ChatMessageState は発言の状態。オーナーの発言は done。オーケストレーターの返事は replying から始まり、
// 終わり方に応じてほかの 6 値のいずれかで終わる。
type ChatMessageState string

// ChatMessageState の 7 値。
const (
	ChatStateReplying    ChatMessageState = "replying"
	ChatStateDone        ChatMessageState = "done"
	ChatStateStopped     ChatMessageState = "stopped"
	ChatStateLimit       ChatMessageState = "limit"
	ChatStateRateLimited ChatMessageState = "rate_limited"
	ChatStateSessionLost ChatMessageState = "session_lost"
	ChatStateFailed      ChatMessageState = "failed"
)

// chatEndStates は返事の終わり方になれる状態（replying 以外の 6 値）。
var chatEndStates = []ChatMessageState{
	ChatStateDone, ChatStateStopped, ChatStateLimit, ChatStateRateLimited, ChatStateSessionLost, ChatStateFailed,
}

// ChatMessageStates は発言の状態の閉集合（7 値）を返す。
func ChatMessageStates() []ChatMessageState {
	return append([]ChatMessageState{ChatStateReplying}, chatEndStates...)
}

// ChatEndStates は返事の終わり方になれる状態（replying 以外の 6 値）を返す。
func ChatEndStates() []ChatMessageState { return append([]ChatMessageState(nil), chatEndStates...) }

func isChatEndState(st ChatMessageState) bool {
	for _, v := range chatEndStates {
		if v == st {
			return true
		}
	}
	return false
}

// ChatViewing はオーナーが発言を送ったときに欄が見せていたもの（課題・タスク・題名）。何も見ていなければ nil。
// PostChatMessage が受けるのは ChallengeID と TaskID だけで、題名は core が引く（呼び出し元が付けた Title・
// TaskTitle は使わない）。Title は課題の題名で、発言の行に残る。TaskTitle はタスクの題名で、発言の行には残さず、
// 実行エンジンへの入力のために ChatTurn.Viewing だけに入る。
type ChatViewing struct {
	ChallengeID string
	TaskID      string
	Title       string
	TaskTitle   string
}

func (v *ChatViewing) empty() bool {
	return v == nil || (v.ChallengeID == "" && v.TaskID == "" && v.Title == "")
}

// ChatCommand は返事の途中で実行したコマンド 1 つ（実行した行と、成功したか）。
type ChatCommand struct {
	Command string `json:"command"`
	OK      bool   `json:"ok"`
}

// ChatSession は会話。
type ChatSession struct {
	ID int64
	// EngineSession は flywheel が決めた会話の識別（小文字の UUID）。
	EngineSession string
	// EngineStarted は実行エンジンの出力に会話の始まりの行が現れたか。真なら次は続きの指定で起動する。
	EngineStarted bool
	StartedAt     time.Time
	ClosedAt      *time.Time
}

// ChatMessage は会話の中の発言 1 つ。
type ChatMessage struct {
	ID        int64
	SessionID int64
	Role      ChatRole
	Body      string
	State     ChatMessageState
	CostUSD   float64
	Viewing   *ChatViewing
	Activity  []ChatCommand
	CreatedAt time.Time
	EndedAt   *time.Time
}

// Chat は、いまの会話（開いている会話）。会話が無ければ Session は nil で、Messages は空。
type Chat struct {
	Session  *ChatSession
	Messages []ChatMessage
	// CostUSD は会話の発言の費用の合計。
	CostUSD float64
	// Replying は返事が進行中か。
	Replying bool
}

// ChatTurn は発言を記録した結果。呼び出し元（サーバ）が実行エンジンを起動するのに要る値を持つ。
type ChatTurn struct {
	// OwnerMessageID はオーナーの発言の ID。ReplyMessageID は作った返事の行（state は replying）の ID。
	OwnerMessageID int64
	ReplyMessageID int64
	SessionID      int64
	// EngineSession は会話の識別。Resume が真なら続きの指定で、偽なら新しい会話の指定で起動する。
	EngineSession string
	Resume        bool
	// NewSession は、この発言で会話を作ったか。
	NewSession bool
	// Viewing は core が題名を引いた「いま見ているもの」（無ければ nil）。ChatLaunchInput.Viewing へそのまま渡す。
	Viewing *ChatViewing
}

// ReadChat はいまの会話を読む。
func (s *Store) ReadChat(ctx context.Context) (*Chat, error) {
	var out *Chat
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = readChat(ctx, tx)
		return err
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return out, nil
}

func readChat(ctx context.Context, tx *sql.Tx) (*Chat, error) {
	sess, err := openChatSession(ctx, tx)
	if err != nil {
		return nil, err
	}
	chat := &Chat{Messages: []ChatMessage{}}
	if sess == nil {
		return chat, nil
	}
	chat.Session = sess
	rows, err := tx.QueryContext(ctx, `SELECT id, session_id, role, body, state, cost_usd, viewing_challenge_id, viewing_task_id, viewing_title, activity, created_at, ended_at
		FROM chat_message WHERE session_id = ? ORDER BY id`, sess.ID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var total int64
	for rows.Next() {
		var (
			m                     ChatMessage
			role, state           string
			micros                int64
			vc, vt, vtitle, ended sql.NullString
			activity, created     string
		)
		if err := rows.Scan(&m.ID, &m.SessionID, &role, &m.Body, &state, &micros, &vc, &vt, &vtitle, &activity, &created, &ended); err != nil {
			return nil, err
		}
		m.Role, m.State = ChatRole(role), ChatMessageState(state)
		m.CostUSD = microsToUSD(micros)
		total += micros
		if vc.Valid || vt.Valid || vtitle.Valid {
			m.Viewing = &ChatViewing{ChallengeID: vc.String, TaskID: vt.String, Title: vtitle.String}
		}
		m.Activity = []ChatCommand{}
		if err := json.Unmarshal([]byte(activity), &m.Activity); err != nil {
			return nil, fmt.Errorf("chat_message %d: activity is not a JSON array: %w", m.ID, err)
		}
		if m.CreatedAt, err = parseTimestamp(created); err != nil {
			return nil, err
		}
		if ended.Valid {
			t, err := parseTimestamp(ended.String)
			if err != nil {
				return nil, err
			}
			m.EndedAt = &t
		}
		if m.State == ChatStateReplying {
			chat.Replying = true
		}
		chat.Messages = append(chat.Messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	chat.CostUSD = microsToUSD(total)
	return chat, nil
}

// openChatSession は開いている会話（closed_at が NULL）を返す。無ければ nil。
func openChatSession(ctx context.Context, tx *sql.Tx) (*ChatSession, error) {
	var (
		sess      ChatSession
		started   int
		startedAt string
		closedAt  sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT id, engine_session, engine_started, started_at, closed_at FROM chat_session WHERE closed_at IS NULL`).
		Scan(&sess.ID, &sess.EngineSession, &started, &startedAt, &closedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.EngineStarted = started != 0
	if sess.StartedAt, err = parseTimestamp(startedAt); err != nil {
		return nil, err
	}
	return &sess, nil
}

// PostChatMessage はオーナーの発言を記録し、返事の行（state は replying）を作って返す。開いている会話が無ければ
// 会話を作り、会話の識別（UUID）を決める。返事が進行中なら ErrChatReplying で断り、何も書かない。
// 発言が空（空白だけを含む）なら ErrValidation。
//
// viewing は識別（課題・タスク）だけを使い、同じ書き込みトランザクションの中で検査して題名を引く。引けない識別
// （存在しない課題・存在しないタスク・形の誤り・その課題に属さないタスク・課題の無いタスクだけ）は ErrValidation で
// 断り、何も書かない。
func (s *Store) PostChatMessage(ctx context.Context, text string, viewing *ChatViewing) (*ChatTurn, error) {
	if strings.TrimSpace(text) == "" {
		return nil, ErrValidation
	}
	var turn *ChatTurn
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		now := formatTimestamp(s.currentTime())
		sess, err := openChatSession(ctx, tx)
		if err != nil {
			return err
		}
		t := &ChatTurn{}
		if sess == nil {
			id, err := newLowercaseUUIDv4()
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO chat_session (engine_session, engine_started, started_at) VALUES (?, 0, ?)`, id, now)
			if err != nil {
				return err
			}
			sid, err := res.LastInsertId()
			if err != nil {
				return err
			}
			sess = &ChatSession{ID: sid, EngineSession: id}
			t.NewSession = true
		} else {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_message WHERE session_id = ? AND state = ?`, sess.ID, string(ChatStateReplying)).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrChatReplying
			}
		}
		var vc, vt, vtitle any
		resolved, err := resolveChatViewing(ctx, tx, viewing)
		if err != nil {
			return err
		}
		if resolved != nil {
			vc, vt, vtitle = nullIfEmpty(resolved.ChallengeID), nullIfEmpty(resolved.TaskID), nullIfEmpty(resolved.Title)
		}
		t.Viewing = resolved
		res, err := tx.ExecContext(ctx, `INSERT INTO chat_message (session_id, role, body, state, viewing_challenge_id, viewing_task_id, viewing_title, created_at, ended_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, sess.ID, string(ChatRoleOwner), text, string(ChatStateDone), vc, vt, vtitle, now, now)
		if err != nil {
			return err
		}
		if t.OwnerMessageID, err = res.LastInsertId(); err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx, `INSERT INTO chat_message (session_id, role, body, state, created_at) VALUES (?, ?, '', ?, ?)`,
			sess.ID, string(ChatRoleOrchestrator), string(ChatStateReplying), now)
		if err != nil {
			return err
		}
		if t.ReplyMessageID, err = res.LastInsertId(); err != nil {
			return err
		}
		t.SessionID, t.EngineSession, t.Resume = sess.ID, sess.EngineSession, sess.EngineStarted
		turn = t
		return nil
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return turn, nil
}

// resolveChatViewing は識別を検査し、題名を引いた ChatViewing を返す。何も見ていなければ nil。
func resolveChatViewing(ctx context.Context, tx *sql.Tx, in *ChatViewing) (*ChatViewing, error) {
	if in.empty() || (in.ChallengeID == "" && in.TaskID == "") {
		return nil, nil
	}
	if in.ChallengeID == "" {
		return nil, fmt.Errorf("%w: a task needs its challenge", ErrValidation)
	}
	cid, ok := parseChallengeID(in.ChallengeID)
	if !ok {
		return nil, fmt.Errorf("%w: invalid challenge id %q", ErrValidation, in.ChallengeID)
	}
	var title string
	if err := tx.QueryRowContext(ctx, `SELECT title FROM challenge WHERE id = ?`, cid).Scan(&title); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: no such challenge %q", ErrValidation, in.ChallengeID)
		}
		return nil, err
	}
	out := &ChatViewing{ChallengeID: formatChallengeID(cid), Title: title}
	if in.TaskID == "" {
		return out, nil
	}
	tid, ok := parseTaskID(in.TaskID)
	if !ok {
		return nil, fmt.Errorf("%w: invalid task id %q", ErrValidation, in.TaskID)
	}
	var owner int64
	if err := tx.QueryRowContext(ctx, `SELECT challenge_id, title FROM task WHERE id = ?`, tid).Scan(&owner, &out.TaskTitle); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: no such task %q", ErrValidation, in.TaskID)
		}
		return nil, err
	}
	if owner != cid {
		return nil, fmt.Errorf("%w: task %q does not belong to challenge %q", ErrValidation, in.TaskID, in.ChallengeID)
	}
	out.TaskID = formatTaskID(tid)
	return out, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// MarkChatEngineStarted は、実行エンジンの出力に会話の始まりの行が現れたことを記録する（次からは続きの指定で起動する）。
// 返事の終わり方によらず、行が現れた時点で呼ぶ。会話が閉じていても、存在すれば記録する。
func (s *Store) MarkChatEngineStarted(ctx context.Context, sessionID int64) error {
	return s.chatWrite(ctx, func(tx *sql.Tx) error {
		err := requireOneRow(tx.ExecContext(ctx, `UPDATE chat_session SET engine_started = 1 WHERE id = ?`, sessionID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
}

// AppendChatSpeech は返事の行へ発言を 1 つ足す（すでに本文があれば、空行を挟んで続ける）。返事が進行中でなければ
// （止めた後に遅れて届いた分など）ErrValidation で断り、何も書かない。
func (s *Store) AppendChatSpeech(ctx context.Context, messageID int64, text string) error {
	return s.chatWrite(ctx, func(tx *sql.Tx) error {
		if err := requireReplying(ctx, tx, messageID); err != nil {
			return err
		}
		if strings.TrimSpace(text) == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE chat_message SET body = CASE WHEN body = '' THEN ? ELSE body || char(10) || char(10) || ? END WHERE id = ?`, text, text, messageID)
		return err
	})
}

// AppendChatCommand は返事の行へ、実行したコマンドを 1 つ足す。返事が進行中でなければ ErrValidation。
func (s *Store) AppendChatCommand(ctx context.Context, messageID int64, cmd ChatCommand) error {
	return s.chatWrite(ctx, func(tx *sql.Tx) error {
		if err := requireReplying(ctx, tx, messageID); err != nil {
			return err
		}
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT activity FROM chat_message WHERE id = ?`, messageID).Scan(&raw); err != nil {
			return err
		}
		var list []ChatCommand
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return fmt.Errorf("chat_message %d: activity is not a JSON array: %w", messageID, err)
		}
		b, err := json.Marshal(append(list, cmd))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE chat_message SET activity = ? WHERE id = ?`, string(b), messageID)
		return err
	})
}

// FinishChatReply は返事を終える（終わり方と費用を記録し、終わった時刻を入れる）。終わり方は replying 以外の 6 値
// （ほかは ErrValidation）。返事が進行中でなければ ErrValidation（二重に終えない）。費用は 0 以上（負は ErrValidation）。
// 終わり方が session_lost のときは、同じトランザクションで会話も閉じる。
func (s *Store) FinishChatReply(ctx context.Context, messageID int64, state ChatMessageState, costUSD float64) error {
	if !isChatEndState(state) || costUSD < 0 || math.IsNaN(costUSD) || math.IsInf(costUSD, 0) {
		return ErrValidation
	}
	return s.chatWrite(ctx, func(tx *sql.Tx) error {
		if err := requireReplying(ctx, tx, messageID); err != nil {
			return err
		}
		now := formatTimestamp(s.currentTime())
		var sessionID int64
		if err := tx.QueryRowContext(ctx, `SELECT session_id FROM chat_message WHERE id = ?`, messageID).Scan(&sessionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE chat_message SET state = ?, cost_usd = ?, ended_at = ? WHERE id = ?`,
			string(state), usdToMicros(costUSD), now, messageID); err != nil {
			return err
		}
		if state == ChatStateSessionLost {
			// 続けられなかった会話は、同じトランザクションで閉じる（次の発言は新しい会話になる）。
			_, err := tx.ExecContext(ctx, `UPDATE chat_session SET closed_at = ? WHERE id = ? AND closed_at IS NULL`, now, sessionID)
			return err
		}
		return nil
	})
}

// CloseChat は開いている会話を閉じる（次の発言は新しい会話になる）。開いている会話が無ければ何もしない。
// 返事が進行中の会話は閉じられない（ErrChatReplying）。
func (s *Store) CloseChat(ctx context.Context) error {
	return s.chatWrite(ctx, func(tx *sql.Tx) error {
		sess, err := openChatSession(ctx, tx)
		if err != nil || sess == nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_message WHERE session_id = ? AND state = ?`, sess.ID, string(ChatStateReplying)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrChatReplying
		}
		_, err = tx.ExecContext(ctx, `UPDATE chat_session SET closed_at = ? WHERE id = ?`, formatTimestamp(s.currentTime()), sess.ID)
		return err
	})
}

// StopInterruptedChatReplies は進行中のまま残った返事（サーバの停止などで終わりを記録できなかったもの）を
// すべて stopped にして、終わった時刻を入れる。止めた件数を返す。
func (s *Store) StopInterruptedChatReplies(ctx context.Context) (int, error) {
	var n int
	err := s.chatWrite(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE chat_message SET state = ?, ended_at = ? WHERE state = ?`,
			string(ChatStateStopped), formatTimestamp(s.currentTime()), string(ChatStateReplying))
		if err != nil {
			return err
		}
		c, err := res.RowsAffected()
		n = int(c)
		return err
	})
	return n, err
}

func (s *Store) chatWrite(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return classifyReadWriteErr(s.db.Write(ctx, fn))
}

// requireReplying は messageID が存在し、進行中の返事（orchestrator の replying）であることを確かめる。
func requireReplying(ctx context.Context, tx *sql.Tx, messageID int64) error {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM chat_message WHERE id = ? AND role = ?`, messageID, string(ChatRoleOrchestrator)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if ChatMessageState(state) != ChatStateReplying {
		return ErrValidation
	}
	return nil
}
