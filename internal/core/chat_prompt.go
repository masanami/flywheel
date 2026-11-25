package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// このファイルは許可の確認と選択肢の問い（表 chat_prompt。以下「確認」）の core の口を持つ
// （docs/features/chat-session.md「許可の確認と選択肢の問いを、画面のボタンで受ける」）。確認は進行中の返事の行に
// 結び付けてストアに記録し、答えは答えを待っている（pending の）確認へ 1 回だけ受ける。
// 許可への答えはその 1 回だけの許可で、以後も許す形は持たない。

// ErrChatPromptNotPending は、答えを待っていない確認（答えた後・答えられなかった）への答えを断ることを表す。
var ErrChatPromptNotPending = errors.New("core: the chat prompt is not waiting for an answer")

// ChatPromptKind は確認の種類。
type ChatPromptKind string

// ChatPromptKind の 2 値。
const (
	ChatPromptPermission ChatPromptKind = "permission"
	ChatPromptQuestion   ChatPromptKind = "question"
)

// ChatPromptKinds は確認の種類の閉集合を返す。
func ChatPromptKinds() []ChatPromptKind {
	return []ChatPromptKind{ChatPromptPermission, ChatPromptQuestion}
}

// ChatPromptState は確認の状態。pending から始まり、ほかの 4 値のいずれかで終わる。expired は「答えられなかった」。
type ChatPromptState string

// ChatPromptState の 5 値。
const (
	ChatPromptPending  ChatPromptState = "pending"
	ChatPromptAllowed  ChatPromptState = "allowed"
	ChatPromptDenied   ChatPromptState = "denied"
	ChatPromptAnswered ChatPromptState = "answered"
	ChatPromptExpired  ChatPromptState = "expired"
)

// ChatPromptStates は確認の状態の閉集合を返す。
func ChatPromptStates() []ChatPromptState {
	return []ChatPromptState{ChatPromptPending, ChatPromptAllowed, ChatPromptDenied, ChatPromptAnswered, ChatPromptExpired}
}

// ChatDecision は許可の確認への答え。ChatDecisionAllow はその 1 回だけを許す。
type ChatDecision string

// ChatDecision の 2 値。
const (
	ChatDecisionAllow ChatDecision = "allow"
	ChatDecisionDeny  ChatDecision = "deny"
)

// ChatDecisions は許可の確認への答えの閉集合を返す。
func ChatDecisions() []ChatDecision { return []ChatDecision{ChatDecisionAllow, ChatDecisionDeny} }

// ChatQuestionOption は問いの選択肢 1 つ（見出しと説明）。
type ChatQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ChatQuestion は問い 1 つ。
type ChatQuestion struct {
	Question    string               `json:"question"`
	Options     []ChatQuestionOption `json:"options"`
	MultiSelect bool                 `json:"multi_select"`
}

// ChatPromptRequest は実行エンジンが求めた確認 1 件。
type ChatPromptRequest struct {
	// RequestID は実行エンジンが付けた確認の識別。答えを返すときに使う。
	RequestID string
	Kind      ChatPromptKind
	Tool      string
	// Target は対象の全文（切らない）。問いでは空でよい。
	Target string
	// Questions は問いの中身（問いだけ。許可の確認では空）。
	Questions []ChatQuestion
}

// ChatPromptAnswer は確認への答え。許可の確認は Decision（と、断るときの任意の Message）、問いは Answers
// （問いの文ごとの答えの文。複数を選んだ問いは、選んだ見出しを「, 」でつないだ 1 つの文）だけを持つ。
type ChatPromptAnswer struct {
	Decision ChatDecision
	Message  string
	Answers  map[string]string
}

// ChatPrompt は記録した確認。
type ChatPrompt struct {
	ID        int64
	MessageID int64
	RequestID string
	Kind      ChatPromptKind
	Tool      string
	Target    string
	Questions []ChatQuestion
	State     ChatPromptState
	// Answer は答え。答えていなければ（pending・expired）nil。
	Answer     *ChatPromptAnswer
	CreatedAt  time.Time
	AnsweredAt *time.Time
}

// storedPermissionAnswer・storedQuestionAnswer は chat_prompt.answer の形。
type storedPermissionAnswer struct {
	Decision ChatDecision `json:"decision"`
	Message  *string      `json:"message"`
}

type storedQuestionAnswer struct {
	Answers map[string]string `json:"answers"`
}

// marshalChatJSON は対話の欄の JSON の列へ書く文字列を作る（<・>・& を置き換えない）。
func marshalChatJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func (r ChatPromptRequest) valid() bool {
	if r.RequestID == "" || r.Tool == "" {
		return false
	}
	switch r.Kind {
	case ChatPromptPermission:
		return len(r.Questions) == 0
	case ChatPromptQuestion:
		return len(r.Questions) > 0
	}
	return false
}

