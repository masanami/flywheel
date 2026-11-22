package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// 区切り S4c-1 の仕上げ: 印（expand_after）の付いたタスクが済み、判断点が上限以下でタスクを足した課題が、
// 手で回さずに 1 回のサイクルで、足したタスクの起動まで進む（AC-218・AC-82）。実ストアと偽の判断・偽の委譲先で確かめる。

// expandByJudgment は判断点ごとに出力を返す偽の判断の IF（J2 は out、J3 はブリーフ）。呼ばれた判断点を記録する。
type expandByJudgment struct {
	expandRecorder
}

func (r *expandByJudgment) InvokeJudgment(ctx context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	if in.Judgment == JudgmentJ3 {
		r.mu.Lock()
		r.calls = append(r.calls, in)
		r.mu.Unlock()
		return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: briefJSON("BRIEF")}, nil
	}
	return r.expandRecorder.InvokeJudgment(ctx, in)
}

// newExpandCLIFixture は newExpandFixture と同じ課題（a は印つき・b。枠は 5 USD ずつ。上限 14）を、契約の形（form: cli）の
// リポジトリ（統合先を使う課題のタスクは、契約の形の委譲先へだけ起動する）で作る。
func newExpandCLIFixture(t *testing.T) *paFixture {
	t.Helper()
	f := newLimitFixture(t, func(m map[string]any) {
		m["budget_limit_usd"] = 14.0
		m["budget_limit_reason"] = "続きを足す分"
		for _, tk := range m["tasks"].([]any) {
			tm := tk.(map[string]any)
			tm["repo"], tm["operation"] = "cli-repo", "cli-op"
		}
	})
	// 契約の形のリポジトリへスロットを足す（起動できる作業ツリーが要る）。
	decl, err := parseConnectorsDeclaration([]byte(strings.Replace(j2TestConnectorsJSON, `"connector": "clitool"}`, `"connector": "clitool", "slots": {"provider": "clone", "paths": ["slot1"]}}`, 1)))
	if err != nil {
		t.Fatalf("parseConnectorsDeclaration: %v", err)
	}
	if err := decl.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	f.conn = decl
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	return f
}

func expandCycleInput(f *paFixture, inv JudgmentInvoker, del DelegationInvoker) CycleRunInput {
	return CycleRunInput{
		Contract: &fakeContractClient{startOut: contractOut(ContractStateFailed, nil, nil) /* 起動した後は、実行が終わる状態にして、サイクルが待ち続けないようにする */, uniqueRunIDs: true},
		Trigger:  "manual", AgentDecl: f.agent, ConnDecl: f.conn, Invoker: inv, Upstream: f.upstream,
		Delegate: del, Git: newFakeSlotGit(), Reconcile: newFakeBranchSource(), Checks: newFakeChecks(), Diffs: &fakeDiffs{}, HeadGit: &noHeadGit{},
		WorkspaceGit: &fakeWorkspaceGit{},
	}
}

// AC-218・AC-82: 1 回の RunCycle で、足す依頼ができ、判断点が足し、足したタスクが同じサイクルの委譲の段で起動される。
// 済みのタスクの枠が上限の検査へ渡るため、足した後の見込みは、済みの a（5）・b（5）・足した c（3）の 13 になり、上限 14 以下で通る。
func TestExpand_AC218_AC82_OneCycleAddsTheTaskAndStartsIt(t *testing.T) {
	t.Parallel()
	f := newExpandCLIFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	inv := &expandByJudgment{expandRecorder{out: []byte(expandAddOutput(func(ts []map[string]any) {
		ts[0]["depends_on"] = []string{"a"}
		ts[0]["repo"], ts[0]["operation"] = "cli-repo", "cli-op"
	}))}}
	del := &fakeDelegator{}
	if _, err := f.s.RunCycle(context.Background(), expandCycleInput(f, inv, del)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	adds := f.additions(t)
	if len(adds) != 1 || adds[0].State != TaskAdditionApplied {
		t.Fatalf("additions = %+v, want one applied", adds)
	}
	c := f.taskByKey(t, "c")
	if c == nil {
		t.Fatal("the task c was not added")
	}
	tid, _ := parseTaskID(c.ID)
	var started int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE kind != 'judgment' AND challenge_id = ? AND task_id = ?`, f.cid(), tid).Scan(&started)
	})
	if started != 1 {
		t.Errorf("the added task %s was not started by a delegation in the same cycle", c.ID)
	}
}

// AC-218（済みのタスクの枠が、足すときの上限の検査へ渡る）: 足した後の見込みが、済みの a を含めて 14.5 になる c は上限 14 を超えるので、
// 足さずに、足す依頼を「オーナー待ち」にして課題を保留にする（a を数えなければ 9.5 で通ってしまう）。
func TestExpand_AC218_DoneTaskBudgetCountsTowardTheLimitCheck(t *testing.T) {
	t.Parallel()
	f := newExpandCLIFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	inv := &expandRecorder{out: []byte(expandAddOutput(func(ts []map[string]any) {
		ts[0]["depends_on"] = []string{"a"}
		ts[0]["repo"], ts[0]["operation"] = "cli-repo", "cli-op"
		ts[0]["budget_impl_usd"], ts[0]["budget_review_usd"] = 3.0, 1.5
	}))}
	if _, err := f.s.RunCycle(context.Background(), expandCycleInput(f, inv, &fakeDelegator{})); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].State != TaskAdditionAwaitingOwner {
		t.Fatalf("additions = %+v, want one awaiting the owner", adds)
	}
	if f.taskByKey(t, "c") != nil {
		t.Error("the task c was added although the estimate exceeds the limit")
	}
	if st := f.detail(t, f.id).Status; st != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human", st)
	}
}

// 宣言を読めなかったときは、検査に通らないのではなく、読めなかった理由を誤りの文に出す。
func TestAnswerDecls_UnreadableDeclarationShowsTheReason(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	d := s.answerDecls(AnswerRequest{})
	if d.agent != nil && d.conn != nil {
		t.Skip("the workspace has readable declarations")
	}
	if d.loadErr == nil {
		t.Fatal("loadErr is nil although a declaration is missing")
	}
	var err error
	readForTest(t, s, func(tx *sql.Tx) error {
		_, err = evaluateTaskAddition(context.Background(), tx, 1, 1, d)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be read") || !strings.Contains(err.Error(), "declaration") {
		t.Errorf("err = %v, want one that says the declaration cannot be read, with the reason", err)
	}
}
