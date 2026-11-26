package view_test

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

func TestShowResponse_SpecPathAndTaskCovers(t *testing.T) {
	empty := marshal(t, view.FromChallengeDetail(&core.ChallengeDetail{Tasks: []core.Task{{ID: "T-1"}}}))
	for _, want := range []string{`"spec_path":null`, `"covers":[]`} {
		if !strings.Contains(empty, want) {
			t.Errorf("show lacks %s: %s", want, empty)
		}
	}
	p := "docs/x.md"
	got := marshal(t, view.FromChallengeDetail(&core.ChallengeDetail{
		Challenge: core.Challenge{SpecPath: &p},
		Tasks:     []core.Task{{ID: "T-1", Covers: []string{"AC-0123abcd"}}},
	}))
	for _, want := range []string{`"spec_path":"docs/x.md"`, `"covers":["AC-0123abcd"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("show lacks %s: %s", want, got)
		}
	}
}