// RecordChatPrompt は進行中の返事の行に結び付けて確認を pending で記録し、その ID を返す。識別か道具の名前が空・
// 種類が閉集合に無い・問いに中身が無い・許可の確認に問いの中身がある・返事が進行中でない場合は ErrValidation。
func (s *Store) RecordChatPrompt(ctx context.Context, messageID int64, req ChatPromptRequest) (int64, error) {
	if !req.valid() {
		return 0, ErrValidation
	}
	var questions any
	if req.Kind == ChatPromptQuestion {
		b, err := marshalChatJSON(req.Questions)
		if err != nil {
			return 0, err
		}
		questions = b
	}
	var id int64
	err := s.chatWrite(ctx, func(tx *sql.Tx) error {
		if err := requireReplying(ctx, tx, messageID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO chat_prompt (message_id, request_id, kind, tool, target, questions, state, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, messageID, req.RequestID, string(req.Kind), req.Tool, req.Target, questions,
			string(ChatPromptPending), formatTimestamp(s.currentTime()))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

const chatPromptSelectColumns = `p.id, p.message_id, p.request_id, p.kind, p.tool, p.target, p.questions, p.state, p.answer, p.created_at, p.answered_at`

func scanChatPrompt(sc rowScanner) (ChatPrompt, error) {
	var (
		p                             ChatPrompt
		kind, state, created          string
		questions, answer, answeredAt sql.NullString
	)
	if err := sc.Scan(&p.ID, &p.MessageID, &p.RequestID, &kind, &p.Tool, &p.Target, &questions, &state, &answer, &created, &answeredAt); err != nil {
		return ChatPrompt{}, err
	}
	p.Kind, p.State = ChatPromptKind(kind), ChatPromptState(state)
	p.Questions = []ChatQuestion{}
	if questions.Valid {
		if err := json.Unmarshal([]byte(questions.String), &p.Questions); err != nil {
			return ChatPrompt{}, fmt.Errorf("chat_prompt %d: questions is not a JSON array: %w", p.ID, err)
		}
	}
	if answer.Valid {
		var a struct {
			storedPermissionAnswer
			storedQuestionAnswer
		}
		if err := json.Unmarshal([]byte(answer.String), &a); err != nil {
			return ChatPrompt{}, fmt.Errorf("chat_prompt %d: answer is not a JSON object: %w", p.ID, err)
		}
		p.Answer = &ChatPromptAnswer{Decision: a.Decision, Answers: a.Answers}
		if a.Message != nil {
			p.Answer.Message = *a.Message
		}
	}
	var err error
	if p.CreatedAt, err = parseTimestamp(created); err != nil {
		return ChatPrompt{}, err
	}
	if answeredAt.Valid {
		t, err := parseTimestamp(answeredAt.String)
		if err != nil {
			return ChatPrompt{}, err
		}
		p.AnsweredAt = &t
	}
	return p, nil
}

// sessionChatPrompts は会話の確認を、返事の行の ID ごとに記録した順で返す。
func sessionChatPrompts(ctx context.Context, tx *sql.Tx, sessionID int64) (map[int64][]ChatPrompt, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+chatPromptSelectColumns+` FROM chat_prompt p JOIN chat_message m ON m.id = p.message_id
		WHERE m.session_id = ? ORDER BY p.id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]ChatPrompt{}
	for rows.Next() {
		p, err := scanChatPrompt(rows)
		if err != nil {
			return nil, err
		}
		out[p.MessageID] = append(out[p.MessageID], p)
	}
	return out, rows.Err()
}

// pendingChatPrompt は answer を受けられる確認を返す。無ければ ErrNotFound、答えを待っていなければ
// ErrChatPromptNotPending、答えの形が種類に合わないか空なら ErrValidation。
func pendingChatPrompt(ctx context.Context, tx *sql.Tx, promptID int64, answer ChatPromptAnswer) (*ChatPrompt, error) {
	p, err := scanChatPrompt(tx.QueryRowContext(ctx, `SELECT `+chatPromptSelectColumns+` FROM chat_prompt p WHERE p.id = ?`, promptID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.State != ChatPromptPending {
		return nil, ErrChatPromptNotPending
	}
	if !answer.fits(p) {
		return nil, fmt.Errorf("%w: the answer does not fit the %s prompt", ErrValidation, p.Kind)
	}
	return &p, nil
}

// fits は答えが確認の種類に合うかを返す。問いは、すべての問いの文に空でない答えがあり、ほかのキーが無いことを求める。
func (a ChatPromptAnswer) fits(p ChatPrompt) bool {
	if p.Kind == ChatPromptPermission {
		if a.Answers != nil {
			return false
		}
		return a.Decision == ChatDecisionDeny || (a.Decision == ChatDecisionAllow && a.Message == "")
	}
	if a.Decision != "" || a.Message != "" {
		return false
	}
	asked := map[string]bool{}
	for _, q := range p.Questions {
		asked[q.Question] = true
		if strings.TrimSpace(a.Answers[q.Question]) == "" {
			return false
		}
	}
	return len(a.Answers) == len(asked)
}

// CheckChatPromptAnswer は、確認が答えを待っていて答えの形が合うことを確かめ、その確認を返す（何も書かない）。
// 呼び出し元が、答えを実行エンジンへ送る前に使う。誤りは AnswerChatPrompt と同じ。
func (s *Store) CheckChatPromptAnswer(ctx context.Context, promptID int64, answer ChatPromptAnswer) (*ChatPrompt, error) {
	var out *ChatPrompt
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = pendingChatPrompt(ctx, tx, promptID, answer)
		return err
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return out, nil
}

// AnswerChatPrompt は答えを記録し、記録した確認を返す。答えを待っていることは同じ書き込みの中で確かめるので、
// 1 つの確認が受ける答えは 1 回だけ。無い確認は ErrNotFound、答えを待っていない確認は ErrChatPromptNotPending、
// 答えの形の誤りは ErrValidation で、いずれも何も書かない。
//
// 返事を終えると、答えを待っていた確認は expired になる。答えを実行エンジンへ送ってから記録する呼び出し元は、
// 送ってから記録するまでの間に、その返事を終えないようにする。
func (s *Store) AnswerChatPrompt(ctx context.Context, promptID int64, answer ChatPromptAnswer) (*ChatPrompt, error) {
	var out *ChatPrompt
	err := s.chatWrite(ctx, func(tx *sql.Tx) error {
		p, err := pendingChatPrompt(ctx, tx, promptID, answer)
		if err != nil {
			return err
		}
		var (
			stored any
			state  = ChatPromptAnswered
		)
		if p.Kind == ChatPromptPermission {
			state = ChatPromptAllowed
			if answer.Decision == ChatDecisionDeny {
				state = ChatPromptDenied
			}
			pa := storedPermissionAnswer{Decision: answer.Decision}
			if answer.Message != "" {
				pa.Message = &answer.Message
			}
			stored = pa
		} else {
			stored = storedQuestionAnswer{Answers: answer.Answers}
		}
		raw, err := marshalChatJSON(stored)
		if err != nil {
			return err
		}
		answeredAt, err := s.nextChatAnsweredAt(ctx, tx, p.MessageID)
		if err != nil {
			return err
		}
		if err := requireOneRow(tx.ExecContext(ctx, `UPDATE chat_prompt SET state = ?, answer = ?, answered_at = ? WHERE id = ? AND state = ?`,
			string(state), raw, answeredAt, promptID, string(ChatPromptPending))); err != nil {
			return err
		}
		recorded, err := scanChatPrompt(tx.QueryRowContext(ctx, `SELECT `+chatPromptSelectColumns+` FROM chat_prompt p WHERE p.id = ?`, promptID))
		if err != nil {
			return err
		}
		out = &recorded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// nextChatAnsweredAt は返事 messageID の確認の答えに記録する時刻を返す。記録の「発言」は答えた順に問いを
// 並べるので、同じ返事のこれまでの答えより後の時刻にする（時刻の刻みの中で続けて答えても順が崩れない）。
func (s *Store) nextChatAnsweredAt(ctx context.Context, tx *sql.Tx, messageID int64) (string, error) {
	var latest sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT MAX(answered_at) FROM chat_prompt WHERE message_id = ?`, messageID).Scan(&latest); err != nil {
		return "", err
	}
	at := formatTimestamp(s.currentTime())
	if !latest.Valid || at > latest.String {
		return at, nil
	}
	prev, err := parseTimestamp(latest.String)
	if err != nil {
		return "", err
	}
	return formatTimestamp(prev.Add(time.Millisecond)), nil
}

// ExpireChatReplyPrompts は返事 1 つの、答えを待っている確認を「答えられなかった」（expired）にし、件数を返す。
func (s *Store) ExpireChatReplyPrompts(ctx context.Context, messageID int64) (int, error) {
	var n int
	err := s.chatWrite(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = expireChatReplyPrompts(ctx, tx, messageID)
		return err
	})
	return n, err
}

// ExpireAllChatPrompts は答えを待っているすべての確認を「答えられなかった」（expired）にし、件数を返す。
func (s *Store) ExpireAllChatPrompts(ctx context.Context) (int, error) {
	var n int
	err := s.chatWrite(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = expireAllChatPrompts(ctx, tx)
		return err
	})
	return n, err
}

func expireChatReplyPrompts(ctx context.Context, tx *sql.Tx, messageID int64) (int, error) {
	return affectedRows(tx.ExecContext(ctx, `UPDATE chat_prompt SET state = ? WHERE message_id = ? AND state = ?`,
		string(ChatPromptExpired), messageID, string(ChatPromptPending)))
}

func expireAllChatPrompts(ctx context.Context, tx *sql.Tx) (int, error) {
	return affectedRows(tx.ExecContext(ctx, `UPDATE chat_prompt SET state = ? WHERE state = ?`,
		string(ChatPromptExpired), string(ChatPromptPending)))
}

func affectedRows(res sql.Result, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
