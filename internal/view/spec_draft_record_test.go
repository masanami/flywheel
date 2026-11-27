package view_test

import (
	"encoding/json"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// 課題の詳細の spec_draft の JSON の形（docs/features/request-to-plan-s2b.md「記録の JSON の形」）。
func TestShowChallenge_SpecDraftShape(t *testing.T) { // AC-358〜AC-366・AC-370〜AC-373
	t.Parallel()
	base, holdID, why := "docs/features/example.md", "H-7", core.SpecDraftHoldQuestion
	got, err := json.Marshal(view.FromShowChallenge(&core.ChallengeDetail{SpecDraft: &core.SpecDraft{
		Path: ".flywheel/specs/c-1.md", BasePath: &base, Repo: "flow-wt", Reason: "why", State: core.SpecDraftStateAwaitingOwner, Attempts: 2,
		Questions: []core.SpecDraftQuestion{{Question: "Q?", Options: []core.SpecDraftOption{{Label: "a"}, {Label: "b"}}, Recommended: 2, Reason: "r"}},
		Answers:   []core.SpecDraftAnswer{{Kind: core.SpecDraftAnswerKindAnswer, Attempt: 1, Question: "P?", Answer: "yes", By: "supervisor"}},
		HoldID:    &holdID, HoldReason: &why,
	}}))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	const want = `{"answers":[{"answer":"yes","attempt":1,"basis":[],"by":"supervisor","kind":"answer","question":"P?"}],"attempts":2,` +
		`"base_path":"docs/features/example.md","hold_id":"H-7","hold_reason":"spec_question","max_attempts":3,"path":".flywheel/specs/c-1.md",` +
		`"questions":[{"options":[{"label":"a"},{"label":"b"}],"question":"Q?","reason":"r","recommended":2}],"reason":"why","repo":"flow-wt","state":"awaiting_owner"}`
	if string(doc["spec_draft"]) != want {
		t.Errorf("spec_draft = %s\nwant %s", doc["spec_draft"], want)
	}
}
