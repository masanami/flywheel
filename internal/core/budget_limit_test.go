package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// 予算の上限（区切り S4c-1。AC-23〜37）を、実ストアで確かめる。

func (f *delegateFixture) limitActivities(t *testing.T, id string) []Activity {
	t.Helper()
	var out []Activity
	acts, err := f.s.ListActivities(context.Background(), &id)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Action == ActivityActionBudgetLimit {
			out = append(out, a)
		}
	}
	return out
}

func (f *delegateFixture) relayedAttestation(t *testing.T, id string) Attestation {
	t.Helper()
	att, err := Verify(ChannelCLI, &fakeVerifier{method: VerificationRelayed, actor: "alice", evidence: RelayEvidence{Session: "s", Quote: "上限を上げて"}}, "s", id)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return att
}

// limitRequest は --limit の①と②の入力。
func (f *delegateFixture) limitRequest(t *testing.T, id string, limit float64) BudgetRequest {
	t.Helper()
	prev, err := f.s.PrepareBudgetLimit(context.Background(), id, f.agent, limit)
	if err != nil {
		t.Fatalf("PrepareBudgetLimit: %v", err)
	}
	return BudgetRequest{ChallengeID: id, ExpectedVersion: prev.Version, PlanVersion: prev.PlanVersion, LimitUSD: &limit, Agent: f.agent}
}

func (f *delegateFixture) budgetApprovals(t *testing.T, id string) int64 {
	t.Helper()
	cid, _ := parseChallengeID(id)
	return f.count0(t, "SELECT COUNT(*) FROM approval WHERE kind = 'budget' AND task_id IS NULL AND challenge_id = "+fmt.Sprint(cid))
}

// AC-23・35・36・37: 枠の増額で見込みが上限を超えると、同じ書き込みで上限が見込みまで上がり、作業ログに前後の額と方式が残る。
func TestBudgetRaise_RaisesTheLimitToTheEstimateWhenItWouldExceedIt(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5)) // 見込み 14
	if err := f.s.setChallengeBudgetLimitChecked(context.Background(), ChannelCLI, id, 20, "理由", f.agent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.raise(t, id, "T-1", 20, nil); err != nil { // 見込み 20+2+4+5 = 31
		t.Fatal(err)
	}
	b := f.budgetOf(t, id)
	if b.LimitUSD != 31 || b.ImplEstimateUSD+b.ReviewEstimateUSD != 31 || b.LimitReason == nil || *b.LimitReason != "理由" {
		t.Errorf("budget = %+v, want limit 31 (= estimate) with the reason kept", b)
	}
	logged := f.limitActivities(t, id)
	if len(logged) != 2 {
		t.Fatalf("budget_limit activities = %d, want 2 (setup + raise)", len(logged))
	}
	last := logged[len(logged)-1]
	if string(last.Before) != `{"limit_usd":20}` || string(last.After) != `{"limit_usd":31,"verification":"tty_confirm"}` || last.Verification != string(VerificationTTYConfirm) {
		t.Errorf("activity = %+v before=%s after=%s", last, last.Before, last.After)
	}
}

// 上限の記録が無い課題も、見込みが前の見込み（＝上限）を超えれば上限が上がる。
func TestBudgetRaise_WithoutARecordedLimitTheLimitFollowsTheEstimate(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	if _, err := f.raise(t, id, "T-1", 5, nil); err != nil {
		t.Fatal(err)
	}
	if b := f.budgetOf(t, id); b.LimitUSD != 16 {
		t.Errorf("limit = %v, want 16", b.LimitUSD)
	}
}

// AC-24: 見込みが上限以下のままなら、上限は変わらず、上限の作業ログも残らない。
func TestBudgetRaise_KeepsTheLimitWhenTheEstimateStaysWithinIt(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	if err := f.s.setChallengeBudgetLimitChecked(context.Background(), ChannelCLI, id, 20, "", f.agent); err != nil {
		t.Fatal(err)
	}
	n := len(f.limitActivities(t, id))
	if _, err := f.raise(t, id, "T-1", 5, nil); err != nil { // 見込み 16 ≤ 20
		t.Fatal(err)
	}
	if b := f.budgetOf(t, id); b.LimitUSD != 20 {
		t.Errorf("limit = %v, want 20 unchanged", b.LimitUSD)
	}
	if got := len(f.limitActivities(t, id)); got != n {
		t.Errorf("budget_limit activities = %d, want %d (no new entry)", got, n)
	}
}

