package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 仕様の下書きの見分けと、置き換える元の文書の場所（--spec-base）を、コマンドを起動して確かめる。

func showTextOf(t *testing.T, ws, id string) string {
	t.Helper()
	code, stdout, stderr := runCLIRaw(t, "show", id, "--workspace", ws)
	if code != 0 {
		t.Fatalf("show %s: exit %d: %s", id, code, stderr)
	}
	return stdout
}

// AC-13〜AC-16・AC-22・AC-23・AC-26 と、詳細の spec_is_draft。
func TestSpecBaseFlag_CreateEditAndShowJSON(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	const draft, base = ".flywheel/specs/example.md", "docs/features/example.md"
	runJSON(t, ws, "create", "--title", "draft", "--spec", draft, "--spec-base", base)
	runJSON(t, ws, "create", "--title", "no base", "--spec", draft)
	runJSON(t, ws, "create", "--title", "no draft", "--spec-base", "no/such/file.md")

	expect := func(id string, isDraft bool, wantBase any) {
		t.Helper()
		c := showChallengeDoc(t, ws, id)
		if got, ok := c["spec_is_draft"]; !ok || got != isDraft {
			t.Errorf("%s: spec_is_draft = %v (present=%v), want %v", id, got, ok, isDraft)
		}
		if got, ok := c["spec_base_path"]; !ok || got != wantBase {
			t.Errorf("%s: spec_base_path = %v (present=%v), want %v", id, got, ok, wantBase)
		}
	}
	expect("C-1", true, base)
	expect("C-2", true, nil)
	expect("C-3", false, "no/such/file.md")

	// --spec-base だけの edit も受け取る。
	runJSON(t, ws, "edit", "C-1", "--spec-base", "docs/other.md")
	expect("C-1", true, "docs/other.md")
	runJSON(t, ws, "edit", "C-1", "--spec-base", "")
	expect("C-1", true, nil)

	coretest.SetChallengeStatus(t, ws, 1, string(core.StatusAwaitingPlanApproval))
	runJSON(t, ws, "edit", "C-1", "--spec-base", base)
	expect("C-1", true, base)
}

// AC-17〜AC-21・AC-24。
func TestSpecBaseFlag_Errors(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	runJSON(t, ws, "create", "--title", "kept", "--spec-base", "docs/a.md")
	for _, bad := range []string{"/abs/x.md", "docs/../x.md", strings.Repeat("a", 501)} {
		requireErrorCode(t, []string{"create", "--title", "bad", "--spec-base", bad, "--workspace", ws}, 1, CodeValidationFailed)
		requireErrorCode(t, []string{"edit", "C-1", "--spec-base", bad, "--workspace", ws}, 1, CodeValidationFailed)
	}
	if got := len(runJSON(t, ws, "list")["challenges"].([]any)); got != 1 {
		t.Errorf("challenges = %d, want 1 (a rejected create makes nothing)", got)
	}
	if got := showChallengeDoc(t, ws, "C-1")["spec_base_path"]; got != "docs/a.md" {
		t.Errorf("spec_base_path after rejected edits = %v, want docs/a.md", got)
	}
	long := strings.Repeat("a", 500)
	runJSON(t, ws, "edit", "C-1", "--spec-base", long)
	if got := showChallengeDoc(t, ws, "C-1")["spec_base_path"]; got != long {
		t.Errorf("a 500-character path was not kept: %v", got)
	}

	coretest.SetChallengeStatus(t, ws, 1, string(core.StatusDone))
	// いまある直すコマンドの終わった課題の扱いと同じ誤り。
	requireErrorCode(t, []string{"edit", "C-1", "--title", "x", "--workspace", ws}, 1, CodeTerminalState)
	requireErrorCode(t, []string{"edit", "C-1", "--spec-base", "docs/b.md", "--workspace", ws}, 1, CodeTerminalState)
}

// AC-27〜AC-30。
func TestShowText_DraftMarkAndSpecBaseLine(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	runJSON(t, ws, "create", "--title", "draft", "--spec", "./.flywheel/specs/example.md", "--spec-base", "docs/features/example.md")
	runJSON(t, ws, "create", "--title", "plain", "--spec", "docs/x.md")

	draft := showTextOf(t, ws, "C-1")
	for _, line := range []string{
		"\n仕様の文書の場所: ./.flywheel/specs/example.md（下書き）\n",
		"\n置き換える元の文書の場所: docs/features/example.md\n",
	} {
		if strings.Count(draft, line) != 1 {
			t.Errorf("show text does not have the line %q exactly once:\n%s", line, draft)
		}
	}
	plain := showTextOf(t, ws, "C-2")
	if !strings.Contains(plain, "\n仕様の文書の場所: docs/x.md\n") || strings.Contains(plain, "下書き") {
		t.Errorf("AC-28: a document that is not a draft is marked:\n%s", plain)
	}
	if strings.Contains(plain, "置き換える元") {
		t.Errorf("AC-30: the spec base line is shown without a spec base:\n%s", plain)
	}
}

func TestShowText_SpecBaseLineEscapesControlCharacters(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	runJSON(t, ws, "create", "--title", "t", "--spec-base", "docs/a\n状態: b.md")
	if got := showTextOf(t, ws, "C-1"); strings.Contains(got, "\n状態: b.md") {
		t.Errorf("a newline in the spec base path broke the line:\n%s", got)
	}
}
