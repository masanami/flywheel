package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 課題の仕様の文書の場所（docs/features/request-to-plan.md「課題の仕様の文書の場所」）を、コマンドを起動して確かめる。

func showChallengeDoc(t *testing.T, ws, id string) map[string]any {
	t.Helper()
	c, ok := runJSON(t, ws, "show", id)["challenge"].(map[string]any)
	if !ok {
		t.Fatal("show has no challenge object")
	}
	if _, ok := c["spec_path"]; !ok {
		t.Fatalf("show challenge has no spec_path key: %v", c)
	}
	return c
}

func TestSpecFlag_CreateEditAndShow(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)

	runJSON(t, ws, "create", "--title", "with", "--spec", "docs/x.md")
	runJSON(t, ws, "create", "--title", "without")
	if got := showChallengeDoc(t, ws, "C-1")["spec_path"]; got != "docs/x.md" {
		t.Errorf("spec_path = %v, want docs/x.md", got)
	}
	if got := showChallengeDoc(t, ws, "C-2")["spec_path"]; got != nil {
		t.Errorf("spec_path without --spec = %v, want null", got)
	}
	textOf := func(id string) string {
		code, stdout, stderr := runCLIRaw(t, "show", id, "--workspace", ws)
		if code != 0 {
			t.Fatalf("show %s: exit %d: %s", id, code, stderr)
		}
		return stdout
	}
	if got := textOf("C-1"); !strings.Contains(got, "\n仕様の文書の場所: docs/x.md\n") {
		t.Errorf("show text lacks the spec path line:\n%s", got)
	}
	if got := textOf("C-2"); strings.Contains(got, "仕様の文書") {
		t.Errorf("show text of a challenge without a spec path mentions it:\n%s", got)
	}

	runJSON(t, ws, "edit", "C-1", "--spec", "docs/y.md")
	if got := showChallengeDoc(t, ws, "C-1")["spec_path"]; got != "docs/y.md" {
		t.Errorf("spec_path after edit = %v, want docs/y.md", got)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "out")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{filepath.Join(ws, "docs/x.md"), "../x.md", "out/x.md"} {
		requireErrorCode(t, []string{"create", "--title", "bad", "--spec", bad, "--workspace", ws, "--json"}, 1, CodeValidationFailed)
		requireErrorCode(t, []string{"edit", "C-1", "--spec", bad, "--workspace", ws, "--json"}, 1, CodeValidationFailed)
	}
	if got := len(runJSON(t, ws, "list")["challenges"].([]any)); got != 2 {
		t.Errorf("challenges = %d, want 2 (a rejected create makes nothing)", got)
	}
	if got := showChallengeDoc(t, ws, "C-1")["spec_path"]; got != "docs/y.md" {
		t.Errorf("spec_path after rejected edits = %v, want docs/y.md", got)
	}

	runJSON(t, ws, "edit", "C-1", "--spec", "")
	if got := showChallengeDoc(t, ws, "C-1")["spec_path"]; got != nil {
		t.Errorf(`spec_path after --spec "" = %v, want null`, got)
	}
}

func TestShowText_SpecPathLineEscapesControlCharacters(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	runJSON(t, ws, "create", "--title", "t", "--spec", "docs/a\n状態: b.md")
	_, stdout, _ := runCLIRaw(t, "show", "C-1", "--workspace", ws)
	if strings.Contains(stdout, "\n状態: b.md") {
		t.Errorf("a newline in the spec path broke the line:\n%s", stdout)
	}
}
