package view_test

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

func TestShowResponse_SpecDraftBaseAndLandsSpec(t *testing.T) {
	empty := marshal(t, view.FromChallengeDetail(&core.ChallengeDetail{Tasks: []core.Task{{ID: "T-1"}}}))
	for _, want := range []string{`"spec_base_path":null`, `"spec_is_draft":false`, `"lands_spec":false`} {
		if !strings.Contains(empty, want) {
			t.Errorf("show lacks %s: %s", want, empty)
		}
	}
	spec, base := ".flywheel/specs/x.md", "docs/x.md"
	got := marshal(t, view.FromChallengeDetail(&core.ChallengeDetail{
		Challenge: core.Challenge{SpecPath: &spec, SpecBasePath: &base},
		Tasks:     []core.Task{{ID: "T-1", LandsSpec: true}},
	}))
	for _, want := range []string{`"spec_base_path":"docs/x.md"`, `"spec_is_draft":true`, `"lands_spec":true`} {
		if !strings.Contains(got, want) {
			t.Errorf("show lacks %s: %s", want, got)
		}
	}
}
