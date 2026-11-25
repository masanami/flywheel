package view_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

func chatJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 会話が無いとき: session_id・started_at は null、messages は空の配列、state は idle。
func TestFromChat_NoSession(t *testing.T) {
	got := chatJSON(t, view.FromChat(&core.Chat{Messages: []core.ChatMessage{}}))
	want := `{"cost_usd":0,"messages":[],"session_id":null,"started_at":null,"state":"idle"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// Messages が nil でも `[]` で出る。
	if got := chatJSON(t, view.FromChat(&core.Chat{})); !strings.Contains(got, `"messages":[]`) {
		t.Errorf("nil messages: %s", got)
	}
}

func TestFromChat_MessagesViewingActivityAndState(t *testing.T) {
	at := time.Date(2026, 11, 22, 1, 2, 3, 0, time.UTC)
	chat := &core.Chat{
		Session:  &core.ChatSession{ID: 1, EngineSession: "uuid-1", StartedAt: at},
		CostUSD:  1.5,
		Replying: true,
		Messages: []core.ChatMessage{
			{ID: 1, Role: core.ChatRoleOwner, Body: "q", State: core.ChatStateDone, CreatedAt: at, Viewing: &core.ChatViewing{ChallengeID: "C-1", Title: "題"}},
			{ID: 2, Role: core.ChatRoleOrchestrator, Body: "a", State: core.ChatStateReplying, CostUSD: 1.5, CreatedAt: at, Activity: []core.ChatActivity{{ID: "toolu_1", Tool: "Bash", Target: "flywheel status", State: core.ChatActivityError}}},
		},
	}
	got := chatJSON(t, view.FromChat(chat))
	want := `{"cost_usd":1.5,"messages":[` +
		`{"activity":[],"body":"q","cost_usd":0,"created_at":"2026-11-22T01:02:03.000Z","id":1,"prompts":[],"role":"owner","state":"done","viewing":{"challenge_id":"C-1","task_id":null,"title":"題"}},` +
		`{"activity":[{"id":"toolu_1","state":"error","target":"flywheel status","tool":"Bash"}],"body":"a","cost_usd":1.5,"created_at":"2026-11-22T01:02:03.000Z","id":2,"prompts":[],"role":"orchestrator","state":"replying","viewing":null}` +
		`],"session_id":"uuid-1","started_at":"2026-11-22T01:02:03.000Z","state":"replying"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// POST の入力は viewing を省ける。出力は発言の id だけ。
func TestChatPostShapes(t *testing.T) {
	var req view.ChatPostRequest
	if err := json.Unmarshal([]byte(`{"text":"こんにちは"}`), &req); err != nil || req.Viewing != nil || req.Text != "こんにちは" {
		t.Fatalf("request without viewing: %+v, %v", req, err)
	}
	if got := chatJSON(t, req); got != `{"text":"こんにちは"}` {
		t.Errorf("request round trip = %s (viewing must be omitted)", got)
	}
	if got := chatJSON(t, view.ChatPostRequest{Text: "x", Viewing: &view.ChatPostViewing{ChallengeID: "C-2"}}); got != `{"text":"x","viewing":{"challenge_id":"C-2"}}` {
		t.Errorf("request with viewing = %s", got)
	}
	if got := chatJSON(t, view.ChatPostResponse{ID: 7}); got != `{"id":7}` {
		t.Errorf("response = %s", got)
	}
}

func TestClosedSets_ChatCodes(t *testing.T) {
	want := map[string][]string{
		"CHAT_MESSAGE_STATE_CODES": {"replying", "done", "stopped", "limit", "rate_limited", "session_lost", "failed"},
		"CHAT_ROLE_CODES":          {"owner", "orchestrator"},
		"CHAT_PANEL_STATE_CODES":   {"idle", "replying"},
	}
	got := map[string][]string{}
	for _, cs := range view.ClosedSets() {
		got[cs.Name] = cs.Codes
	}
	for name, codes := range want {
		if !reflect.DeepEqual(got[name], codes) {
			t.Errorf("%s = %v, want %v", name, got[name], codes)
		}
	}
}

// 確認の JSON の形: 許可の確認は target の全文（切らない）と空の questions、問いは target が null。
func TestFromChat_Prompts(t *testing.T) {
	at := time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC)
	long := strings.Repeat("c", 5000)
	chat := &core.Chat{Messages: []core.ChatMessage{{ID: 2, Role: core.ChatRoleOrchestrator, State: core.ChatStateReplying, CreatedAt: at, Prompts: []core.ChatPrompt{
		{ID: 1, Kind: core.ChatPromptPermission, Tool: "Bash", Target: long, State: core.ChatPromptPending, CreatedAt: at, RequestID: "r1"},
		{ID: 2, Kind: core.ChatPromptPermission, Tool: "Write", Target: "{}", State: core.ChatPromptDenied, CreatedAt: at, AnsweredAt: &at,
			Answer: &core.ChatPromptAnswer{Decision: core.ChatDecisionDeny, Message: "だめ"}},
		{ID: 3, Kind: core.ChatPromptQuestion, Tool: "AskUserQuestion", State: core.ChatPromptAnswered, CreatedAt: at, AnsweredAt: &at,
			Questions: []core.ChatQuestion{{Question: "色は？", Options: []core.ChatQuestionOption{{Label: "赤", Description: "red"}}, MultiSelect: true}},
			Answer:    &core.ChatPromptAnswer{Answers: map[string]string{"色は？": "赤"}}},
	}}}}
	got := chatJSON(t, view.FromChat(chat).Messages[0].Prompts)
	const ts = `"2026-11-25T01:02:03.000Z"`
	want := `[{"answer":null,"answered_at":null,"created_at":` + ts + `,"id":1,"kind":"permission","questions":[],"state":"pending","target":"` + long + `","tool":"Bash"},` +
		`{"answer":{"answers":null,"decision":"deny","message":"だめ"},"answered_at":` + ts + `,"created_at":` + ts + `,"id":2,"kind":"permission","questions":[],"state":"denied","target":"{}","tool":"Write"},` +
		`{"answer":{"answers":{"色は？":"赤"},"decision":null,"message":null},"answered_at":` + ts + `,"created_at":` + ts + `,"id":3,"kind":"question",` +
		`"questions":[{"multi_select":true,"options":[{"description":"red","label":"赤"}],"question":"色は？"}],"state":"answered","target":null,"tool":"AskUserQuestion"}]`
	if got != want {
		t.Errorf("prompts =\n%s\nwant\n%s", got, want)
	}
}

func TestChatSessionsAndTranscriptShapes(t *testing.T) {
	at := time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC)
	const ts = `"2026-11-25T01:02:03.000Z"`
	if got := chatJSON(t, view.FromChatSessions(nil)); got != `{"sessions":[]}` {
		t.Errorf("empty sessions = %s", got)
	}
	got := chatJSON(t, view.FromChatSessions([]core.ChatSessionSummary{
		{EngineSession: "u1", StartedAt: at, FirstMessage: "m", CostUSD: 0.5, Open: true},
		{EngineSession: "u2", StartedAt: at, ClosedAt: &at, TranscriptAvailable: true, Challenges: []core.ChatSessionChallenge{{ChallengeID: "C-1", Title: "題"}}},
	}))
	want := `{"sessions":[{"challenges":[],"closed_at":null,"cost_usd":0.5,"first_message":"m","id":"u1","open":true,"started_at":` + ts + `,"transcript_available":false},` +
		`{"challenges":[{"challenge_id":"C-1","title":"題"}],"closed_at":` + ts + `,"cost_usd":0,"first_message":"","id":"u2","open":false,"started_at":` + ts + `,"transcript_available":true}]}`
	if got != want {
		t.Errorf("sessions =\n%s\nwant\n%s", got, want)
	}
	got = chatJSON(t, view.FromChatTranscript([]core.ChatTranscriptEntry{
		{Kind: core.ChatTranscriptOwner, Text: "q", At: at},
		{Kind: core.ChatTranscriptToolResult, Text: "r", Tool: "Bash", Truncated: true},
	}))
	want = `{"entries":[{"at":` + ts + `,"kind":"owner","text":"q","tool":null,"truncated":false},{"at":null,"kind":"tool_result","text":"r","tool":"Bash","truncated":true}]}`
	if got != want {
		t.Errorf("transcript =\n%s\nwant\n%s", got, want)
	}
}

