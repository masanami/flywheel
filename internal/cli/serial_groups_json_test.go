package cli

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// #106: 委譲の段（run）の phase は判断の段の形に serial_groups を足した形で、skipped の段は [] を持つ
// （AC-329〜333・369 の出力の形。段の結線は cycle の仕上げのチケット）。

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

func TestCyclePhaseJSON_RunPhaseCarriesSerialGroups(t *testing.T) {
	sha := "cafe01"
	got := phaseDoc(t, core.CyclePhaseResult{
		Phase: core.CyclePhaseRun,
		SerialGroups: []core.SerialGroup{
			{Repo: "r", Challenges: []string{"C-9", "C-1", "C-2"}, Reasons: []core.SerialGroupReason{core.SerialReasonSharedFiles, core.SerialReasonDependency}, PredictionHeadSHA: &sha},
			{Repo: "r", Challenges: []string{"C-3"}, Reasons: nil},
		},
	})
	want := []any{
		map[string]any{"repo": "r", "challenges": []any{"C-9", "C-1", "C-2"}, "reasons": []any{"shared_files", "dependency"}, "prediction_head_sha": "cafe01"},
		map[string]any{"repo": "r", "challenges": []any{"C-3"}, "reasons": []any{}, "prediction_head_sha": nil},
	}
	if !reflect.DeepEqual(got["serial_groups"], want) {
		t.Errorf("serial_groups = %#v, want %#v", got["serial_groups"], want)
	}
	for _, k := range []string{"phase", "skipped", "items", "not_started"} {
		if _, ok := got[k]; !ok {
			t.Errorf("run phase lacks %q: %v", k, got)
		}
	}
}

func TestCyclePhaseJSON_SkippedRunPhaseHasNoSerialGroups(t *testing.T) {
	got := phaseDoc(t, core.CyclePhaseResult{Phase: core.CyclePhaseRun, Skipped: true})
	if g, ok := got["serial_groups"].([]any); !ok || len(g) != 0 || got["skipped"] != true {
		t.Errorf("phase = %#v, want skipped with serial_groups []", got)
	}
}

func TestCyclePhaseJSON_JudgmentPhasesHaveNoSerialGroups(t *testing.T) {
	if _, ok := phaseDoc(t, core.CyclePhaseResult{Phase: core.CyclePhasePlan})["serial_groups"]; ok {
		t.Error("only the run phase has serial_groups")
	}
}
