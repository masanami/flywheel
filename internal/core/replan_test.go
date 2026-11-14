package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// このファイルは、保留への回答の「範囲が変わる」印（T12 の IfReplan。docs/features/m1-core.md §状態機械）を、
// 実ストア（t.TempDir() の SQLite）・偽の判断／委譲の IF で検証する。

func (f *delegateFixture) answer(t *testing.T, id, answer string, replan bool) (*Challenge, *Hold, error) {
	t.Helper()
	ctx := context.Background()
	att := relayedAttestation(t, id, "sess", "quote")
	preview, err := f.s.PrepareAnswer(ctx, id, answer)
	if err != nil {
		t.Fatalf("PrepareAnswer: %v", err)
	}
	return f.s.ExecuteAnswer(ctx, AnswerRequest{ChallengeID: id, ExpectedVersion: preview.Version, Answer: answer, Replan: replan}, att)
}

// questionHold は着手中の課題を委譲して問いの保留（成果物〔変更の提案・作業ブランチ〕・費用つき）にする。
func (f *delegateFixture) questionHold(t *testing.T) string {
	t.Helper()
	id := f.newInProgress(t, "t", "P1", planSpec(func(m map[string]any) { m["size"] = "S" }))
	f.deleg.result = questionsOut("feat/x")
	f.runDelegation(t, id)
	if got := f.detail(t, id).Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	// 子が作った変更の提案を成果物として足す（問いの run に付く）。
	if err := f.exec(`INSERT INTO run_artifact (run_id, kind, ref, state) VALUES ((SELECT MAX(id) FROM run WHERE kind = 'delegate'), 'pr', 'https://github.com/o/r/pull/7', 'open')`); err != nil {
		t.Fatal(err)
	}
	if err := f.exec(`INSERT INTO run_artifact (run_id, kind, ref) VALUES ((SELECT MAX(id) FROM run WHERE kind = 'delegate'), 'branch', 'feat/x')`); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *delegateFixture) exec(q string, args ...any) error {
	return f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	})
}

// 印の付いた回答は、委譲に戻らず分類済になり、保留に印が残る。
func TestAnswerReplan_GoesBackToClassifiedAndMarksTheHold(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.questionHold(t)

	c, hold, err := f.answer(t, id, "NEW-DIRECTION", true)
	if err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
	if c.Status != StatusClassified {
		t.Errorf("status = %s, want classified", c.Status)
	}
	if !hold.Replan {
		t.Errorf("hold.Replan = false, want true")
	}
	d := f.detail(t, id)
	if len(d.Holds) == 0 || !d.Holds[len(d.Holds)-1].Replan {
		t.Errorf("stored hold is not marked: %+v", d.Holds)
	}
	if got := f.queryString(t, `SELECT after FROM activity WHERE action = 'answer' ORDER BY id DESC LIMIT 1`); !strings.Contains(got, `"replan":true`) {
		t.Errorf("activity after = %s, want replan:true", got)
	}
}

// 印の無い回答の動きは今までどおり（保留の直前の状態へ戻る）。
func TestAnswerReplan_WithoutTheMarkReturnsToThePrecedingStatus(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.questionHold(t)

	c, hold, err := f.answer(t, id, "JUST-ANSWER", false)
	if err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
	if c.Status != StatusInProgress || hold.Replan {
		t.Errorf("status = %s replan = %v, want in_progress/false", c.Status, hold.Replan)
	}
}

