package view_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルのテストは、core の見直し 区切り S3d の土台（masanami/flywheel#927）が足した API の形と閉集合を確かめる。

// タスクの要素の 5 つのキーは、値が無いとき artifacts が []・facts が {}・delegate／landing／verification が null で出る。
func TestFromTasks_S3dKeysDefault_927(t *testing.T) {
	got := marshal(t, view.FromTasks([]core.Task{{ID: "T-1"}}))
	var out []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &out); err != nil || len(out) != 1 {
		t.Fatalf("%v: %s", err, got)
	}
	want := map[string]string{"artifacts": "[]", "facts": "{}", "delegate": "null", "landing": "null", "verification": "null"}
	for k, v := range want {
		if string(out[0][k]) != v {
			t.Errorf("tasks[0].%s = %s, want %s", k, out[0][k], v)
		}
	}
}

// 値が入ったタスクの要素は、仕様のキー名と形で出る。
func TestTask_S3dKeysShape_927(t *testing.T) {
	kind, role, landing := "review", "integration", "sent"
	got := marshal(t, view.Task{
		Artifacts:    []view.TaskArtifact{{Kind: "pr", Ref: "https://example.com/1", Role: &role, State: "open"}},
		Delegate:     &view.TaskDelegate{RequestedKind: &kind, RunID: "R-1", State: "waiting", Steps: []view.TaskDelegateStep{}, Summary: "要約"},
		Facts:        map[string]string{"self_review": "done"},
		Landing:      &landing,
		Verification: &view.TaskVerification{At: "2026-11-21T00:00:00Z", Reason: "理由", Verdict: "met"},
	})
	for _, want := range []string{
		`"artifacts":[{"kind":"pr","ref":"https://example.com/1","role":"integration","state":"open"}]`,
		`"delegate":{"flow":null,"requested_kind":"review","run_id":"R-1","state":"waiting","steps":[],"summary":"要約"}`,
		`"facts":{"self_review":"done"}`,
		`"landing":"sent"`,
		`"verification":{"at":"2026-11-21T00:00:00Z","reason":"理由","verdict":"met"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%s が無い: %s", want, got)
		}
	}
}

// ツリーのタスクと課題の progress は、core が値を入れるまで null で出る。
func TestFromChallengeTree_ProgressDefaultsNull_927(t *testing.T) {
	c := core.TreeChallenge{ID: "C-1", Tasks: []core.TreeTask{{Key: "a", Phase: core.TaskPhaseRunning}}}
	got := marshal(t, view.FromTreeChallenge(c))
	if strings.Count(got, `"progress":null`) != 2 {
		t.Errorf("課題とタスクの progress が null でない: %s", got)
	}
}

// 画面と共有する値の集合に、細かい進み・統合先へ入れる進み・検証の判定・委譲先の実行の状態・不可逆操作の種類と状態がある。
func TestClosedSets_S3d_927(t *testing.T) {
	want := map[string][]string{
		"TASK_PROGRESS_CODES":      {"working", "verifying", "verified", "landing"},
		"CHALLENGE_PROGRESS_CODES": {"reflecting", "checking"},
		"TASK_LANDING_CODES":       {"verified", "sent", "landed"},
		"VERDICT_CODES":            {"met", "not_met", "uncertain"},
		"DELEGATE_STATE_CODES":     {"running", "waiting", "succeeded", "failed", "cancelled"},
		"OPERATION_KIND_CODES":     {"release", "delete", "external_send", "other"},
		"OPERATION_STATE_CODES":    {"pending", "approved", "rejected", "executed", "closed"},
	}
	got := map[string][]string{}
	for _, cs := range view.ClosedSets() {
		got[cs.Name] = cs.Codes
	}
	for name, codes := range want {
		if !reflect.DeepEqual(got[name], codes) {
			t.Errorf("%s = %v, want %v", name, got[name], codes)
		}
	}
	// phase の値の集合は変えない。
	if !reflect.DeepEqual(got["TASK_PHASE_CODES"], []string{"running", "waiting", "next", "blocked", "done", "cancelled"}) {
		t.Errorf("TASK_PHASE_CODES が変わっている: %v", got["TASK_PHASE_CODES"])
	}
}

// AC-147・AC-149・AC-150（#1229）: delegate.steps の要素はキーの辞書順で、行が無ければ空の配列、flow が無ければ null。
func TestTaskDelegate_StepsShape_1229(t *testing.T) {
	cost := 1.9429
	outcome := "pass"
	got := marshal(t, view.TaskDelegate{Flow: nil, Steps: []view.TaskDelegateStep{{
		Attempt: 1, CostUnknown: true, CostUSD: &cost, Kind: "llm", ModelsUsed: []string{"a", "b"}, Outcome: &outcome,
		StartedAt: "2026-11-01T00:00:00Z", State: "finished", Step: "implement",
	}}})
	want := `{"flow":null,"requested_kind":null,"run_id":"","state":"","steps":[{"attempt":1,"cost_unknown":true,"cost_usd":1.9429,"effort":null,"ended_at":null,"kind":"llm","model":null,"models_used":["a","b"],"outcome":"pass","started_at":"2026-11-01T00:00:00Z","state":"finished","step":"implement"}],"summary":""}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
