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
	got := chatJSON(t, view.FromChat(&core.Chat{Messages: []core.ChatMessage{}}, core.ChatToolsRestricted))
	want := `{"cost_usd":0,"messages":[],"session_id":null,"started_at":null,"state":"idle","tools":"restricted"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// Messages が nil でも `[]` で出る。
	if got := chatJSON(t, view.FromChat(&core.Chat{}, core.ChatToolsFull)); !strings.Contains(got, `"messages":[]`) || !strings.Contains(got, `"tools":"full"`) {
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
			{ID: 1, Role: core.ChatRoleOwner, Body: "q", State: core.ChatStateDone, CreatedAt: at, Viewing: &core.ChatViewing{ChallengeID: "C-1", Title: "題"}, Activity: []core.ChatCommand{}},
			{ID: 2, Role: core.ChatRoleOrchestrator, Body: "a", State: core.ChatStateReplying, CostUSD: 1.5, CreatedAt: at, Activity: []core.ChatCommand{{Command: "flywheel status", OK: false}}},
		},
	}
	got := chatJSON(t, view.FromChat(chat, core.ChatToolsRestricted))
	want := `{"cost_usd":1.5,"messages":[` +
		`{"activity":[],"body":"q","cost_usd":0,"created_at":"2026-11-22T01:02:03.000Z","id":1,"role":"owner","state":"done","viewing":{"challenge_id":"C-1","task_id":null,"title":"題"}},` +
		`{"activity":[{"command":"flywheel status","ok":false}],"body":"a","cost_usd":1.5,"created_at":"2026-11-22T01:02:03.000Z","id":2,"role":"orchestrator","state":"replying","viewing":null}` +
		`],"session_id":"uuid-1","started_at":"2026-11-22T01:02:03.000Z","state":"replying","tools":"restricted"}`
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
		"CHAT_TOOLS_CODES":         {"restricted", "full"},
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
