package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// AC-58 #1140: promoted_challenge_id と promoted_task_id の両方に値があるフォローアップの `followup list` の行は、上げた先としてタスクを出す。
func TestFollowupList_AC58_PromotedTaskWinsOverChallenge(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.InsertApprovedPlan(t, ws, n, "PLAN-BODY", taskAdditionPlanSpec)
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: n, Title: "両方", PromotedChallengeID: n, PromotedTaskID: 1})
	out := runText(t, ws, "followup", "list")
	if !strings.Contains(out, "タスク T-1「a」に上げた") {
		t.Errorf("AC-58: line should name the task with its title:\n%s", out)
	}
	if strings.Contains(out, id+" に上げた") {
		t.Errorf("AC-58: line must not name the challenge when the task column has a value:\n%s", out)
	}
}
