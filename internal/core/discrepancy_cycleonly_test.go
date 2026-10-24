package core

import "testing"

func TestDiscrepancyCycleOnly(t *testing.T) {
	cases := []struct {
		name  string
		kinds []DiscrepancyKind
		want  bool
	}{
		{"empty", nil, false},
		{"cycle only", []DiscrepancyKind{DiscrepancyKindDependencyCycle}, true},
		{"cycle and upstream", []DiscrepancyKind{DiscrepancyKindUpstreamClosed, DiscrepancyKindDependencyCycle}, false},
		{"merged unapproved", []DiscrepancyKind{DiscrepancyKindMergedUnapproved}, false},
	}
	for _, c := range cases {
		if got := (Discrepancy{Kinds: c.kinds}).CycleOnly(); got != c.want {
			t.Errorf("%s: CycleOnly() = %v, want %v", c.name, got, c.want)
		}
	}
}
