package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは S4b-2 の表示の受入基準（`status` のスーパーバイザーが確認中・オーナー待ちの数え、`show` の保留の値と
// 対処の記録・打った手の件数）を検証する。保留と対処の記録は、土台の列へ直接置く（スーパーバイザーの段を待たない）。

const svRaisedAt = "2026-11-22T00:00:00.000Z"

// svHoldChallenge は人間対応待ちの課題に、止まり方 verify_uncertain と、スーパーバイザーの状態 state の保留を 1 件置く。
func svHoldChallenge(t *testing.T, ws, state, findings, recommendation string) string {
	t.Helper()
	id := createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	kind := "verify_uncertain"
	if state == "" {
		kind = ""
	}
	coretest.InsertSupervisorHold(t, ws, n, "どうしますか？", "verifying", svRaisedAt, kind, state, findings, recommendation)
	coretest.SetChallengeStatus(t, ws, n, "awaiting_human")
	return id
}

func challengeIDsOf(t *testing.T, v any, what string) []string {
	t.Helper()
	out := []string{}
	for _, e := range anySlice(t, v, what) {
		out = append(out, e.(map[string]any)["id"].(string))
	}
	return out
}

func TestStatus_SupervisorPendingIsCheckingNotOwnerWaiting_AC232_to_238(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	pending := svHoldChallenge(t, ws, "pending", "", "")
	escalated := svHoldChallenge(t, ws, "escalated", "調べた結果", "再検証を勧める")
	noMark := svHoldChallenge(t, ws, "", "", "")

	out := runJSON(t, ws, "status")
	if got := challengeIDsOf(t, out["needs_human"].(map[string]any)["challenges"], "needs_human.challenges"); !reflect.DeepEqual(got, []string{escalated, noMark}) {
		t.Errorf("needs_human.challenges = %v, want [%s %s]（pending は入らない。escalated・NULL は入る）", got, escalated, noMark)
	}
	checking := out["supervisor_checking"].(map[string]any)
	if got := challengeIDsOf(t, checking["challenges"], "supervisor_checking.challenges"); !reflect.DeepEqual(got, []string{pending}) {
		t.Errorf("supervisor_checking.challenges = %v, want [%s]", got, pending)
	}
	if checking["count"] != float64(1) {
		t.Errorf("supervisor_checking.count = %v, want 1", checking["count"])
	}

	text := runText(t, ws, "status")
	requireInOrder(t, text, "あなたの回答を待っているもの:", escalated, noMark, "スーパーバイザーが確認中: 1 件", pending)
	// pending の課題は、あなたの回答を待っているものの節に出ない。
	answers := text[strings.Index(text, "あなたの回答を待っているもの:"):]
	if end := strings.Index(answers, "スーパーバイザーが確認中"); end > 0 {
		answers = answers[:end]
	}
	if strings.Contains(answers, pending) {
		t.Errorf("確認中の課題 %s が、あなたの回答を待っているものに出ている: %q", pending, answers)
	}
}

func TestStatus_NoSupervisorCheckingIsZeroAndEmptyArray_AC233(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	out := runJSON(t, ws, "status")
	checking := out["supervisor_checking"].(map[string]any)
	if cs, ok := checking["challenges"].([]any); !ok || len(cs) != 0 || checking["count"] != float64(0) {
		t.Errorf("supervisor_checking = %#v, want empty challenges and count 0", checking)
	}
	requireInOrder(t, runText(t, ws, "status"), "スーパーバイザーが確認中: 0 件")
}

func TestShowText_EscalatedHoldShowsFindingsAndRecommendation_AC241(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	esc := svHoldChallenge(t, ws, "escalated", "照合で成果物が見つからなかった", "再検証より差し戻しが良い")
	requireInOrder(t, runText(t, ws, "show", esc), "保留:", "止まり方: verify_uncertain", "スーパーバイザーの状態: escalated",
		"調べたこと: 照合で成果物が見つからなかった", "推奨: 再検証より差し戻しが良い")

	pend := svHoldChallenge(t, ws, "pending", "途中の所見", "途中の推奨")
	if out := runText(t, ws, "show", pend); strings.Contains(out, "調べたこと:") || strings.Contains(out, "推奨:") {
		t.Errorf("上げていない（pending）保留の findings・recommendation が出ている: %q", out)
	}
}

