package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは画面の対話の欄の JSON の形を持つ（docs/features/core-redesign-s4d.md S4d-1「API」・
// docs/features/chat-session.md「画面・API設計」）。対話の欄の口の入出力と、課題の詳細の紐づいた会話が、ここの型を共有する。

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

// ChatActivity は返事の道具の一覧の要素 1 つ。id は道具の使用の識別（過去の行から読み替えた要素では空）、
// state は running・ok・error。
type ChatActivity struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Target string `json:"target"`
	Tool   string `json:"tool"`
}

// ChatQuestionOption は問いの選択肢 1 つ。
type ChatQuestionOption struct {
	Description string `json:"description"`
	Label       string `json:"label"`
}

// ChatQuestion は問い 1 つ。
type ChatQuestion struct {
	MultiSelect bool                 `json:"multi_select"`
	Options     []ChatQuestionOption `json:"options"`
	Question    string               `json:"question"`
}

// ChatPromptAnswer は確認の答えの結果。許可の確認は decision（allow・deny）と message（添えた文。無ければ null）を
// 持ち answers は null、問いは answers（問いの文ごとの答えの文）を持ち decision と message は null。
type ChatPromptAnswer struct {
	Answers  map[string]string `json:"answers" ts:"nullable"`
	Decision *string           `json:"decision"`
	Message  *string           `json:"message"`
}

// ChatPrompt は返事の中の確認（許可の確認と問い）1 つ。target は許可の確認だけが持つ対象の全文（切らない。問いでは
// null）。questions は問いだけが持つ（許可の確認では []）。answer は答えていなければ null。
type ChatPrompt struct {
	Answer     *ChatPromptAnswer `json:"answer"`
	AnsweredAt *string           `json:"answered_at"`
	CreatedAt  string            `json:"created_at"`
	ID         int64             `json:"id"`
	Kind       string            `json:"kind"`
	Questions  []ChatQuestion    `json:"questions"`
	State      string            `json:"state"`
	Target     *string           `json:"target"`
	Tool       string            `json:"tool"`
}

// ChatPromptAnswerRequest は `POST …/chat/prompts/{id}/answer` の入力。許可の確認は decision と、断るときの任意の
// message。問いは answers。
type ChatPromptAnswerRequest struct {
	Answers  map[string]string `json:"answers,omitempty"`
	Decision string            `json:"decision,omitempty"`
	Message  string            `json:"message,omitempty"`
}

// ChatSessionChallenge は会話に紐づいた課題。
type ChatSessionChallenge struct {
	ChallengeID string `json:"challenge_id"`
	Title       string `json:"title"`
}

// ChatSessionSummary は `GET …/chat/sessions` の要素。id は会話の識別、closed_at は開いている会話では null。
type ChatSessionSummary struct {
	Challenges          []ChatSessionChallenge `json:"challenges"`
	ClosedAt            *string                `json:"closed_at"`
	CostUSD             float64                `json:"cost_usd"`
	FirstMessage        string                 `json:"first_message"`
	ID                  string                 `json:"id"`
	Open                bool                   `json:"open"`
	StartedAt           string                 `json:"started_at"`
	TranscriptAvailable bool                   `json:"transcript_available"`
}

// ChatSessionsResponse は `GET …/chat/sessions` の本文（新しい順。無ければ []）。
type ChatSessionsResponse struct {
	Sessions []ChatSessionSummary `json:"sessions"`
}

// ChallengeChatSession は課題の詳細の、紐づいた会話 1 つ。cost_usd はこの課題に数えた額。
type ChallengeChatSession struct {
	CostUSD      float64 `json:"cost_usd"`
	FirstMessage string  `json:"first_message"`
	ID           string  `json:"id"`
	StartedAt    string  `json:"started_at"`
}

// ChatTranscriptEntry は全文の項目 1 つ。kind は owner・assistant・tool_use・tool_result。tool は道具の使用と結果
// だけが持つ（ほかは null）。at は時刻が分からなければ null。
type ChatTranscriptEntry struct {
	At        *string `json:"at"`
	Kind      string  `json:"kind"`
	Text      string  `json:"text"`
	Tool      *string `json:"tool"`
	Truncated bool    `json:"truncated"`
}

// ChatTranscriptResponse は `GET …/chat/sessions/{id}/transcript` の本文。
type ChatTranscriptResponse struct {
	Entries []ChatTranscriptEntry `json:"entries"`
}

