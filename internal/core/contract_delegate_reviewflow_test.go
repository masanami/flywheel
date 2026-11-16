package core

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

// このファイルは form: cli の接続ツールの run が成果物つきのレビュー待ち（waiting・requested_action.kind = review）に
// なったときの流れを、実ストア・偽の ContractClient で固定する: (1) レビュー待ちが検証中へ進む、(2) J5 の not_met の差し戻しが
// resume で返る、(3) 本番反映の後に ready を返して接続ツールの run が終わる、(4) T18 の戻し・(d) T15 の差し戻しの理由が respond で 1 回だけ返る。

const reviewPRURL = "https://github.com/o/r/pull/1"

// reviewConnectorsJSON は resume に選択肢と添え書きを別の差し込みで渡す宣言。
var reviewConnectorsJSON = strings.Replace(contractConnectorsJSON,
	`"--answer", "{answer}"`, `"--choice", "{choice}", "--note", "{note}"`, 1)

func reviewWaitOut() ContractLaunchOutput {
	return contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち", Options: []string{"respond", "ready"}}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}}
	})
}

type reviewFlowFixture struct {
	*contractFixture
	j5 *j5Fixture
}

func newReviewFlowFixture(t *testing.T) *reviewFlowFixture {
	t.Helper()
	cf := newContractFixtureWith(t, reviewConnectorsJSON)
	j := &j5Fixture{delegateFixture: cf.delegateFixture, checks: newFakeChecks(), j5: &fakeJudgmentInvoker{result: j5Out("not_met", "FEEDBACK-TEXT", nil)}}
	j.checks.prs["o/r#1"] = prChecks("open", checkDone)
	return &reviewFlowFixture{contractFixture: cf, j5: j}
}

// toVerifying は契約の run を始め、レビュー待ちを読んで課題を検証中へ進める。
func (f *reviewFlowFixture) toVerifying(t *testing.T) {
	t.Helper()
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
}