func TestShowJSON_HoldCarriesStopKindStateFindingsRecommendation_AC242(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	esc := svHoldChallenge(t, ws, "escalated", "調べたこと", "推奨")
	h := anySlice(t, runJSON(t, ws, "show", esc)["holds"], "holds")[0].(map[string]any)
	want := map[string]any{"stop_kind": "verify_uncertain", "supervisor_state": "escalated", "supervisor_findings": "調べたこと", "supervisor_recommendation": "推奨"}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("hold[%s] = %#v, want %#v", k, h[k], v)
		}
	}
}

func TestShowJSON_SupervisorActionsOldestFirstAndEmptyArray_AC229_AC231(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	none := createForCase(t, ws)
	if v, ok := runJSON(t, ws, "show", none)["supervisor_actions"].([]any); !ok || len(v) != 0 {
		t.Errorf("対処の記録が無い課題の supervisor_actions = %#v, want []", runJSON(t, ws, "show", none)["supervisor_actions"])
	}

	id := createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	cycle := coretest.InsertCycle(t, ws, "manual", 0, svRaisedAt)
	coretest.InsertSupervisorAction(t, ws, cycle, n, "verify_uncertain", "reverify", "executed", "一回目", "2026-11-22T00:01:00.000Z")
	coretest.InsertSupervisorAction(t, ws, cycle, n, "verify_uncertain", "defer", "executed", "二回目", "2026-11-22T00:02:00.000Z")
	acts := anySlice(t, runJSON(t, ws, "show", id)["supervisor_actions"], "supervisor_actions")
	if len(acts) != 2 || acts[0].(map[string]any)["findings"] != "一回目" || acts[1].(map[string]any)["findings"] != "二回目" {
		t.Errorf("supervisor_actions = %#v, want 古い順の 2 件", acts)
	}
	first := acts[0].(map[string]any)
	if first["hand"] != "reverify" || first["result"] != "executed" || first["stop_kind"] != "verify_uncertain" {
		t.Errorf("supervisor_actions[0] = %#v", first)
	}
	requireInOrder(t, runText(t, ws, "show", id), "スーパーバイザーの対処: 2 件", "手: reverify", "手: defer")
}

func TestShowJSON_HandsTakenCountsExecutedActionsPerHand_AC243(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.SetChallengeStatus(t, ws, n, "awaiting_completion_approval")
	cycle := coretest.InsertCycle(t, ws, "manual", 0, svRaisedAt)
	for i, c := range []struct{ hand, result string }{{"reverify", "executed"}, {"reverify", "executed"}, {"defer", "executed"}, {"reverify", "failed"}} {
		coretest.InsertSupervisorAction(t, ws, cycle, n, "verify_uncertain", c.hand, c.result, "f", "2026-11-22T00:0"+string(rune('1'+i))+":00.000Z")
	}
	got := runJSON(t, ws, "show", id)["supervisor_hands_taken"]
	if !reflect.DeepEqual(got, map[string]any{"reverify": float64(2), "defer": float64(1)}) {
		t.Errorf("supervisor_hands_taken = %#v, want reverify 2・defer 1（failed は入らない）", got)
	}
	requireInOrder(t, runText(t, ws, "show", id), "スーパーバイザーが打った手: reverify: 2・defer: 1")
}

// 同じ課題に pending と escalated（または NULL）の保留が両方開いているときは、オーナーの回答を待つ保留があるので、オーナー待ちのまま。
func TestStatus_PendingMixedWithEscalatedOrNullStaysOwnerWaiting_AC237_AC238(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	var ids []string
	for _, other := range []string{"escalated", ""} {
		id := svHoldChallenge(t, ws, "pending", "", "")
		kind := "verify_uncertain"
		if other == "" {
			kind = ""
		}
		coretest.InsertSupervisorHold(t, ws, challengeIDToInternalID(t, id), "もう 1 件", "verifying", svRaisedAt, kind, other, "f", "r")
		ids = append(ids, id)
	}
	out := runJSON(t, ws, "status")
	if got := challengeIDsOf(t, out["needs_human"].(map[string]any)["challenges"], "needs_human.challenges"); !reflect.DeepEqual(got, ids) {
		t.Errorf("needs_human.challenges = %v, want %v", got, ids)
	}
	if cs := out["supervisor_checking"].(map[string]any)["challenges"].([]any); len(cs) != 0 {
		t.Errorf("supervisor_checking.challenges = %v, want []", cs)
	}
}
