package view_test

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

func TestFromDiscrepanciesCycleOnly(t *testing.T) {
	out := view.FromDiscrepancies([]core.Discrepancy{
		{ChallengeID: "C-1", Kinds: []core.DiscrepancyKind{core.DiscrepancyKindDependencyCycle}},
		{ChallengeID: "C-2", Kinds: []core.DiscrepancyKind{core.DiscrepancyKindMergedUnapproved}},
	})
	if !out[0].CycleOnly || out[1].CycleOnly {
		t.Fatalf("CycleOnly = %v,%v, want true,false", out[0].CycleOnly, out[1].CycleOnly)
	}
	if got := marshal(t, out[0]); !strings.Contains(got, `"cycle_only":true`) {
		t.Errorf("json = %s, want cycle_only:true", got)
	}
	if got := marshal(t, out[1]); strings.Contains(got, "cycle_only") {
		t.Errorf("json = %s, want no cycle_only", got)
	}
}
