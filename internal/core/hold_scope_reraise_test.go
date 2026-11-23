package core

import (
	"context"
	"strconv"
	"testing"
)

// answerOnlyOpenHold は、開いている保留が 1 つの課題の、その保留に答える（課題は戻り先〔着手中〕へ戻る）。
func (f *holdScopeFixture) answerOnlyOpenHold(t *testing.T) {
	t.Helper()
	att := verifiedAttestationForTest(t, f.id)
	c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: f.version(t), Answer: "これで"}, att)
	if err != nil || c.Status != StatusInProgress {
		t.Fatalf("ExecuteAnswer = %v, %v, want in_progress", c, err)
	}
}

// タスク a を止める保留が開いている間に、a についてもう 1 つ止まりが起きても、保留は増えない（AC-43）。
// その保留が閉じた後の回りでは、a について待っていた止まりが、改めてタスクを止める保留になる（AC-44）。
func TestHoldScope_AC43_AC44_StopOfTheSameTaskIsRaisedAgainAfterItsHoldIsClosed(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	v := f.version(t)
	held, err := f.s.holdContractWait(context.Background(), f.runA, "もう一度、どうしますか")
	if err != nil || held {
		t.Fatalf("second stop of the held task: held = %v, err = %v, want not held and no error", held, err)
	}
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds = %s, want 1", got)
	}
	if got := f.version(t); got != v {
		t.Errorf("version = %d, want %d (unchanged)", got, v)
	}

	f.answerOnlyOpenHold(t)
	if got := f.openHolds(t); got != "0" {
		t.Fatalf("open holds after the answer = %s, want 0", got)
	}
	f.waitHold(t, f.runA)
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds after the stop is raised again = %s, want 1", got)
	}
	if got := f.q(t, `SELECT COALESCE(scope, '') || '/' || task_id FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`, f.cid); got != "task/"+strconv.FormatInt(f.taskA, 10) {
		t.Errorf("the hold raised again = %s, want task/%d", got, f.taskA)
	}
	if got := f.challengeStatus(t); got != string(StatusAwaitingHuman) {
		t.Errorf("status = %s, want awaiting_human", got)
	}
}

// 課題を止める保留が開いている間に、タスク b の止まりが起きても、保留は立たず（AC-45）、課題を止める保留に読み替えた行も増えない（AC-46）。
// 課題を止める保留が閉じて着手中へ戻った後の回りでは、b について待っていた止まりが、タスクを止める保留になる（AC-47）。
func TestHoldScope_AC45_AC46_AC47_TaskStopWaitsForTheChallengeHoldAndIsRaisedAfterIt(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, f.id, HoldInput{Question: "課題を止める"}); err != nil {
		t.Fatal(err)
	}
	v := f.version(t)
	held, err := f.s.holdContractWait(context.Background(), f.runB, "どうしますか")
	if err != nil || held {
		t.Fatalf("task stop under a challenge hold: held = %v, err = %v, want not held and no error", held, err)
	}
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds = %s, want 1", got)
	}
	if got := f.q(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ? AND scope IS NULL`, f.cid); got != "1" {
		t.Errorf("rows with scope NULL = %s, want 1 (only the challenge hold)", got)
	}
	if got := f.version(t); got != v {
		t.Errorf("version = %d, want %d (unchanged)", got, v)
	}

	f.answerOnlyOpenHold(t)
	f.waitHold(t, f.runB)
	if got := f.q(t, `SELECT COALESCE(scope, '') || '/' || task_id FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`, f.cid); got != "task/"+strconv.FormatInt(f.taskB, 10) {
		t.Errorf("the hold raised after the challenge hold = %s, want task/%d", got, f.taskB)
	}
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds = %s, want 1", got)
	}
}
