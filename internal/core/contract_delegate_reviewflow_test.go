package core

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

// このファイルは form: cli の接続ツールの run が成果物つきのレビュー待ち（waiting・requested_action.kind = review）に
// なったときの流れを、実ストア・偽の ContractClient で固定する: (1) レビュー待ちが検証中へ進む、(2) J5 の not_met の差し戻しが
// resume で返る、(3) 本番反映の後に ready を返して接続ツールの run が終わる。

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
	got, err := fillContractArgs([]string{"{choice}|{note}"}, "C-1", nil, "ext", contractAnswer{Choice: "respond", Note: "x {answer} {choice}"})
	if err != nil || got[0] != "respond|x {answer} {choice}" {
		t.Fatalf("got %v err %v", got, err)
	}
}

// 差し戻しが上限に達したら、resume を返さず課題を保留にする。
func TestPollContractRuns_NotMetAtTheReworkLimitHoldsInsteadOfResuming(t *testing.T) {
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
