package core

import (
	"context"
	"fmt"
	"testing"
)

// このファイルは #108（親要件チケット #98 §一括の操作（サイクル）の S2）の、cycle の委譲（run）と
// 検証（verify）の段を、実ストア・偽の判断／委譲の IF・偽の SlotGit・偽の上流で検証する
// （AC-353〜358 の core 側）。

// cycleS2Invoker は J3 にブリーフ、J5 に判定 verdict を返す偽の判断の IF（J1・J2 の対象は無い）。
func cycleS2Invoker(verdict string, calls *[]JudgmentPoint) JudgmentInvoker {
	return &cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		*calls = append(*calls, c.Judgment)
		switch c.Judgment {
		case JudgmentJ3:
			return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: briefJSON("BRIEF"), ReportedTotalCostUSD: float64Ptr(0.4)}
		case JudgmentJ5:
			return j5Out(verdict, nil, nil)
		}
		return JudgmentLaunchOutput{Result: RunResultErrored}
	}}
}

func (f *j5Fixture) cycleInput(inv JudgmentInvoker) CycleRunInput {
	return CycleRunInput{
		Trigger: "manual", AgentDecl: f.agent, ConnDecl: f.conn, Invoker: inv, Upstream: f.upstream,
		Delegate: f.deleg, Git: f.git, Reconcile: f.branches, Checks: f.checks,
	}
}

// 着手中の課題は、同じ周の委譲の段で委譲され、同じ周の検証の段で J5 の対象になる。J5 の最初の起動は、
// その周の委譲の run がすべて終わってからである（run の started_at と ended_at で検証する）。
func TestRunCycle_DelegatesThenVerifiesInTheSameCycle(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.branches.branches["feat/x"] = true
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "develop")}
	f.checks.prs["o/r#1"] = prChecks("open", checkDone)
	var calls []JudgmentPoint

	res, err := f.s.RunCycle(context.Background(), f.cycleInput(cycleS2Invoker("met", &calls)))
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	if got := fmt.Sprint(phaseNames(res)); got != "[ingest classify plan run verify]" {
		t.Fatalf("phases = %s", got)
	}
	run := phaseOfResult(t, res, CyclePhaseRun)
	if run.Skipped || len(run.Items) != 1 || run.Items[0].Outcome != "completed" || run.Items[0].Status == nil || *run.Items[0].Status != string(StatusVerifying) {
		t.Fatalf("run phase = %+v, want 1 item (outcome completed, status verifying)", run)
	}
	verify := phaseOfResult(t, res, CyclePhaseVerify)
	if verify.Skipped || len(verify.Items) != 1 || verify.Items[0].Outcome != "met" {
		t.Fatalf("verify phase = %+v, want J5 on the challenge that became verifying in the run phase", verify)
	}
	if got := fmt.Sprint(calls); got != "[J3 J5]" {
		t.Errorf("judgment calls = %s, want [J3 J5]", got)
	}

	delegates := f.delegateRuns(t)
	j5s := f.j5Runs(t)
	if len(delegates) != 1 || len(j5s) != 1 {
		t.Fatalf("delegate runs = %d, J5 runs = %d, want 1 each", len(delegates), len(j5s))
	}
	if delegates[0].EndedAt == nil || j5s[0].StartedAt.Before(*delegates[0].EndedAt) {
		t.Errorf("J5 started at %v before the delegation ended at %v", j5s[0].StartedAt, delegates[0].EndedAt)
	}
	if res.Cycle.Result != CycleResultCompleted {
		t.Errorf("cycle result = %s", res.Cycle.Result)
	}
}

// PR のチェックが完了していなければ、検証の段は J5 を起動せず、not_started に waiting_external で示す。
func TestRunCycle_VerifyPhase_PendingChecksAreWaitingExternal(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.branches.branches["feat/x"] = true
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "develop")}
	f.checks.prs["o/r#1"] = prChecks("open", checkDone, checkRunning)
	var calls []JudgmentPoint

	res, err := f.s.RunCycle(context.Background(), f.cycleInput(cycleS2Invoker("met", &calls)))
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	verify := phaseOfResult(t, res, CyclePhaseVerify)
	if len(verify.Items) != 0 || len(verify.NotStarted) != 1 || verify.NotStarted[0].Reason != NotStartedWaitingExternal {
		t.Fatalf("verify phase = %+v, want waiting_external only", verify)
	}
	if got := fmt.Sprint(calls); got != "[J3]" {
		t.Errorf("judgment calls = %s, want [J3] (J5 must not start while checks are pending)", got)
	}
	if got := f.detail(t, "C-1").Status; got != StatusVerifying {
		t.Errorf("status = %s, want verifying", got)
	}
}

// 接続ツールの宣言が無い周は、委譲と検証の段を skipped にし（J3・J5 を起動せず）、エラーにしない。
func TestRunCycle_NoConnectors_RunAndVerifyPhasesAreSkipped(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	var calls []JudgmentPoint
	in := f.cycleInput(cycleS2Invoker("met", &calls))
	in.ConnDecl, in.Upstream, in.Delegate, in.Git, in.Reconcile, in.Checks = nil, nil, nil, nil, nil, nil

	res, err := f.s.RunCycle(context.Background(), in)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	for _, name := range []CyclePhase{CyclePhaseRun, CyclePhaseVerify} {
		p := phaseOfResult(t, res, name)
		if !p.Skipped || len(p.Items) != 0 || len(p.NotStarted) != 0 || len(p.SerialGroups) != 0 {
			t.Errorf("%s phase = %+v, want skipped and empty", name, p)
		}
	}
	if len(calls) != 0 {
		t.Errorf("judgment calls = %v, want none", calls)
	}
	if got := len(f.delegateRuns(t)); got != 0 {
		t.Errorf("delegate runs = %d, want 0", got)
	}
}
