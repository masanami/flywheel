package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

func rel(s string) []string { return []string{"--relayed", "--session", "s", "--quote", s} }

func rejectedFieldsOfLog(t *testing.T, ws string) []any {
	t.Helper()
	afters := activityAfters(t, ws, "reject")
	if len(afters) != 1 {
		t.Fatalf("reject logs = %d, want 1: %s", len(afters), relayedLog(t, ws))
	}
	v, ok := afters[0]["rejected_fields"]
	if !ok || v == nil {
		return nil
	}
	return v.([]any)
}

func TestParseArgs_RepeatableFlagCollectsInOrder(t *testing.T) {
	cmd := Command{Path: []string{"x"}, Flags: []flagDef{{Name: "field", HasValue: true, Repeatable: true}, {Name: "reason", HasValue: true}}, MaxPositional: 1}
	got, err := parseArgs([]string{"--field", "b", "--reason=r", "--field=a", "id"}, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Multi["field"], []string{"b", "a"}) {
		t.Errorf("Multi = %v", got.Multi["field"])
	}
	if got.Values["reason"] != "r" {
		t.Errorf("reason = %q", got.Values["reason"])
	}
	if _, ok := got.Multi["reason"]; ok {
		t.Errorf("non-repeatable flag must not be in Multi")
	}
}

func TestRejectField_DedupAndClosedSetOrder(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	args := append([]string{"reject", id, "--reason", "no", "--field", "figure", "--field", "issues", "--field", "figure"}, rel("q")...)
	runJSON(t, ws, args...)
	if got := rejectedFieldsOfLog(t, ws); !reflect.DeepEqual(got, []any{"issues", "figure"}) {
		t.Errorf("rejected_fields = %v", got)
	}
	shown, _ := json.Marshal(runJSON(t, ws, "show", id))
	if !strings.Contains(string(shown), `"rejected_fields":["issues","figure"]`) {
		t.Errorf("show lacks rejected_fields: %s", shown)
	}
}

func TestRejectField_NoFieldIsNull(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	runJSON(t, ws, append([]string{"reject", id, "--reason", "no"}, rel("q")...)...)
	if got := rejectedFieldsOfLog(t, ws); got != nil {
		t.Errorf("rejected_fields = %v, want nil", got)
	}
}

func TestRejectField_OutsideClosedSetWritesNothing(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	// 端末の無い経路（本人確認が成立しない）でも validation_failed になる＝本人確認の前に拒む。
	requireErrorCode(t, []string{"reject", id, "--reason", "no", "--field", "title", "--workspace", ws}, 1, CodeValidationFailed)
	if got := workStatus(t, ws, id); got != "awaiting_plan_approval" {
		t.Errorf("status = %q, want unchanged", got)
	}
	if n := len(activityAfters(t, ws, "reject")); n != 0 {
		t.Errorf("reject logs = %d, want 0", n)
	}
}

func TestRejectField_OnOperationIsUsageError(t *testing.T) {
	ws := initializedWorkspace(t)
	args := append([]string{"reject", "OP-1", "--reason", "no", "--field", "issues", "--workspace", ws}, rel("q")...)
	requireErrorCode(t, args, 2, CodeUsageError)
	if n := len(activityAfters(t, ws, "reject")); n != 0 {
		t.Errorf("reject logs = %d, want 0", n)
	}
}

func TestApprovalSummaryText_PlanStages(t *testing.T) {
	ans := "A案"
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindPlan, PlanVersion: 1, PlanBody: "b",
		IssuesAnswer: &ans}
	p.Issues = []core.StageIssue{{Question: "どちらにする？", Options: []core.StageOption{{Label: "甲案"}, {Label: "乙案"}}, Recommended: 2}}
	p.Expected = []core.StageExpected{{Scene: "一覧", Before: "空", After: "3件"}}
	p.NonGoals = []string{"認証は触らない"}
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
	for _, want := range []string{"どちらにする？", "2) 乙案", "推奨: 2", "A案", "一覧", "空", "3件", "認証は触らない"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q: %s", want, got)
		}
	}
}

func TestApprovalSummaryText_CompletionPairs(t *testing.T) {
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindCompletion, DoneCriteria: "d"}
	p.Expected = []core.StageExpected{{Scene: "s1", After: "予想1"}, {Scene: "s2", After: "予想2"}}
	p.Actual = []core.StageActual{{Scene: "s1", After: "実物1"}}
	p.Pairs = core.Pairs(p.Expected, p.Actual)
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
	for _, want := range []string{
		"  1. 予想: 予想1\n     実物: 実物1\n",
		"  2. 予想: 予想2\n     実物: " + core.PairActualMissingMark + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q: %s", want, got)
		}
	}
	p.Expected, p.Actual = p.Expected[:1], []core.StageActual{{After: "x"}, {After: "余り"}}
	p.Pairs = core.Pairs(p.Expected, p.Actual)
	if got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil); !strings.Contains(got, "  2. 予想: "+core.PairExpectedMissingMark+"\n     実物: 余り\n") {
		t.Errorf("summary lacks 予想なし row: %s", got)
	}
}

func TestApprovalSummaryText_NoStagesHasNoHeadings(t *testing.T) {
	for _, k := range []core.ApprovalKind{core.ApprovalKindPlan, core.ApprovalKindCompletion} {
		p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: k, PlanVersion: 1, PlanBody: "b", DoneCriteria: "d", Pairs: core.Pairs(nil, nil)}
		got := approvalSummaryText(p, core.ApprovalDecisionApproved, false, nil)
		var want string
		if k == core.ApprovalKindPlan {
			want = "課題 ID:     C-1\nタイトル:    t\n承認の種類:  計画\n計画 v1:\nb\n"
		} else {
			want = "課題 ID:     C-1\nタイトル:    t\n承認の種類:  完了\n完了条件:    d\n" +
				"同時に承認される本番反映 (release): (無し)\n同時には承認されない不可逆操作:     (無し)\n"
		}
		if got != want {
			t.Errorf("%s summary = %q, want %q", k, got, want)
		}
	}
}
