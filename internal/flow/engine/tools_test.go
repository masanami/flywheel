package engine

import (
	"slices"
	"testing"

	"github.com/masanami/flywheel/internal/flow/runstate"
)

// AC-131・AC-132: tools を書いたステップの起動の引数にだけ --tools が入る。
func TestLLMToolsAreLaunchArguments(t *testing.T) {
	f := newFake(t)
	f.respond(0, 0, claudeResult("pass", 0.1))
	st, _ := start(t, "llm-tools", nil)
	if st.Status != runstate.StatusSucceeded {
		t.Fatalf("status = %s (%s)", st.Status, st.Reason)
	}
	if got, ok := flag(f.argv(1), "--tools"); !ok || got != "Read,Edit" {
		t.Errorf("narrow: --tools = %q (present %v) in %v", got, ok, f.argv(1))
	}
	if argv := f.argv(2); slices.Contains(argv, "--tools") {
		t.Errorf("wide: --tools must not be passed: %v", argv)
	}
}
