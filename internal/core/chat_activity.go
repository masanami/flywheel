package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// このファイルは返事の行の道具の一覧（chat_message.activity）の core の口を持つ
// （docs/features/chat-session.md「返事・道具の使用・途中経過を流して出す」）。一覧に残すのは道具の名前と対象の 1 行と
// 成否だけで、道具の入力の全文と結果の中身はストアへ写さない。

// ChatToolTargetLimit は道具の一覧の対象の 1 行の長さの上限（文字数）。超える分は切って記録する。
const ChatToolTargetLimit = 200

// ChatActivityState は道具の一覧の要素の状態。
type ChatActivityState string

// ChatActivityState の 3 値。
const (
	ChatActivityRunning ChatActivityState = "running"
	ChatActivityOK      ChatActivityState = "ok"
	ChatActivityError   ChatActivityState = "error"
)

// ChatActivityStates は道具の一覧の要素の状態の閉集合を返す。
func ChatActivityStates() []ChatActivityState {
	return []ChatActivityState{ChatActivityRunning, ChatActivityOK, ChatActivityError}
}

// ChatToolUse は道具の使い始め。ID は実行エンジンが付けた道具の使用の識別で、結果はこれで突き合わせる。
// Target は対象の 1 行（無い道具では空）。
type ChatToolUse struct {
	ID     string
	Tool   string
	Target string
}

// ChatActivity は返事の道具の一覧の要素 1 つ。
type ChatActivity struct {
	// ID は道具の使用の識別。過去の形の要素（command・ok）では空。
	ID     string            `json:"id"`
	Tool   string            `json:"tool"`
	Target string            `json:"target"`
	State  ChatActivityState `json:"state"`
}

// chatLegacyTool は過去の形の要素（command・ok）を読み替えるときの道具の名前。
const chatLegacyTool = "Bash"

// parseChatActivity は chat_message.activity を読む。過去の形の要素（command・ok）は、識別が空の Bash の要素に読み替える。
func parseChatActivity(messageID int64, raw string) ([]ChatActivity, error) {
	var stored []struct {
		ChatActivity
		Command *string `json:"command"`
		OK      bool    `json:"ok"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, fmt.Errorf("chat_message %d: activity is not a JSON array: %w", messageID, err)
	}
	out := make([]ChatActivity, 0, len(stored))
	for _, e := range stored {
		a := e.ChatActivity
		if e.Command != nil {
			a = ChatActivity{Tool: chatLegacyTool, Target: *e.Command, State: chatActivityResult(e.OK)}
		}
		out = append(out, a)
	}
	return out, nil
}

func chatActivityResult(ok bool) ChatActivityState {
	if ok {
		return ChatActivityOK
	}
	return ChatActivityError
}

// updateChatActivity は返事の行の道具の一覧を読み、change が返した一覧に書き換える。
func updateChatActivity(ctx context.Context, tx *sql.Tx, messageID int64, change func([]ChatActivity) ([]ChatActivity, error)) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT activity FROM chat_message WHERE id = ?`, messageID).Scan(&raw); err != nil {
		return err
	}
	list, err := parseChatActivity(messageID, raw)
	if err != nil {
		return err
	}
	if list, err = change(list); err != nil {
		return err
	}
	b, err := marshalChatJSON(list)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE chat_message SET activity = ? WHERE id = ?`, b, messageID)
	return err
}

func cutRunes(s string, limit int) string {
	n := 0
	for i := range s {
		if n == limit {
			return s[:i]
		}
		n++
	}
	return s
}

// StartChatTool は返事の行の道具の一覧へ、使い始めた道具を running で足す。対象の 1 行は ChatToolTargetLimit 文字で
// 切る。識別か名前が空、同じ識別の要素がすでにある、返事が進行中でない場合は ErrValidation で、何も書かない。
func (s *Store) StartChatTool(ctx context.Context, messageID int64, use ChatToolUse) error {
	if use.ID == "" || use.Tool == "" {
		return ErrValidation
	}
	return s.chatWrite(ctx, func(tx *sql.Tx) error {
		if err := requireReplying(ctx, tx, messageID); err != nil {
			return err
		}
		return updateChatActivity(ctx, tx, messageID, func(list []ChatActivity) ([]ChatActivity, error) {
			for _, a := range list {
				if a.ID == use.ID {
					return nil, fmt.Errorf("%w: the tool use %q is already listed", ErrValidation, use.ID)
				}
			}
			return append(list, ChatActivity{ID: use.ID, Tool: use.Tool, Target: cutRunes(use.Target, ChatToolTargetLimit), State: ChatActivityRunning}), nil
		})
	})
}

// FinishChatTool は道具の結果で、同じ識別の要素だけを成功（ok）か失敗（error）に変える。識別が空・要素が running で
// ない・返事が進行中でない場合は ErrValidation、同じ識別の要素が無ければ ErrNotFound で、何も書かない。
func (s *Store) FinishChatTool(ctx context.Context, messageID int64, toolUseID string, ok bool) error {
	if toolUseID == "" {
		return ErrValidation
	}
	return s.chatWrite(ctx, func(tx *sql.Tx) error {
		if err := requireReplying(ctx, tx, messageID); err != nil {
			return err
		}
		return updateChatActivity(ctx, tx, messageID, func(list []ChatActivity) ([]ChatActivity, error) {
			for i := range list {
				if list[i].ID != toolUseID {
					continue
				}
				if list[i].State != ChatActivityRunning {
					return nil, fmt.Errorf("%w: the tool use %q already has a result", ErrValidation, toolUseID)
				}
				list[i].State = chatActivityResult(ok)
				return list, nil
			}
			return nil, ErrNotFound
		})
	})
}

// closeRunningChatTools は返事の道具の一覧で running のまま残った要素を error に締める。
func closeRunningChatTools(ctx context.Context, tx *sql.Tx, messageID int64) error {
	return updateChatActivity(ctx, tx, messageID, func(list []ChatActivity) ([]ChatActivity, error) {
		for i := range list {
			if list[i].State == ChatActivityRunning {
				list[i].State = ChatActivityError
			}
		}
		return list, nil
	})
}
