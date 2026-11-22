package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは画面の対話の欄の JSON の形を持つ（docs/features/core-redesign-s4d.md S4d-1「API」）。
// `GET …/chat` の応答と `POST …/chat/messages` の入出力が、ここの型を共有する。

// ChatState は欄の全体の状態。返事が進行中なら replying、そうでなければ idle。
const (
	ChatStateIdle     = "idle"
	ChatStateReplying = "replying"
)

// ChatViewing は発言を送ったときに欄が見せていたもの。見ていなかった項目は null。
type ChatViewing struct {
	ChallengeID *string `json:"challenge_id"`
	TaskID      *string `json:"task_id"`
	Title       *string `json:"title"`
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ChatActivity は返事の途中で実行したコマンド 1 つ。
type ChatActivity struct {
	Command string `json:"command"`
	Ok      bool   `json:"ok"`
}

// ChatMessage は会話の発言 1 つ。viewing は見ていたものが無ければ null。
type ChatMessage struct {
	Activity  []ChatActivity `json:"activity"`
	Body      string         `json:"body"`
	CostUSD   float64        `json:"cost_usd"`
	CreatedAt string         `json:"created_at"`
	ID        int64          `json:"id"`
	Role      string         `json:"role"`
	State     string         `json:"state"`
	Viewing   *ChatViewing   `json:"viewing"`
}

// ChatResponse は `GET …/chat` の成功の形。会話が無ければ session_id・started_at は null で、messages は空。
// cost_usd は会話の費用の合計。tools はエージェントの宣言の道具の範囲（restricted・full）。
type ChatResponse struct {
	CostUSD   float64       `json:"cost_usd"`
	Messages  []ChatMessage `json:"messages"`
	SessionID *string       `json:"session_id"`
	StartedAt *string       `json:"started_at"`
	State     string        `json:"state"`
	Tools     string        `json:"tools"`
}

// ChatPostViewing は `POST …/chat/messages` の入力の viewing（欄が見せていた課題とタスク。題名は core が読む）。
type ChatPostViewing struct {
	ChallengeID string `json:"challenge_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
}

// ChatPostRequest は `POST …/chat/messages` の入力。viewing は見ていたものが無ければ省く。
type ChatPostRequest struct {
	Text    string           `json:"text"`
	Viewing *ChatPostViewing `json:"viewing,omitempty"`
}

// ChatPostResponse は `POST …/chat/messages` の成功の形（受けたオーナーの発言の ID）。
type ChatPostResponse struct {
	ID int64 `json:"id"`
}

// FromChat は読んだ会話を ChatResponse へ変換する。tools は宣言の道具の範囲。
func FromChat(c *core.Chat, tools core.ChatTools) ChatResponse {
	out := ChatResponse{CostUSD: c.CostUSD, Messages: make([]ChatMessage, 0, len(c.Messages)), State: ChatStateIdle, Tools: string(tools)}
	if c.Replying {
		out.State = ChatStateReplying
	}
	if c.Session != nil {
		id, at := c.Session.EngineSession, FormatTimestamp(c.Session.StartedAt)
		out.SessionID, out.StartedAt = &id, &at
	}
	for _, m := range c.Messages {
		vm := ChatMessage{
			Activity: make([]ChatActivity, 0, len(m.Activity)), Body: m.Body, CostUSD: m.CostUSD,
			CreatedAt: FormatTimestamp(m.CreatedAt), ID: m.ID, Role: string(m.Role), State: string(m.State),
		}
		for _, a := range m.Activity {
			vm.Activity = append(vm.Activity, ChatActivity{Command: a.Command, Ok: a.OK})
		}
		if m.Viewing != nil {
			vm.Viewing = &ChatViewing{ChallengeID: nullable(m.Viewing.ChallengeID), TaskID: nullable(m.Viewing.TaskID), Title: nullable(m.Viewing.Title)}
		}
		out.Messages = append(out.Messages, vm)
	}
	return out
}