func TestChatPromptAnswerRequestToCore(t *testing.T) {
	got := view.ChatPromptAnswerRequest{Decision: "deny", Message: "m"}.ToCore()
	if got.Decision != core.ChatDecisionDeny || got.Message != "m" || got.Answers != nil {
		t.Errorf("ToCore = %+v", got)
	}
}

// 課題の詳細の challenge は、仕様づくりの費用と紐づいた会話を持つ（無ければ 0 と []）。
func TestShowChallenge_SpecCostAndChatSessions(t *testing.T) {
	if got := chatJSON(t, view.FromShowChallenge(&core.ChallengeDetail{})); !strings.Contains(got, `"chat_sessions":[]`) || !strings.Contains(got, `"spec_cost_usd":0`) {
		t.Errorf("empty detail = %s", got)
	}
	at := time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC)
	got := chatJSON(t, view.FromShowChallenge(&core.ChallengeDetail{SpecCostUSD: 0.42,
		ChatSessions: []core.ChallengeChatSession{{EngineSession: "u1", StartedAt: at, FirstMessage: "m", CostUSD: 0.42}}}))
	if !strings.Contains(got, `"chat_sessions":[{"cost_usd":0.42,"first_message":"m","id":"u1","started_at":"2026-11-25T01:02:03.000Z"}]`) || !strings.Contains(got, `"spec_cost_usd":0.42`) {
		t.Errorf("detail = %s", got)
	}
}