// AC-25・26・30・31・32〜34: --limit は上限だけを変え、版は変えず、承認の記録 1 行と作業ログを残す（ターミナルでも --relayed でも）。
func TestBudgetLimit_ChangesOnlyTheLimit(t *testing.T) {
	t.Parallel()
	for _, relayed := range []bool{false, true} {
		f := newDelegateFixture(t)
		id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5)) // 見込み 14
		before, _ := f.s.GetChallenge(context.Background(), id)
		att := f.budgetAttestation(t, id)
		want := string(VerificationTTYConfirm)
		if relayed {
			att, want = f.relayedAttestation(t, id), string(VerificationRelayed)
		}
		approval, err := f.s.ExecuteBudget(context.Background(), f.limitRequest(t, id, 25), att)
		if err != nil {
			t.Fatalf("relayed=%v: %v", relayed, err)
		}
		if approval.Kind != ApprovalKindBudget {
			t.Errorf("approval = %+v", approval)
		}
		b := f.budgetOf(t, id)
		if b.LimitUSD != 25 || b.ImplEstimateUSD+b.ReviewEstimateUSD != 14 {
			t.Errorf("relayed=%v: budget = %+v, want limit 25 and estimate 14", relayed, b)
		}
		after, _ := f.s.GetChallenge(context.Background(), id)
		if after.Version != before.Version || after.Status != before.Status {
			t.Errorf("version/status = %d/%s, want %d/%s", after.Version, after.Status, before.Version, before.Status)
		}
		if n := f.budgetApprovals(t, id); n != 1 {
			t.Errorf("budget approvals without a task = %d, want 1", n)
		}
		logged := f.limitActivities(t, id)
		if len(logged) != 1 || string(logged[0].Before) != `{"limit_usd":14}` || logged[0].Verification != want ||
			string(logged[0].After) != `{"limit_usd":25,"verification":"`+want+`"}` {
			t.Errorf("relayed=%v: activities = %+v", relayed, logged)
		}
	}
}

// AC-27: 見込みより小さい額は検証の誤りで断り、上限は変わらない。
func TestBudgetLimit_SmallerThanTheEstimateIsRejected(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	if _, err := f.s.PrepareBudgetLimit(context.Background(), id, f.agent, 13.9); !errors.Is(err, ErrValidation) {
		t.Fatalf("prepare err = %v, want ErrValidation", err)
	}
	req := f.limitRequest(t, id, 20)
	small := 13.9
	req.LimitUSD = &small
	if _, err := f.s.ExecuteBudget(context.Background(), req, f.budgetAttestation(t, id)); !errors.Is(err, ErrValidation) {
		t.Fatalf("execute err = %v, want ErrValidation", err)
	}
	if b := f.budgetOf(t, id); b.LimitUSD != 14 || f.budgetApprovals(t, id) != 0 || len(f.limitActivities(t, id)) != 0 {
		t.Errorf("state changed: %+v", b)
	}
}

// AC-28: 本人確認が無ければ断り、上限は変わらない。
func TestBudgetLimit_WithoutAttestationIsRejected(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	if _, err := f.s.ExecuteBudget(context.Background(), f.limitRequest(t, id, 20), Attestation{}); !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}
	if b := f.budgetOf(t, id); b.LimitUSD != 14 || f.budgetApprovals(t, id) != 0 {
		t.Errorf("state changed: %+v", b)
	}
}

// AC-29: 上限とタスクの枠の指定の同時指定は検証の誤り。
func TestBudgetLimit_CombinedWithATaskFrameIsRejected(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	review := 3.0
	for _, mut := range []func(*BudgetRequest){
		func(r *BudgetRequest) { r.ImplUSD = 5 },
		func(r *BudgetRequest) { r.ReviewUSD = &review },
		func(r *BudgetRequest) { r.TaskID = "T-1" },
	} {
		req := f.limitRequest(t, id, 20)
		mut(&req)
		if _, err := f.s.ExecuteBudget(context.Background(), req, f.budgetAttestation(t, id)); !errors.Is(err, ErrValidation) {
			t.Errorf("err = %v, want ErrValidation", err)
		}
	}
	if b := f.budgetOf(t, id); b.LimitUSD != 14 || f.budgetApprovals(t, id) != 0 {
		t.Errorf("state changed: %+v", b)
	}
}