// (1) waiting・review・成果物ありは、run の終わりを待たずに成果物を取り込み、課題を検証中へ進める。
func TestPollContractRuns_ReviewWaitWithArtifactMovesTheChallengeToVerifyingAndKeepsTheRunOpen(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.toVerifying(t)
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact WHERE kind = 'pr' AND ref = '`+reviewPRURL+`'`); n != 1 {
		t.Fatalf("pr artifacts = %d, want 1", n)
	}
	if state, _, _, ended := f.contractRow(t); state != "waiting" || ended {
		t.Fatalf("contract_run state=%s ended=%v, want waiting and open（本番反映まで開いておく）", state, ended)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 0 {
		t.Fatalf("holds = %d, want 0（レビュー待ちは保留にしない）", n)
	}
	// 検証中の間は、成果物を取り込んだ読みの run を新しい読みで押し流さないよう、状態を読み直さない。
	before := len(f.client.statuses)
	f.poll(t)
	if len(f.client.statuses) != before || len(f.client.resumes) != 0 {
		t.Fatalf("status calls %d→%d, resumes %d, want no call while verifying", before, len(f.client.statuses), len(f.client.resumes))
	}
}

// レビュー待ちでも成果物が無ければ検証できないので、他の求めと同じくオーナーの保留へ写す。
func TestPollContractRuns_ReviewWaitWithoutArtifactIsHeldForTheOwner(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち"}
	})}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
}

// 成果物が pr・branch・commit のどれでもないとき（other だけ）は検証中へ進めない。
func TestPollContractRuns_ReviewWaitWithOnlyOtherArtifactIsNotSubmitted(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち"}
		st.Artifacts = []ContractArtifact{{Kind: "other", Ref: "x"}}
	})}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
}

// (2) J5 が not_met なら、同じ契約の run へ resume（選択肢 respond・添え書きに差し戻しの理由）を返し、課題は着手中のまま。
// 次のレビュー待ちで、また検証中へ進む。
func TestPollContractRuns_NotMetIsReturnedByResumeAndTheNextReviewWaitVerifiesAgain(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.toVerifying(t)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status after not_met = %s, want in_progress", got)
	}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t)
	want := []string{"tool", "resume", "ext-1", "--choice", "respond", "--note", "FEEDBACK-TEXT"}
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], want) {
		t.Fatalf("resumes = %v, want %v", f.client.resumes, want)
	}
	// 同じ差し戻しを二重に返さない。
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
	f.poll(t)
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1（差し戻しは 1 回だけ返す）", len(f.client.resumes))
	}
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status after the next review wait = %s, want verifying", got)
	}
}

// 差し込んだ値の中の波括弧は、後の差し込みとして読まない。
func TestFillContractArgs_ValuesAreNotReSubstituted(t *testing.T) {
	t.Parallel()
	got, err := fillContractArgs([]string{"{choice}|{note}"}, "C-1", nil, "ext", contractAnswer{Choice: "respond", Note: "x {answer} {choice}"})
	if err != nil || got[0] != "respond|x {answer} {choice}" {
		t.Fatalf("got %v err %v", got, err)
	}
}

// 差し戻しが上限に達したら、resume を返さず課題を保留にする。
func TestPollContractRuns_NotMetAtTheReworkLimitHoldsInsteadOfResuming(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.agent.ReworkLimit = 1
	f.toVerifying(t)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none at the rework limit", f.client.resumes)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
}

// (3) 本番反映（release が executed）の後に ready を返し、接続ツールの run が succeeded で終わる。
func TestPollContractRuns_AfterTheReleaseIsExecutedReadyIsReturnedAndTheRunEnds(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.toVerifying(t)
	// 本番反映の前は何も返さない。
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none before the release", f.client.resumes)
	}
	insertReleaseForTest(t, f.s, f.cid, reviewPRURL, OperationStateExecuted, "merge-sha")
	// 課題がまだ検証中の間は、release が executed でも ready を返さない（J5 と完了の承認の前に終わらせない）。
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none while the challenge is not done", f.client.resumes)
	}
	n, _ := parseChallengeID(f.cid)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, string(StatusDone), n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.client.resumeOut = contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) {
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}}
	})
	f.poll(t)
	want := []string{"tool", "resume", "ext-1", "--choice", "ready", "--note", ""}
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], want) {
		t.Fatalf("resumes = %v, want %v", f.client.resumes, want)
	}
	if state, _, _, ended := f.contractRow(t); state != "succeeded" || !ended {
		t.Fatalf("contract_run state=%s ended=%v, want succeeded and ended", state, ended)
	}
	f.poll(t)
	if len(f.client.resumes) != 1 || len(f.client.statuses) != 1 {
		t.Fatalf("resumes=%d statuses=%d after the run ended, want no more calls", len(f.client.resumes), len(f.client.statuses))
	}
}

// reopenAfterRelease は検証中の課題を本物の経路で T18 まで進める: release を足し、J5 の met → 完了の承認（D12 で release も承認）
// → ReopenChallengeForRelease（not_mergeable）で着手中へ戻す。戻した記録が契約の run の開始より新しくなるよう時計を進める。
func (f *reviewFlowFixture) reopenAfterRelease(t *testing.T, advance func()) {
	t.Helper()
	ctx := context.Background()
	ref := reviewPRURL
	rel, err := f.s.CreateOperation(ctx, ChannelCLI, OperationInput{ChallengeID: f.cid, Kind: "release", Summary: "PR 1 を本番へ", Ref: &ref})
	if err != nil {
		t.Fatalf("op add: %v", err)
	}
	f.j5.j5.result = j5Out("met", nil, nil)
	if _, err := f.s.VerifyAutoJ5(ctx, f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status after J5 met = %s, want awaiting_completion_approval", got)
	}
	prev, err := f.s.PrepareApproval(ctx, f.cid)
	if err != nil || len(prev.PendingReleases) != 1 {
		t.Fatalf("PrepareApproval = %+v err %v, want one pending release", prev, err)
	}
	head := "abc123"
	if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{
		ChallengeID: f.cid, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		ReleaseHeads: []ReleaseHead{{Operation: prev.PendingReleases[0], HeadSHA: &head}},
	}, verifiedAttestationForTest(t, f.cid)); err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
	if got := f.status(t, f.cid); got != StatusDone {
		t.Fatalf("status after the completion approval = %s, want done", got)
	}
	// 完了の後・本番反映の前（release は承認済みで未実行）は ready を返さない。
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none before the release is executed", f.client.resumes)
	}
	advance()
	res, err := reopenExecPath(ctx, f.s, ChannelCLI, ReopenForReleaseInput{OperationID: rel.ID, Reason: ReopenReasonNotMergeable})
	if err != nil || !res.Reopened {
		t.Fatalf("ReopenChallengeForRelease = %+v err %v, want reopened", res, err)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status after the reopen = %s, want in_progress", got)
	}
}

// (4) 本番反映でマージできず T18 で着手中へ戻った後は、同じ契約の run へ respond と理由を 1 回だけ返す（ready は返さない）。
// 課題は着手中のまま。resume が同じレビュー待ちを返しても取り込み直さず、次の周で状態を読み直してレビュー待ちなら検証中へ進む。
func TestPollContractRuns_ReopenedForReleaseIsReturnedByRespondOnceAndVerifiesAgain(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	cur := time.Now()
	f.s.now = func() time.Time { return cur }
	f.toVerifying(t)
	f.reopenAfterRelease(t, func() { cur = cur.Add(time.Hour) })
	cur = cur.Add(time.Hour)
	f.client.resumeOut = reviewWaitOut()
	f.poll(t)
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0][:6], []string{"tool", "resume", "ext-1", "--choice", "respond", "--note"}) ||
		!strings.Contains(f.client.resumes[0][6], "PR "+reviewPRURL) || !strings.Contains(f.client.resumes[0][6], "既定ブランチへ追従") {
		t.Fatalf("resumes = %v, want one respond with the reopen reason and the PR", f.client.resumes)
	}
	// resume の直後は着手中のまま（resume が返した同じレビュー待ちは取り込み直さない）。
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status right after the respond = %s, want in_progress", got)
	}
	// 次の周: 戻しはもう返さず、状態を読み直してレビュー待ちなら検証中へ進む。
	cur = cur.Add(time.Hour)
	statuses := len(f.client.statuses)
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1（同じ戻しを二重に返さない）", len(f.client.resumes))
	}
	if len(f.client.statuses) != statuses+1 {
		t.Fatalf("status calls %d→%d, want one read after the respond", statuses, len(f.client.statuses))
	}
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status on the first review wait after the respond = %s, want verifying", got)
	}
}

// (c) T18 の記録が無い着手中の課題は、成果物つきのレビュー待ちを読んでも resume を呼ばず、検証中へ進む。
func TestPollContractRuns_InProgressWithoutReopenDoesNotResumeAndVerifies(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("precondition: status = %s, want in_progress", got)
	}
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none without a reopen record", f.client.resumes)
	}
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
}

// rejectCompletion は検証中の課題を本物の経路で T15 まで進める: J5 の met → 完了確認待ち → 人が理由つきで差し戻す。
func (f *reviewFlowFixture) rejectCompletion(t *testing.T, reason string) {
	t.Helper()
	ctx := context.Background()
	f.j5.j5.result = j5Out("met", nil, nil)
	if _, err := f.s.VerifyAutoJ5(ctx, f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status after J5 met = %s, want awaiting_completion_approval", got)
	}
	prev, err := f.s.PrepareApproval(ctx, f.cid)
	if err != nil {
		t.Fatalf("PrepareApproval: %v", err)
	}
	if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{
		ChallengeID: f.cid, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: &reason,
	}, verifiedAttestationForTest(t, f.cid)); err != nil {
		t.Fatalf("ExecuteApproval(reject): %v", err)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status after the T15 = %s, want in_progress", got)
	}
}

// (d) 人が完了の承認を差し戻した（T15）後は、同じ契約の run へ respond と差し戻しの理由を 1 回だけ返す。
// 課題は着手中のまま。resume が同じレビュー待ちを返しても取り込み直さず、次のレビュー待ちで再び検証中へ進む。
func TestPollContractRuns_CompletionRejectionIsReturnedByRespondOnceAndVerifiesAgain(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	cur := time.Now()
	f.s.now = func() time.Time { return cur }
	f.toVerifying(t)
	cur = cur.Add(time.Hour)
	f.rejectCompletion(t, "REJECT-REASON-TEXT")
	cur = cur.Add(time.Hour)
	f.client.resumeOut = reviewWaitOut()
	f.poll(t)
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0][:6], []string{"tool", "resume", "ext-1", "--choice", "respond", "--note"}) ||
		!strings.Contains(f.client.resumes[0][6], "REJECT-REASON-TEXT") {
		t.Fatalf("resumes = %v, want one respond with the rejection reason", f.client.resumes)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status right after the respond = %s, want in_progress", got)
	}
	cur = cur.Add(time.Hour)
	statuses := len(f.client.statuses)
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1（同じ差し戻しを二重に返さない）", len(f.client.resumes))
	}
	if len(f.client.statuses) != statuses+1 {
		t.Fatalf("status calls %d→%d, want one read after the respond", statuses, len(f.client.statuses))
	}
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status on the next review wait = %s, want verifying", got)
	}
}

// (e) 計画の承認での差し戻し（kind=plan）の記録は、着手中の課題でも respond で返さない。
func TestPollContractRuns_PlanRejectionIsNotReturnedByRespond(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	cur := time.Now().Add(time.Hour)
	f.s.now = func() time.Time { return cur }
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, plan_version, actor, channel, verification, reason, decided_at)
		 SELECT id, 'plan', 'rejected', version, 1, 'owner', 'cli', 'tty', 'PLAN-REJECT', ? FROM challenge WHERE id = ?`,
			formatTimestamp(cur), f.intID(t))
		return err
	})
	if err != nil {
		t.Fatalf("insert plan rejection: %v", err)
	}
	cur = cur.Add(time.Hour)
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none for a plan rejection", f.client.resumes)
	}
}

