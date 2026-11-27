package view

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// AC-280（Issue #1382）: スーパーバイザーの答えの要素の basis に、path と locator が出る。
func TestSpecDraftSupervisorAnswerBasisIsShown(t *testing.T) {
	got, err := json.Marshal(fromSpecDraft(&core.SpecDraft{Answers: []core.SpecDraftAnswer{{
		Kind: core.SpecDraftAnswerKindAnswer, Attempt: 1, Question: "q", Answer: "a", By: "supervisor",
		Basis: []core.SpecDraftBasis{{Path: ".flywheel/repos/app/docs/a.md", Locator: "節 1"}},
	}}}))
	if err != nil {
		t.Fatal(err)
	}
	if want := `"basis":[{"locator":"節 1","path":".flywheel/repos/app/docs/a.md"}]`; !strings.Contains(string(got), want) {
		t.Errorf("json lacks %s:\n%s", want, got)
	}
}