// 保留の直前の状態が着手中・検証中でなければ、印は成立せず、何も書かない。
func TestAnswerReplan_RejectedWhenTheHoldWasNotRaisedFromWork(t *testing.T) {
	f := newDelegateFixture(t)
	ctx := context.Background()
	ch, err := f.s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "x", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ClassifyChallenge(ctx, ChannelCLI, ch.ID, ClassifyInput{Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	q := "Q"
	if _, err := f.s.HoldChallenge(ctx, ChannelCLI, ch.ID, HoldInput{Question: q}); err != nil {
		t.Fatalf("HoldChallenge: %v", err)
	}
	before := f.detail(t, ch.ID)

	_, _, err = f.answer(t, ch.ID, "A", true)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	after := f.detail(t, ch.ID)
	if after.Status != before.Status || after.Version != before.Version || after.Holds[0].Answer != nil {
		t.Errorf("state changed after a rejected replan answer: %+v", after)
	}
}

// 作り直しの入力に回答の文と前の計画と使った費用が入り、承認の後の委譲は前の成果物を引き継ぎ、費用は積み上がる。
func TestAnswerReplan_J2InputCarriesAnswerAndPlan_DelegationCarriesTheDeliverableAndSpend(t *testing.T) {
	f := newDelegateFixture(t)
	ctx := context.Background()
	id := f.questionHold(t)
	if _, _, err := f.answer(t, id, "NEW-DIRECTION-ANSWER", true); err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}

	// 計画を作る判断点（J2）が、分類済へ戻った課題を対象にし、入力に回答の文と前の計画を入れる。
	f.agent = newAgentDeclForJ1Test(t, f.s, "POSITION")
	var j2In JudgmentLaunchInput
	j2 := succeededInvoker(j2PlanOutput(func(m map[string]any) {
		m["repo"], m["operation"], m["size"] = "main-repo", "impl", "S"
		m["budget_impl_usd"], m["budget_review_usd"] = 6, 1
	}))
	j2.onInvoke = func(in JudgmentLaunchInput) { j2In = in }
	res, err := f.s.PlanAutoJ2(ctx, J2AutoInput{ChallengeID: &id, AgentDecl: f.agent, ConnDecl: f.conn, Invoker: j2, Upstream: f.upstream, CycleID: f.cycle(t, 300)})
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	_ = res
	text := allSectionsText(j2In.Sections)
	// 前の計画の版までに使った費用（問いの run の 2 USD）と、新しい予算はこの額を含む上限として書く旨が入る。
	for _, want := range []string{labelJ2Replan, "NEW-DIRECTION-ANSWER", "PLAN-BODY-t",
		"前の計画の版までに使った費用（新しい計画の枠から引かれる積み上げの額）: 実装枠 2 USD", "この額を含む上限として書く"} {
		if !strings.Contains(text, want) {
			t.Errorf("J2 input lacks %q:\n%s", want, text)
		}
	}
	if got := f.detail(t, id).Status; got != StatusAwaitingPlanApproval {
		t.Fatalf("status after J2 = %s, want awaiting_plan_approval (no automatic approval)", got)
	}
	if n := f.queryString(t, `SELECT COUNT(*) FROM task_plan WHERE challenge_id = 1`); n != "2" {
		t.Errorf("plan versions = %s, want 2", n)
	}

	// 計画の承認（初回と同じゲート）の後の委譲。
	c, err := f.s.GetChallenge(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runApproval(t, f.s, &c.Challenge, ApprovalDecisionApproved, verifiedAttestationForTest(t, id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	launchesBefore := len(f.deleg.launched())
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: reportJSON(nil), ReportedTotalCostUSD: float64Ptr(1)}
	f.j3Inputs = nil
	f.runDelegation(t, id)

	if len(f.deleg.launched()) != launchesBefore+1 {
		t.Fatalf("delegation was not launched after the new plan was approved")
	}
	if l := f.lastLaunch(t); l.ResumeKind != "" {
		t.Errorf("the new plan must start a new session, got resume kind %q", l.ResumeKind)
	}
	if len(f.j3Inputs) == 0 {
		t.Fatal("J3 was not invoked")
	}
	j3 := allSectionsText(f.j3Inputs[len(f.j3Inputs)-1].Sections)
	// 成果物の行は、固定のラベルではなく成果物の種類の値をそのまま出す。
	for _, want := range []string{labelPreviousDeliverable, "pr: https://github.com/o/r/pull/7", "branch: feat/x"} {
		if !strings.Contains(j3, want) {
			t.Errorf("J3 input lacks %q:\n%s", want, j3)
		}
	}

	// 使った費用（問いの run の 2 USD ＋ 今回の 1 USD）は新しい計画の枠から引かれる（積み上げ）。
	cid, _ := parseChallengeID(id)
	var sp bucketSpend
	if err := f.s.db.Read(ctx, func(tx *sql.Tx) error {
		var e error
		sp, e = loadBucketSpend(ctx, tx, cid, 2)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if sp.Impl < usdToMicros(3) {
		t.Errorf("accumulated impl spend = %d, want >= %d (spend of the first plan version must carry over)", sp.Impl, usdToMicros(3))
	}
}
