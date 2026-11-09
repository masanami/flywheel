package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// #485 の通し: 完了の承認まで済んだ PR が本番反映でマージできなくなる → 課題が着手中へ戻る → 追従の委譲（J3 の入力に
// 戻した理由の区画が入る）→ J5 が新しい head を検証 → 完了の承認（前の承認と結ばれる）→ 本番反映でマージされる。

func TestRecovery_NotMergeableToMergedRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.setPRHead("head-1")
	ref := j5PRURL
	rel, err := f.s.CreateOperation(ctx, ChannelCLI, OperationInput{ChallengeID: "C-1", Kind: "release", Summary: "PR 1 を本番へ", Ref: &ref})
	if err != nil {
		t.Fatalf("op add: %v", err)
	}
	if _, err := f.verify(t, nil); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got := f.status(t); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status after J5 = %s, want awaiting_completion_approval", got)
	}
	approveCompletion := func(head string) {
		t.Helper()
		prev, err := f.s.PrepareApproval(ctx, "C-1")
		if err != nil {
			t.Fatalf("PrepareApproval: %v", err)
		}
		if len(prev.PendingReleases) != 1 {
			t.Fatalf("pending releases = %+v, want 1", prev.PendingReleases)
		}
		h := head
		if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{
			ChallengeID: "C-1", ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
			ReleaseHeads: []ReleaseHead{{Operation: prev.PendingReleases[0], HeadSHA: &h}},
		}, verifiedAttestationForTest(t, "C-1")); err != nil {
			t.Fatalf("ExecuteApproval: %v", err)
		}
	}
	approveCompletion("head-1")
	if got := f.status(t); got != StatusDone {
		t.Fatalf("status after the completion approval = %s, want done", got)
	}

	// 本番反映の段: 同じファイルを触る別の PR が先にマージされ、この PR は既定ブランチへマージできない。
	no := false
	relPR := &fakeRelease{pr: UpstreamPullRequestChecks{URL: relRef, State: "open", Base: "main", HeadSHA: "head-1", Mergeable: &no}}
	opts := ReleaseExecOptions{Checks: relPR, Merger: relPR, Connectors: relConnectors(""), Agent: &AgentDeclaration{FailureLimit: 3}, Trigger: ReleaseExecTriggerCycle}
	res, err := f.s.ExecuteReleases(ctx, nil, opts)
	if err != nil || len(res.Items) != 1 || !res.Items[0].Reopened {
		t.Fatalf("release stage = %+v err = %v, want not_mergeable with the challenge reopened", res, err)
	}
	newOp := res.Items[0].NewOperationID
	if got := f.status(t); got != StatusInProgress {
		t.Fatalf("status after the reopen = %s, want in_progress", got)
	}
	d := f.detail(t, "C-1")
	var oldOp, nw *IrreversibleOperation
	for i := range d.Operations {
		switch d.Operations[i].ID {
		case rel.ID:
			oldOp = &d.Operations[i]
		case newOp:
			nw = &d.Operations[i]
		}
	}
	if oldOp == nil || nw == nil || oldOp.State != OperationStateClosed || oldOp.ClosedReason == nil || *oldOp.ClosedReason != ClosedReasonSuperseded ||
		oldOp.SupersededByOperationID == nil || *oldOp.SupersededByOperationID != newOp || nw.State != OperationStatePending || nw.SupersedesOperationID == nil || *nw.SupersedesOperationID != rel.ID {
		t.Fatalf("operations = %+v, want the old release closed/superseded and a new pending one linked to it", d.Operations)
	}
	var priorApproval *Approval
	for i := range d.Approvals {
		if d.Approvals[i].Kind == ApprovalKindCompletion {
			priorApproval = &d.Approvals[i]
		}
	}
	if priorApproval == nil || priorApproval.SupersededAt == nil || priorApproval.SupersededReason == nil || *priorApproval.SupersededReason != string(ReopenReasonNotMergeable) || priorApproval.SupersededBy != nil {
		t.Fatalf("previous completion approval = %+v, want superseded and not yet linked", priorApproval)
	}

	// 追従の委譲: J3 の入力に、戻した理由の区画が入る。
	before := len(f.j3Inputs)
	f.runDelegation(t, "C-1")
	if len(f.j3Inputs) != before+1 {
		t.Fatalf("J3 calls = %d, want %d", len(f.j3Inputs), before+1)
	}
	sec, ok := sectionByLabel(f.j3Inputs[before].Sections, "本番反映のやり直し")
	if !ok {
		t.Fatalf("J3 input lacks the reopen section:\n%s", sectionsText(f.j3Inputs[before].Sections))
	}
	for _, want := range []string{j5PRURL, "マージできなくなった", "main", "新しい PR を作らない"} {
		if !strings.Contains(sec.Content, want) {
			t.Errorf("reopen section lacks %q:\n%s", want, sec.Content)
		}
	}
	if got := f.status(t); got != StatusVerifying {
		t.Fatalf("status after the follow-up delegation = %s, want verifying", got)
	}

	// J5 は新しい head を検証し、検証の head の記録は新しい head になる。
	f.setPRHead("head-2")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatalf("verify (2nd): %v", err)
	}
	if got := f.status(t); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status after the 2nd J5 = %s, want awaiting_completion_approval", got)
	}
	if got := f.queryString(t, `SELECT verified_head_sha FROM run_artifact WHERE verified_head_sha IS NOT NULL ORDER BY id DESC LIMIT 1`); got != "head-2" {
		t.Errorf("latest verified head = %q, want head-2", got)
	}
	if got := f.queryString(t, `SELECT COUNT(*) FROM run_artifact WHERE verified_head_sha IS NOT NULL`); got != "2" {
		t.Errorf("verified heads recorded = %s, want 2 (one per verification)", got)
	}
	// 戻した記録は、最後の委譲より古くなったので、次の委譲の入力には入らない。
	if note := f.reopenNoteForTest(t, "C-1"); note != nil {
		t.Errorf("reopen note after the follow-up delegation = %+v, want none", note)
	}
	// 古い head を承認の入力にすると断る（検証の head は新しい head になっている）。
	prev, err := f.s.PrepareApproval(ctx, "C-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(prev.PendingReleases) != 1 || prev.PendingReleases[0].ID != newOp || prev.VerifiedHeadSHAs[newOp] != "head-2" {
		t.Fatalf("preview = pending %+v verified %v, want only the new release verified at head-2", prev.PendingReleases, prev.VerifiedHeadSHAs)
	}
	stale := "head-1"
	if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{
		ChallengeID: "C-1", ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		ReleaseHeads: []ReleaseHead{{Operation: prev.PendingReleases[0], HeadSHA: &stale}},
	}, verifiedAttestationForTest(t, "C-1")); err == nil {
		t.Fatal("approving the old head succeeded, want a conflict")
	}
	approveCompletion("head-2")

	d = f.detail(t, "C-1")
	if d.Status != StatusDone {
		t.Fatalf("status after the 2nd completion approval = %s, want done", d.Status)
	}
	var completions []Approval
	for _, a := range d.Approvals {
		if a.Kind == ApprovalKindCompletion {
			completions = append(completions, a)
		}
	}
	if len(completions) != 2 || completions[0].SupersededBy == nil || *completions[0].SupersededBy != completions[1].ID || completions[1].SupersededAt != nil {
		t.Fatalf("completion approvals = %+v, want the first linked to the second, which is effective", completions)
	}

	// 本番反映: 今度はマージできる。
	yes := true
	relPR.pr = UpstreamPullRequestChecks{URL: relRef, State: "open", Base: "main", HeadSHA: "head-2", Mergeable: &yes}
	relPR.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: "head-2", MergeCommitSHA: "mc"}
	res, err = f.s.ExecuteReleases(ctx, nil, opts)
	if err != nil || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMerged || res.Items[0].OperationID != newOp {
		t.Fatalf("2nd release stage = %+v err = %v, want %s merged", res, err, newOp)
	}
	if len(relPR.mergeArgs) != 1 || relPR.mergeArgs[0][2] != "head-2" {
		t.Errorf("merge args = %v, want one request pinned to head-2", relPR.mergeArgs)
	}
	d = f.detail(t, "C-1")
	if d.Status != StatusDone {
		t.Errorf("status = %s, want done", d.Status)
	}
}

func (f *j5Fixture) reopenNoteForTest(t *testing.T, id string) *reopenNote {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var note *reopenNote
	readForTest(t, f.s, func(tx *sql.Tx) error {
		var err error
		note, err = loadReopenNote(context.Background(), tx, cid)
		return err
	})
	return note
}
