package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// docs/features/chat-session.md「flywheel init の設定のファイル」の CLI の出力。

func TestInit_OutputSaysWhetherTheClaudeSettingsWereCreated(t *testing.T) {
	t.Parallel()
	initOutput := func(ws string, extra ...string) string {
		var out, errb bytes.Buffer
		args := append([]string{"init", "--workspace", ws}, extra...)
		if code := runWithLookPath(args, strings.NewReader(""), &out, &errb, defaultCommands(), lookPathFor(t)); code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", args, code, errb.String())
		}
		return out.String()
	}
	jsonValue := func(ws string) any {
		var v map[string]any
		if err := json.Unmarshal([]byte(initOutput(ws, "--json")), &v); err != nil {
			t.Fatal(err)
		}
		return v["claude_settings_created"]
	}

	ws := t.TempDir()
	if got := jsonValue(ws); got != true {
		t.Errorf("first init: claude_settings_created = %v, want true", got)
	}
	if got := jsonValue(ws); got != false {
		t.Errorf("second init: claude_settings_created = %v, want false", got)
	}

	ws = t.TempDir()
	if got, want := initOutput(ws), "Claude Code の設定: 作った（"; !strings.Contains(got, want) {
		t.Errorf("first init text lacks %q: %s", want, got)
	}
	if got, want := initOutput(ws), "Claude Code の設定: 作っていない（"; !strings.Contains(got, want) {
		t.Errorf("second init text lacks %q: %s", want, got)
	}
}