// ChatMessage は会話の発言 1 つ。viewing は見ていたものが無ければ null。
type ChatMessage struct {
	Activity  []ChatActivity `json:"activity"`
	Body      string         `json:"body"`
	CostUSD   float64        `json:"cost_usd"`
	CreatedAt string         `json:"created_at"`
	ID        int64          `json:"id"`
	Prompts   []ChatPrompt   `json:"prompts"`
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
			CreatedAt: FormatTimestamp(m.CreatedAt), ID: m.ID, Prompts: fromChatPrompts(m.Prompts), Role: string(m.Role), State: string(m.State),
		}
		for _, a := range m.Activity {
			vm.Activity = append(vm.Activity, ChatActivity{ID: a.ID, State: string(a.State), Target: a.Target, Tool: a.Tool})
		}
		if m.Viewing != nil {
			vm.Viewing = &ChatViewing{ChallengeID: nullable(m.Viewing.ChallengeID), TaskID: nullable(m.Viewing.TaskID), Title: nullable(m.Viewing.Title)}
		}
		out.Messages = append(out.Messages, vm)
	}
	return out
}

// ToCore は入力を core の答えへ変換する（形の検査は core が行う）。
func (r ChatPromptAnswerRequest) ToCore() core.ChatPromptAnswer {
	return core.ChatPromptAnswer{Answers: r.Answers, Decision: core.ChatDecision(r.Decision), Message: r.Message}
}

func fromChatPrompts(ps []core.ChatPrompt) []ChatPrompt {
	out := make([]ChatPrompt, 0, len(ps))
	for _, p := range ps {
		v := ChatPrompt{
			CreatedAt: FormatTimestamp(p.CreatedAt), ID: p.ID, Kind: string(p.Kind),
			Questions: make([]ChatQuestion, 0, len(p.Questions)), State: string(p.State), Tool: p.Tool,
		}
		if p.Kind == core.ChatPromptPermission {
			target := p.Target
			v.Target = &target
		}
		for _, q := range p.Questions {
			options := make([]ChatQuestionOption, 0, len(q.Options))
			for _, o := range q.Options {
				options = append(options, ChatQuestionOption{Description: o.Description, Label: o.Label})
			}
			v.Questions = append(v.Questions, ChatQuestion{MultiSelect: q.MultiSelect, Options: options, Question: q.Question})
		}
		if p.Answer != nil {
			v.Answer = &ChatPromptAnswer{Answers: p.Answer.Answers, Decision: nullable(string(p.Answer.Decision)), Message: nullable(p.Answer.Message)}
		}
		if p.AnsweredAt != nil {
			at := FormatTimestamp(*p.AnsweredAt)
			v.AnsweredAt = &at
		}
		out = append(out, v)
	}
	return out
}

// FromChatSessions は会話の一覧を ChatSessionsResponse へ変換する。
func FromChatSessions(sessions []core.ChatSessionSummary) ChatSessionsResponse {
	out := ChatSessionsResponse{Sessions: make([]ChatSessionSummary, 0, len(sessions))}
	for _, s := range sessions {
		v := ChatSessionSummary{
			Challenges: make([]ChatSessionChallenge, 0, len(s.Challenges)), CostUSD: s.CostUSD, FirstMessage: s.FirstMessage,
			ID: s.EngineSession, Open: s.Open, StartedAt: FormatTimestamp(s.StartedAt), TranscriptAvailable: s.TranscriptAvailable,
		}
		if s.ClosedAt != nil {
			at := FormatTimestamp(*s.ClosedAt)
			v.ClosedAt = &at
		}
		for _, c := range s.Challenges {
			v.Challenges = append(v.Challenges, ChatSessionChallenge{ChallengeID: c.ChallengeID, Title: c.Title})
		}
		out.Sessions = append(out.Sessions, v)
	}
	return out
}

func fromChallengeChatSessions(sessions []core.ChallengeChatSession) []ChallengeChatSession {
	out := make([]ChallengeChatSession, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, ChallengeChatSession{CostUSD: s.CostUSD, FirstMessage: s.FirstMessage, ID: s.EngineSession, StartedAt: FormatTimestamp(s.StartedAt)})
	}
	return out
}

// FromChatTranscript は全文の項目の並びを ChatTranscriptResponse へ変換する。
func FromChatTranscript(entries []core.ChatTranscriptEntry) ChatTranscriptResponse {
	out := ChatTranscriptResponse{Entries: make([]ChatTranscriptEntry, 0, len(entries))}
	for _, e := range entries {
		v := ChatTranscriptEntry{Kind: string(e.Kind), Text: e.Text, Tool: nullable(e.Tool), Truncated: e.Truncated}
		if !e.At.IsZero() {
			at := FormatTimestamp(e.At)
			v.At = &at
		}
		out.Entries = append(out.Entries, v)
	}
	return out
}