// (d2) 未配達の完了の差し戻しが 2 件たまっていたら、1 回の respond の添え書きへ両方の理由を古い順にまとめて返す。
// その後の周では返さない（resume は増えない）。
func TestPollContractRuns_StackedCompletionRejectionsAreReturnedOldestFirstInOneRespond(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	cur := time.Now()
	f.s.now = func() time.Time { return cur }
	f.toVerifying(t)
	cur = cur.Add(time.Hour)
	f.rejectCompletion(t, "REJECT-FIRST")
	cur = cur.Add(time.Hour)
	// 2 件目の差し戻し（kind=completion）を、契約のコマンドを呼ぶ前に積む。
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, plan_version, actor, channel, verification, reason, decided_at)
		 SELECT id, 'completion', 'rejected', version, 1, 'owner', 'cli', 'tty', 'REJECT-SECOND', ? FROM challenge WHERE id = ?`,
			formatTimestamp(cur), f.intID(t))
		return err
	})
	if err != nil {
		t.Fatalf("insert second completion rejection: %v", err)
	}
	cur = cur.Add(time.Hour)
	f.client.resumeOut = reviewWaitOut()
	f.poll(t)
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0][:6], []string{"tool", "resume", "ext-1", "--choice", "respond", "--note"}) {
		t.Fatalf("resumes = %v, want one respond", f.client.resumes)
	}
	note := f.client.resumes[0][6]
	first, second := strings.Index(note, "REJECT-FIRST"), strings.Index(note, "REJECT-SECOND")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("note = %q, want both reasons oldest first", note)
	}
	cur = cur.Add(time.Hour)
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1（まとめて返した差し戻しを二重に返さない）", len(f.client.resumes))
	}
}

// intID は課題の ID（"C-<n>"）の数値部分（SQL で challenge.id と比べるため）。
func (f *reviewFlowFixture) intID(t *testing.T) int64 {
	t.Helper()
	n, ok := parseChallengeID(f.cid)
	if !ok {
		t.Fatalf("parseChallengeID(%q) failed", f.cid)
	}
	return n
}
