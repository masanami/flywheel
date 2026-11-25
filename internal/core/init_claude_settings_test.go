package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// docs/features/chat-session.md「flywheel init が Claude Code の設定のファイルを作る」「init が作る設定」。

const wantClaudeSettings = `{"permissions": {"allow": ["Bash(flywheel:*)", "Bash(gh issue view:*)", "Bash(gh issue list:*)", "Bash(gh pr view:*)", "Bash(gh pr list:*)", "Bash(gh pr checks:*)", "Bash(gh pr diff:*)"]}}`

func jsonValue(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("not JSON: %v: %s", err, data)
	}
	return v
}

// .claude/ の有無に関わらず、設定のファイルが無ければ作る。2 回目は作らない。
func TestInit_CreatesClaudeSettingsWhenMissing(t *testing.T) {
	t.Parallel()
	for _, withDir := range []bool{false, true} {
		ws := t.TempDir()
		if withDir {
			if err := os.Mkdir(filepath.Join(ws, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		res, err := Init(ws)
		if err != nil {
			t.Fatal(err)
		}
		if !res.ClaudeSettingsCreated {
			t.Errorf("withDir=%v: ClaudeSettingsCreated = false, want true", withDir)
		}
		data, err := os.ReadFile(filepath.Join(ws, ".claude", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := jsonValue(t, data), jsonValue(t, []byte(wantClaudeSettings)); !reflect.DeepEqual(got, want) {
			t.Errorf("settings = %v, want %v", got, want)
		}
		again, err := Init(ws)
		if err != nil {
			t.Fatal(err)
		}
		if again.ClaudeSettingsCreated {
			t.Error("re-run: ClaudeSettingsCreated = true, want false")
		}
	}
}

// あるファイルは、中身によらず（JSON でなくても・読めなくても）中身と更新時刻を変えない。
func TestInit_LeavesAnExistingClaudeSettingsAlone(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		content string
		mode    os.FileMode
	}{
		"JSON":     {`{"permissions": {"allow": []}}`, 0o644},
		"JSON でない": {"not json {", 0o644},
		"空":        {"", 0o644},
		"読めない":     {"x", 0o000},
	} {
		ws := t.TempDir()
		path := filepath.Join(ws, ".claude", "settings.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tc.mode); err != nil {
			t.Fatal(err)
		}
		res, err := Init(ws)
		if err != nil {
			t.Fatalf("%s: Init: %v", name, err)
		}
		if res.ClaudeSettingsCreated {
			t.Errorf("%s: ClaudeSettingsCreated = true, want false", name)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(old) || info.Mode().Perm() != tc.mode {
			t.Errorf("%s: mtime/mode = %v/%v, want unchanged", name, info.ModTime(), info.Mode())
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != tc.content {
			t.Errorf("%s: content = %q, want unchanged", name, data)
		}
	}
}
