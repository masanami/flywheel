package core

import (
	"context"
	"strings"
	"testing"
)

// このファイルは、課題が保留の間に契約の run（form: cli）が succeeded まで進んだ場合の扱い
// （docs/features/m3-invoker-delegation.md §委譲の流れ（`form: cli`）の「保留の間に成功した契約の run」）を、
// 実ストア・偽の ContractClient で固定する。

func succeededWithPR() ContractLaunchOutput {
	return contractOut(ContractStateSucceeded, float64Ptr(1), func(st *ContractStatus) {
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/9"}}
	})
}

func (f *contractFixture) heldSuccessFlag(t *testing.T) int64 {
	t.Helper()
	return f.count(t, `SELECT COALESCE(SUM(held_success), 0) FROM contract_run`)
}

// 答えの後の周（RunDelegation）で、start も resume も呼ばず検証中へ進む。
func (f *contractFixture) delegationCycle(t *testing.T) {
	t.Helper()
	in := f.input(f.cycle(t, 300), nil)
	in.Contract = f.client
	if _, err := f.s.RunDelegation(context.Background(), in); err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
}

func (f *contractFixture) assertAdvancedWithoutCalls(t *testing.T) {
	t.Helper()
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %d, want 1（2 本目の委譲を起動しない）", len(f.client.starts))
	}
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none（答えは resume に使わない）", f.client.resumes)
	}
	if got := f.heldSuccessFlag(t); got != 0 {
		t.Fatalf("held_success = %d, want 0 after advancing", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact WHERE kind = 'pr'`); n != 1 {
		t.Fatalf("pr artifacts = %d, want the PR imported once", n)
	}
}

// 判断待ちの保留の間に succeeded になった課題は、答えの前は保留のまま（周を重ねても）、答えの後の周で検証中へ進む。
// 答えは resume に使わず保留に残り、完了の検証（J5）の材料に入り、成果物の PR は J5 の「直前の委譲の run」から見える。
func TestHeldSuccess_WaitingHoldAnsweredAfterSuccessAdvancesWithoutStartOrResume(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil), succeededWithPR()}
	f.poll(t)
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human until the owner answers", got)
	}
	if got := f.heldSuccessFlag(t); got != 1 {
		t.Fatalf("held_success = %d, want 1", got)
	}
	f.delegationCycle(t)
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want still awaiting_human before the answer", got)
	}

	f.answerLatestHold(t, f.cid, "ANSWER-LATE")
	f.delegationCycle(t)
	f.assertAdvancedWithoutCalls(t)

	ch := f.detail(t, f.cid).Challenge
	vc, err := f.s.loadJ5Context(context.Background(), ch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatHoldsSection(vc.Holds), "ANSWER-LATE") {
		t.Fatalf("J5 holds = %q, want the answer", formatHoldsSection(vc.Holds))
	}
	if len(vc.Artifacts) != 1 || vc.Artifacts[0].Ref != "https://github.com/o/r/pull/9" {
		t.Fatalf("J5 artifacts = %+v, want the imported PR via the last delegation run", vc.Artifacts)
	}
}

// 予算超過の中止に失敗した保留（別の理由の保留）の間に succeeded になった課題も同じ。
func TestHeldSuccess_CancelFailureHoldAdvancesAfterAnswer(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "exit 1"}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, float64Ptr(6), nil), succeededWithPR()}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want held after the failed cancel", got)
	}
	f.poll(t)
	f.answerLatestHold(t, f.cid, "止めた")
	f.delegationCycle(t)
	f.assertAdvancedWithoutCalls(t)
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %d, want only the first", len(f.client.cancels))
	}
}

// resume が失敗した保留の間に succeeded になった課題も同じ。
func TestHeldSuccess_ResumeFailureHoldAdvancesAfterAnswer(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil)}
	f.poll(t)
	f.answerLatestHold(t, f.cid, "A1")
	f.client.resumeOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want held after the failed resume", got)
	}
	f.client.queue = []ContractLaunchOutput{succeededWithPR()}
	f.poll(t)
	f.answerLatestHold(t, f.cid, "A2")
	f.delegationCycle(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	if len(f.client.starts) != 1 || len(f.client.resumes) != 1 {
		t.Fatalf("starts = %d resumes = %d, want 1 and 1（失敗した resume のみ）", len(f.client.starts), len(f.client.resumes))
	}
	if got := f.heldSuccessFlag(t); got != 0 {
		t.Fatalf("held_success = %d, want 0 after advancing", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact WHERE kind = 'pr'`); n != 1 {
		t.Fatalf("pr artifacts = %d, want 1", n)
	}
}

// 保留の間に failed になった課題は、この規則の対象ではない（印を付けず、検証中へ進めない）。
func TestHeldSuccess_FailedDuringHoldIsNotAdvanced(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil), contractOut(ContractStateFailed, nil, nil)}
	f.poll(t)
	f.poll(t)
	if got := f.heldSuccessFlag(t); got != 0 {
		t.Fatalf("held_success = %d, want 0 for failed", got)
	}
	f.answerLatestHold(t, f.cid, "A")
	f.poll(t)
	if got := f.status(t, f.cid); got == StatusVerifying {
		t.Fatalf("status = %s, want not verifying for a failed run", got)
	}
}

// 着手中のまま succeeded を読む通常の経路は、印を付けずその場で検証中へ進める（従来どおり）。
func TestHeldSuccess_SucceededWhileInProgressDoesNotMark(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{succeededWithPR()}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	if got := f.heldSuccessFlag(t); got != 0 {
		t.Fatalf("held_success = %d, want 0", got)
	}
}
