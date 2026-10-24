package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// release の段が途中でエラーになっても、それまでに実行済みのマージはエラーの文言に残る。
func TestWithExecutedReleases_KeepsThePartialResultInTheMessage(t *testing.T) {
	e := withExecutedReleases(NewError(CodeStoreError, "boom"), []core.ReleaseExecItem{
		{OperationID: "OP-1", Result: core.ExecutionResultMerged, MergeCommit: "m1"},
		{OperationID: "OP-2", Result: core.ExecutionResultHeadChanged},
	})
	for _, want := range []string{"boom", "OP-1 merged m1", "OP-2 head_changed"} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("message = %q, want it to contain %q", e.Message, want)
		}
	}
	if e := withExecutedReleases(NewError(CodeStoreError, "boom"), nil); e.Message != "boom" {
		t.Errorf("message without items = %q, want unchanged", e.Message)
	}
}
