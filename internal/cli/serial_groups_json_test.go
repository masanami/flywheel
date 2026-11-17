package cli

import (
	"encoding/json"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// 委譲の段（run）の phase は、判断の段と同じ形（items・not_started・phase・skipped）で、serial_groups のキーを持たない。

// phaseDoc は cycle の段を JSON へマーシャルして読み戻す（出力の形そのものを検査する）。
func phaseDoc(t *testing.T, p core.CyclePhaseResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(view.FromCyclePhase(p))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// AC-6: `flywheel cycle --json` の委譲の段の出力に serial_groups のキーが無い。
func TestCyclePhaseJSON_RunPhaseHasNoSerialGroups(t *testing.T) {
	t.Parallel()
	for _, skipped := range []bool{false, true} {
		got := phaseDoc(t, core.CyclePhaseResult{Phase: core.CyclePhaseRun, Skipped: skipped})
		if _, ok := got["serial_groups"]; ok {
			t.Errorf("skipped=%v: run phase has serial_groups: %v", skipped, got)
		}
		for _, k := range []string{"phase", "skipped", "items", "not_started"} {
			if _, ok := got[k]; !ok {
				t.Errorf("skipped=%v: run phase lacks %q: %v", skipped, k, got)
			}
		}
	}
}

// AC-7: `flywheel run --json` の出力（委譲の段の phase の形）に serial_groups のキーが無い。
func TestRunJSON_DelegationPhaseHasNoSerialGroups(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(view.FromDelegationPhase(false, &core.JudgmentAutoResult{}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["serial_groups"]; ok {
		t.Errorf("delegation phase has serial_groups: %v", got)
	}
}

func TestCyclePhaseJSON_JudgmentPhasesHaveNoSerialGroups(t *testing.T) {
	t.Parallel()
	if _, ok := phaseDoc(t, core.CyclePhaseResult{Phase: core.CyclePhasePlan})["serial_groups"]; ok {
		t.Error("no phase has serial_groups")
	}
}
