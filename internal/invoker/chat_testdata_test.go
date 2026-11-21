package invoker

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// testdata の stream-json の写し（実物の実行エンジンの出力）から、対話の実行が読む 4 つのもの
// （会話の始まりの行・発言の文・コマンドの実行とその成否・終わりの行の終わり方と費用）が読めることを確かめる。
// 対話の実行の実装（後続）が読み違えないための、写しの形の固定。ここでの読み取りは検査用で、本番の読み取りではない。

type chatLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Session string `json:"session_id"`
	Message struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Name      string `json:"name"`
			ID        string `json:"id"`
			ToolUseID string `json:"tool_use_id"`
			IsError   bool   `json:"is_error"`
			Input     struct {
				Command string `json:"command"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
	IsError    bool     `json:"is_error"`
	NumTurns   int      `json:"num_turns"`
	StopReason string   `json:"stop_reason"`
	Terminal   string   `json:"terminal_reason"`
	Cost       *float64 `json:"total_cost_usd"`
	Result     string   `json:"result"`
}

func readChatStream(t *testing.T, name string) []chatLine {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []chatLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var l chatLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("%s: a line is not JSON: %v", name, err)
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestChatStreamTestdata_FirstMessage(t *testing.T) {
	lines := readChatStream(t, "chat_stream_success.jsonl")
	if first := lines[0]; first.Type != "system" || first.Subtype != "init" || first.Session == "" {
		t.Fatalf("first line = %+v, want system/init with a session_id", first)
	}
	var texts, commands []string
	results := map[string]bool{} // tool_use_id → 成功か
	uses := map[string]string{}  // tool_use id → コマンド
	for _, l := range lines {
		for _, c := range l.Message.Content {
			switch {
			case l.Type == "assistant" && c.Type == "text":
				texts = append(texts, c.Text)
			case l.Type == "assistant" && c.Type == "tool_use" && c.Name == "Bash":
				uses[c.ID] = c.Input.Command
				commands = append(commands, c.Input.Command)
			case l.Type == "user" && c.Type == "tool_result":
				results[c.ToolUseID] = !c.IsError
			}
		}
	}
	if len(texts) != 1 || texts[0] == "" {
		t.Errorf("speeches = %q, want one", texts)
	}
	if len(commands) != 2 || commands[0] != "echo hello" || commands[1] != "nosuchcmd_xyz" {
		t.Errorf("commands = %q", commands)
	}
	for id, cmd := range uses {
		ok, found := results[id]
		if !found {
			t.Errorf("command %q has no tool_result", cmd)
		}
		if want := cmd == "echo hello"; ok != want {
			t.Errorf("command %q: ok = %v, want %v", cmd, ok, want)
		}
	}
	last := lines[len(lines)-1]
	if last.Type != "result" || last.Subtype != "success" || last.IsError || last.Cost == nil || *last.Cost <= 0 || last.NumTurns != 3 || last.Terminal != "completed" || last.Result != texts[0] {
		t.Errorf("last line = %+v, want a successful result with a cost", last)
	}
}

// 続き（--resume）でも、先頭に会話の始まりの行が出て、同じ会話の識別を持つ。
func TestChatStreamTestdata_ResumeKeepsTheSession(t *testing.T) {
	first := readChatStream(t, "chat_stream_success.jsonl")
	second := readChatStream(t, "chat_stream_resume.jsonl")
	if second[0].Type != "system" || second[0].Subtype != "init" || second[0].Session != first[0].Session {
		t.Errorf("resume first line = %+v, want system/init of session %s", second[0], first[0].Session)
	}
	if last := second[len(second)-1]; last.Type != "result" || last.Cost == nil {
		t.Errorf("resume last line = %+v", last)
	}
}
