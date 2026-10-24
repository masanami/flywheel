package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは M4.5 S2（#326）の、cycle の段と課題の依存の関係（AC-178〜AC-181）を、実ストア・
// 偽の claude・偽の gh で検証する。本物の claude・gh は起動しない。

// newUnmetDependencyTarget は依存先として満たされない（完了していない）課題を作る。どの段の対象にもならない
// 状態（完了の承認待ち）に置き、J1・J2・J5 の対象が依存元だけになるようにする。
func newUnmetDependencyTarget(t *testing.T, ws string) string {
	t.Helper()
	return createWithStatusForCase(t, ws, "awaiting_completion_approval")
}

// AC-179: 依存先が満たされていない課題も、cycle の分類（J1）の段で分類され、結果が記録される。
func TestCycle_UnmetDependencyDoesNotStopTheClassifyPhase(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := createTitled(t, ws, "dependent")
	dep := newUnmetDependencyTarget(t, ws)
	runJSON(t, ws, "dep", "add", id, dep)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, "")

	doc := cycleDoc(t, ws)

	cls := cyclePhase(t, doc, "classify")
	items := phaseItems(t, cls)
	if len(items) != 1 || items[0]["challenge_id"] != id || items[0]["outcome"] != "mine" || items[0]["status"] != "classified" {
		t.Fatalf("classify items = %v, want %s mine -> classified", items, id)
	}
	if ns := phaseNotStarted(t, cls); len(ns) != 0 {
		t.Errorf("classify not_started = %v, want none", ns)
	}
	if got := challengeStatusOf(t, ws, dep); got != "awaiting_completion_approval" {
		t.Errorf("dependency target status = %s, want unchanged", got)
	}
}

// AC-180: 依存先が満たされていない課題も、cycle の計画（J2）の段で計画され、結果が記録される。
func TestCycle_UnmetDependencyDoesNotStopThePlanPhase(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := createTitled(t, ws, "dependent")
	classifyManually(t, ws, id, "P1")
	dep := newUnmetDependencyTarget(t, ws)
	runJSON(t, ws, "dep", "add", id, dep)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j2RoutePlan(t)}, "")

	doc := cycleDoc(t, ws)

	plan := cyclePhase(t, doc, "plan")
	items := phaseItems(t, plan)
	if len(items) != 1 || items[0]["challenge_id"] != id || items[0]["outcome"] != "plan" || items[0]["status"] != "awaiting_plan_approval" {
		t.Fatalf("plan items = %v, want %s planned -> awaiting_plan_approval", items, id)
	}
	if ns := phaseNotStarted(t, plan); len(ns) != 0 {
		t.Errorf("plan not_started = %v, want none", ns)
	}
}

// AC-181: 依存先が満たされていない課題も、cycle の検証（J5）の段で検証され、結果が記録される。
func TestCycle_UnmetDependencyDoesNotStopTheVerifyPhase(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newVerifyingForCase(t, ws, true)
	dep := newUnmetDependencyTarget(t, ws)
	runJSON(t, ws, "dep", "add", id, dep)
	withFakeGHRoutesOnPATH(t, ghChecksRoutes("open", checkRunsComplete))
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j5Route("met", nil, nil)}, "")

	doc := cycleDoc(t, ws)

	verify := cyclePhase(t, doc, "verify")
	items := phaseItems(t, verify)
	if verify["skipped"] != false || len(items) != 1 || items[0]["challenge_id"] != id || items[0]["outcome"] != "met" || items[0]["status"] != "awaiting_completion_approval" {
		t.Fatalf("verify phase = %v, want %s met -> awaiting_completion_approval", verify, id)
	}
	if ns := phaseNotStarted(t, verify); len(ns) != 0 {
		t.Errorf("verify not_started = %v, want none", ns)
	}
}

