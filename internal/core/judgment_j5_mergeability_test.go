package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

// conflictingPR は衝突している（mergeable=false・dirty）open な PR。
func conflictingPR() UpstreamPullRequestChecks {
	pr := prChecks("open", checkDone)
	pr.Mergeable, pr.MergeableState = boolPtr(false), "dirty"
	return pr
}

// 偽の GitHub の応答（CONFLICTING）から、区画への記載・J5 の判定（met）・課題が承認待ちに入らないところまでを通す。
func TestVerifyAutoJ5_ConflictingPR_SectionIsWrittenAndMetIsOverriddenAndTheChallengeDoesNotAwaitApproval(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.checks.prs["o/r#1"] = conflictingPR()
	f.j5.result = j5Out("met", nil, nil) // J5 が見落として met を返しても
	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(res, err)
	}
	if it := res.Items[0]; it.Outcome != "not_met" || it.Status == nil || *it.Status != string(StatusInProgress) {
		t.Errorf("item = %+v, want not_met / in_progress", it)
	}
	if got := f.detail(t, "C-1").Status; got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress (not awaiting_completion_approval)", got)
	}
	in := f.stdin(t)
	for _, want := range []string{labelPullRequestMergeability, "mergeable=CONFLICTING / mergeStateStatus=DIRTY"} {
		if !strings.Contains(in, want) {
			t.Errorf("J5 input lacks %q", want)
		}
	}
	if got := f.holdCount(t); got != "0" {
		t.Errorf("holds = %s, want none (no question to a human)", got)
	}
	// 追従の委譲が読む最新の J5 の出力は not_met で、指摘に衝突の解消が書かれている。
	runs := f.j5Runs(t)
	v, ok := validateJ5Output([]byte(runs[len(runs)-1].Output))
	if !ok || v.Verdict != J5VerdictNotMet || v.Feedback == nil || !strings.Contains(*v.Feedback, "衝突を解消") || !strings.Contains(*v.Feedback, j5PRURL) {
		t.Errorf("stored J5 output = %q", runs[len(runs)-1].Output)
	}
	if got := f.queryString(t, `SELECT COUNT(*) FROM occurrence WHERE challenge_id = 1 AND kind = 'verification_not_met'`); got != "1" {
		t.Errorf("verification_not_met occurrences = %s, want 1", got)
	}
}

// BEHIND（衝突ではなく base に遅れているだけ）は止めない。
func TestVerifyAutoJ5_BehindPR_IsNotTreatedAsConflict(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	pr := prChecks("open", checkDone)
	pr.Mergeable, pr.MergeableState = boolPtr(true), "behind"
	f.checks.prs["o/r#1"] = pr
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval", got)
	}
	if !strings.Contains(f.stdin(t), "mergeable=MERGEABLE / mergeStateStatus=BEHIND") {
		t.Errorf("J5 input lacks the BEHIND mergeability")
	}
	if f.approvalPreview(t, "C-1").MergeabilityUnconfirmed {
		t.Errorf("a known mergeability must not be flagged unconfirmed")
	}
}

// seqChecks は最初の n 回は mergeable=null を返し、その後に after を返す偽の取得。
type seqChecks struct {
	mu    sync.Mutex
	nulls int
	after UpstreamPullRequestChecks
	calls int
}

func (c *seqChecks) GetPullRequestChecks(context.Context, string, int) (UpstreamPullRequestChecks, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	pr := c.after
	if c.calls <= c.nulls {
		pr.Mergeable, pr.MergeableState = nil, ""
	}
	return pr, nil
}

// UNKNOWN（計算中）は取り直し、確定した結果（衝突）で判定する。
func TestVerifyAutoJ5_UnknownMergeabilityIsRetriedAndTheSettledConflictBlocksApproval(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	sc := &seqChecks{nulls: 2, after: conflictingPR()}
	in := f.input(f.cycle(t, 300), nil)
	in.Checks, in.MergeableRetries, in.MergeableInterval = sc, 3, time.Millisecond
	if _, err := f.s.VerifyAutoJ5(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if sc.calls != 3 {
		t.Errorf("GetPullRequestChecks calls = %d, want 3 (1 + 2 retries until settled)", sc.calls)
	}
	if got := f.detail(t, "C-1").Status; got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress", got)
	}
}

// なお不明のまま: 承認待ちへは進め、承認の要約に「マージ可否は未確認」と出す。
func TestVerifyAutoJ5_StillUnknownAfterRetries_AwaitsApprovalAndTheSummaryNotesItIsUnconfirmed(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	sc := &seqChecks{nulls: 100, after: prChecks("open", checkDone)}
	in := f.input(f.cycle(t, 300), nil)
	in.Checks, in.MergeableRetries, in.MergeableInterval = sc, 2, time.Millisecond
	if _, err := f.s.VerifyAutoJ5(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if sc.calls != 3 {
		t.Errorf("calls = %d, want 3", sc.calls)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval", got)
	}
	if !f.approvalPreview(t, "C-1").MergeabilityUnconfirmed {
		t.Errorf("MergeabilityUnconfirmed = false, want true")
	}
	if !strings.Contains(f.stdin(t), "マージ可否は未確認") {
		t.Errorf("J5 input should say the mergeability is unconfirmed")
	}
}
