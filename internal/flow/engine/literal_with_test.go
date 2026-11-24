package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/flow/runstate"
)

// AC-69・AC-70: with の文字列の定数 "on" は、始めて最初に起動したステップの添付データにも、ゲートの後（別プロセスの resume を模して開き直した後）に
// 起動し直したステップの添付データにも、文字列の "on" として渡る。
func TestLiteralWithIsAttachedOnStartAndAfterResume(t *testing.T) {
	f := newFake(t)
	f.respond(0, 0, claudeResult("pass", 0.1))
	st, e := start(t, "llm-literal-with", map[string]any{"issue": 1})
	if st.Status != runstate.StatusWaiting {
		t.Fatalf("status = %s (%s)", st.Status, st.Reason)
	}
	mustResolve(t, e, input("respond"))
	if f.calls() != 2 {
		t.Fatalf("claude was launched %d times, want 2", f.calls())
	}
	for n := 1; n <= 2; n++ {
		if got := attachedInputs(t, f.file(n, "stdin"))["codex_review"]; got != `"on"` {
			t.Errorf("launch %d: attached inputs.codex_review = %s, want the string \"on\"", n, got)
		}
	}
}

// attachedInputs は、添付データの JSON ブロックの inputs を、欄の名前 → 生の JSON にして返す。
func attachedInputs(t *testing.T, stdin string) map[string]string {
	t.Helper()
	_, rest, ok := strings.Cut(stdin, "```json\n")
	if !ok {
		t.Fatalf("no attached data block in %q", stdin)
	}
	block, _, _ := strings.Cut(rest, "\n```")
	var data struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(block), &data); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k, v := range data.Inputs {
		out[k] = string(v)
	}
	return out
}