// AC-178: 依存先（完了）の release が承認済みで未実行のとき、1 回の cycle の release の段でそれが実行済みに
// なり、同じ周の run の段で依存元の委譲が起動される（段の順 release → run に依存することを cycle で固定する）。
func TestCycle_ReleaseExecutedInTheSameCycleUnblocksTheRunPhase(t *testing.T) {
	ws := setupRunWorkspace(t)
	// 委譲の宣言に、release の PR の o/r を足す。
	writeConnectorsJSONForTest(t, ws, strings.Replace(runConnectorsFixture, `"repos": [`,
		`"repos": [
    {"name": "r-repo", "remote": "o/r", "default_branch": "main", "connector": "harness"},`, 1))
	id := newInProgressForRun(t, ws, nil)
	dep := createWithStatusForCase(t, ws, "done")
	coretest.InsertApprovedRelease(t, ws, challengeIDToInternalID(t, dep), releasePRURL(1), execHead, "2026-10-22T00:00:00.000Z")
	runJSON(t, ws, "dep", "add", id, dep)
	gh := newReleaseGH(t, execHead)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j3Route("BRIEF-DEP"), delegateRoute("completed")}, "")

	// release の段より前は、依存先の release が承認済みで未実行なので、依存元は止まっている（M45H12）。
	before := phaseOf(t, runJSON(t, ws, "run"))
	if ns, _ := before["not_started"].([]any); len(ns) != 1 ||
		ns[0].(map[string]any)["reason"] != "dependency" || ns[0].(map[string]any)["detail"] != dep {
		t.Fatalf("before the release phase: not_started = %v, want %s dependency %s", before["not_started"], id, dep)
	}
	if got := delegateRunsOf(t, ws); len(got) != 0 {
		t.Fatalf("before the release phase: delegate runs = %v, want none", got)
	}

	doc := cycleDoc(t, ws)

	rel := cyclePhase(t, doc, "release")
	ritems := phaseItems(t, rel)
	if len(ritems) != 1 || ritems[0]["challenge_id"] != dep || ritems[0]["result"] != "merged" {
		t.Fatalf("release items = %v, want the dependency's release merged", ritems)
	}
	if len(gh.mergeCalls(t)) != 1 {
		t.Errorf("merge calls = %v, want 1", gh.mergeCalls(t))
	}
	run := cyclePhase(t, doc, "run")
	if ns := phaseNotStarted(t, run); len(ns) != 0 {
		t.Errorf("run not_started = %v, want none (the dependency became satisfied in this cycle)", ns)
	}
	items := phaseItems(t, run)
	if len(items) != 1 || items[0]["challenge_id"] != id {
		t.Errorf("run items = %v, want the delegation of %s", items, id)
	}
	if got := delegateRunsOf(t, ws); len(got) != 1 {
		t.Errorf("delegate runs = %v, want one", got)
	}
}

// J2 が出した depends_on は、承認の後の委譲の判定で依存元を止める（一続きの確認）。
func TestCycle_J2DependsOnStopsTheDelegationAfterPlanApproval(t *testing.T) {
	ws := setupRunWorkspace(t)
	id := createTitled(t, ws, "dependent")
	classifyManually(t, ws, id, "P1")
	dep := newUnmetDependencyTarget(t, ws)
	stdout, _ := j2PlanFixture(t, func(m map[string]any) { m["depends_on"] = []string{dep} })
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{
		{Match: fakeClaudeJ2Match, Tag: "J2", Stdout: stdout},
		j3Route("BRIEF-DEP"), delegateRoute("completed"),
	}, "")

	// 1 周目: J2 が計画と依存を登録する。計画は未承認なので委譲の段は何も起動しない。
	doc := cycleDoc(t, ws)
	plan := cyclePhase(t, doc, "plan")
	if items := phaseItems(t, plan); len(items) != 1 || items[0]["challenge_id"] != id || items[0]["status"] != "awaiting_plan_approval" {
		t.Fatalf("plan items = %v, want %s planned", items, id)
	}
	deps, _ := runJSON(t, ws, "show", id)["challenge"].(map[string]any)["dependencies"].([]any)
	if len(deps) != 1 || deps[0].(map[string]any)["challenge_id"] != dep {
		t.Fatalf("dependencies after J2 = %v, want [%s]", deps, dep)
	}

	// 計画を承認し、次の周の委譲の段が依存で止まる。
	runJSON(t, ws, "approve", id, "--relayed", "--session", "s", "--quote", "q")
	doc = cycleDoc(t, ws)
	ns := phaseNotStarted(t, cyclePhase(t, doc, "run"))
	if len(ns) != 1 || ns[0]["challenge_id"] != id || ns[0]["reason"] != "dependency" || ns[0]["detail"] != dep {
		t.Fatalf("run not_started = %v, want %s dependency %s", ns, id, dep)
	}
	if got := delegateRunsOf(t, ws); len(got) != 0 {
		t.Errorf("delegate runs = %v, want none", got)
	}
}
