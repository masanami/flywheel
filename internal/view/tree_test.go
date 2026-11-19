package view_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルのテスト名の AC-<n> は、core の見直しの区切り S2c の課題（masanami/flywheel#802）の受入基準の番号。

// AC-74（#802）: 画面と共有する値の集合に、表示の区分の 6 つの値がある。
func TestClosedSets_TaskPhaseCodes_AC74_802(t *testing.T) {
	for _, cs := range view.ClosedSets() {
		if cs.Name != "TASK_PHASE_CODES" {
			continue
		}
		want := []string{"running", "waiting", "next", "blocked", "done", "cancelled"}
		if !reflect.DeepEqual(cs.Codes, want) {
			t.Errorf("TASK_PHASE_CODES = %v, want %v", cs.Codes, want)
		}
		return
	}
	t.Fatal("TASK_PHASE_CODES が ClosedSets に無い")
}

// AC-67（#802）: `flywheel approve <ID> --json` の形（view.ChallengeApprovalResponse）は見直し前と同じで、summary・budget_usd を足していない。
// summary・budget_usd は API の承認の要約（view.ApprovalPlan）だけが持つ。
func TestChallengeApprovalResponse_HasNoSummaryOrBudget_AC67_802(t *testing.T) {
	got := marshal(t, view.ChallengeApprovalResponse{Approval: view.FromApproval(core.Approval{}), Challenge: view.FromChallenge(core.Challenge{})})
	for _, bad := range []string{`"summary"`, `"budget_usd"`} {
		if strings.Contains(got, bad) {
			t.Errorf("approve --json の形に %s がある: %s", bad, got)
		}
	}
	if !strings.HasPrefix(got, `{"approval":{`) || !strings.Contains(got, `,"challenge":{`) {
		t.Errorf("approve --json のキーが見直し前と違う: %s", got)
	}
	if plan := marshal(t, view.ApprovalPlan{}); !strings.Contains(plan, `"summary"`) || !strings.Contains(plan, `"budget_usd"`) {
		t.Errorf("API の承認の要約の plan には summary・budget_usd がある: %s", plan)
	}
}
