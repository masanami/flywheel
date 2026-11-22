package core

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// このファイルは、統合先を使う課題（タスク 2 個。2 個目は 1 個目を前提にする）が、手で回さず 1 回のサイクルで完了の承認待ちへ届くこと
// （AC-2・AC-3・AC-4。区切り S4a の Issue #991）を、実ストア・偽の委譲先・偽の判断の呼び出しで確かめる。
// 委譲先（integFlowClient）は、実行ごとに、状態の読み取りのたびに決めた順に状態を進める
// （起動 → 作業中 → レビュー待ち → 「入れてよい」が返ったら成功。差し戻しが返ったら、また作業中 → レビュー待ち）。

// integFlowTask は 1 つのタスクの実行の、委譲先の側の台本。
type integFlowTask struct {
	pr      int    // タスクの成果物の PR の番号
	content string // 最初のレビュー待ちの中身の識別
	landed  string // 統合先へ入った後の中身の識別
}

type integFlowRun struct {
	task    *integFlowTask
	reflect bool
	// running は、あと何回、作業中を返すか。
	running int
	// responded は差し戻しを返された回数。landed は「入れてよい」を返されたか。
	responded int
	landed    bool
}

type integFlowClient struct {
	mu      sync.Mutex
	starts  [][]string
	resumes [][]string
	runs    map[string]*integFlowRun
	byTask  map[string]*integFlowTask // タスクの ID → 台本
}

func (c *integFlowClient) Start(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts = append(c.starts, in.Command)
	id := "ext-" + strconv.Itoa(len(c.starts))
	run := &integFlowRun{running: 1}
	if in.Command[1] == "reflect" {
		run.reflect = true
	} else {
		for i, a := range in.Command {
			if a == "--task" {
				run.task = c.byTask[in.Command[i+1]]
			}
		}
	}
	c.runs[id] = run
	return contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = id }), nil
}

func (c *integFlowClient) Status(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := in.Command[2]
	r := c.runs[id]
	with := func(st *ContractStatus) { st.RunID = id }
	switch {
	case r == nil:
		return ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "unknown run " + id}, nil
	case r.reflect:
		if r.running > 0 {
			r.running--
			return contractOut(ContractStateRunning, nil, with), nil
		}
		out := upReviewOut([]string{"respond", "land"}, upArtsOK...)
		out.Status.RunID = id
		return out, nil
	case r.landed:
		arts := []ContractArtifact{{Kind: "pr", Ref: integFlowPRURL(r.task.pr)}, integBranchArt, {Kind: "commit", Ref: r.task.landed, Role: "integration"}}
		return contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) { st.RunID = id; st.Artifacts = arts }), nil
	case r.running > 0:
		r.running--
		return contractOut(ContractStateRunning, nil, with), nil
	}
	// 差し戻された後は、別の中身のレビュー待ちを返す。
	content := r.task.content
	if r.responded > 0 {
		content += strconv.Itoa(r.responded)
	}
	arts := []ContractArtifact{{Kind: "commit", Ref: content}, integBranchArt, {Kind: "commit", Ref: "aaaaaaa" + strconv.Itoa(r.responded), Role: "integration"}}
	out := intakeOut(intakeOptsWithLand, arts, nil)
	out.Status.RunID = id
	return out, nil
}

func (c *integFlowClient) Resume(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resumes = append(c.resumes, in.Command)
	id := in.Command[2]
	if r := c.runs[id]; r != nil {
		if strings.Contains(strings.Join(in.Command, " "), "--choice land") {
			r.landed = true
		} else {
			r.responded++
			r.running = 1
		}
	}
	return contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = id }), nil
}

func (c *integFlowClient) Cancel(context.Context, ContractLaunchInput) (ContractLaunchOutput, error) {
	return ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "cancel is not expected"}, nil
}

func integFlowPRURL(n int) string { return "https://github.com/o/r/pull/" + strconv.Itoa(n) }

// runIntegrationCycle は、タスク a・b（b は a を前提にする）の統合先を使う課題を、1 回のサイクルで回す。verdicts は J5 が呼ばれた順に返す判定
// （尽きたら最後のもの。課題の単位の確かめも同じ列から返す）。
func runIntegrationCycle(t *testing.T, verdicts []string) (f *contractFixture, id string, c *integFlowClient, res *CycleRunResult, j5Calls int) {
	t.Helper()
	f = newMultiFixture(t, 2)
	conn, err := parseConnectorsDeclaration([]byte(strings.Replace(upConnectorsJSON(),
		`"--answer", "{answer}"`, `"--choice", "{choice}", "--note", "{note}", "--approved", "{approved_ref}"`, 1)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := conn.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	f.conn = conn
	f.agent.CyclePollIntervalSec = 1
	id = f.newMulti(t, "chain", "P1", true, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}})

	tasks := map[string]*integFlowTask{
		"a": {pr: 11, content: "c0ffee1", landed: "d0000001"},
		"b": {pr: 12, content: "c0ffee2", landed: "d0000002"},
	}
	c = &integFlowClient{runs: map[string]*integFlowRun{}, byTask: map[string]*integFlowTask{}}
	checks := newIgLandChecks()
	for key, tk := range tasks {
		c.byTask[f.taskID(t, id, key)] = tk
		// 検証した中身（最初の中身と、差し戻しの後の中身）はチェックが通っている。入った後の PR は統合先へ merged で、head は検証した中身。
		checks.commits[tk.content] = igPassingChecks
		checks.commits[tk.content+"1"] = igPassingChecks
		f.branches.pulls[releaseKey(tk.pr)] = UpstreamPullRequest{URL: integFlowPRURL(tk.pr), Title: "task " + key, State: "merged", Base: integBranch}
		head := tk.content
		if key == "a" && verdicts[0] == "not_met" {
			head += "1" // 1 個目のタスクは、差し戻しの後の中身が入る
		}
		checks.prs[releaseKey(tk.pr)] = UpstreamPullRequestChecks{URL: integFlowPRURL(tk.pr), State: "merged", Base: integBranch, HeadSHA: head}
	}
	// 課題を反映先へ上げる依頼の PR（既定ブランチ宛て）。課題の単位の確かめは、この PR のチェックを読む。
	f.setUpPR("main")
	up := prChecks("open", checkDone)
	up.URL = upPRURL
	mergeable := true // 「計算中」のままだと、J5 がマージ可否を実時間で待ち直す
	up.Mergeable = &mergeable
	checks.prs["o/r#7"] = up

	rf := &roundsFixture{reviewFlowFixture: &reviewFlowFixture{contractFixture: f}, wg: &fakeWorkspaceGit{}, verdicts: verdicts}
	waits := 0
	rf.onWait = func(ctx context.Context, _ int) error {
		if waits++; waits > 40 { // 取りこぼしても無限に回らない
			return context.Canceled
		}
		return ctx.Err()
	}
	in := CycleRunInput{
		Trigger: "manual", AgentDecl: f.agent, ConnDecl: f.conn, Invoker: rf.invoker(t), Upstream: f.upstream, Delegate: f.deleg, Git: f.git,
		Reconcile: f.branches, Checks: checks, Diffs: &fakeDiffs{}, HeadGit: &noHeadGit{}, Contract: c, Compare: newFakeCompare(),
		WorkspaceGit: rf.wg, Wait: rf.wait,
	}
	res, err = f.s.RunCycle(context.Background(), in)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	return f, id, c, res, rf.judgedCount(JudgmentJ5)
}

// AC-2: 統合先を使う、計画を承認した課題（タスク 2 個。2 個目は 1 個目を前提にする）について、どちらのタスクも、委譲先が「作業中 → レビュー待ち」と進み、
// J5 が「満たした」を返し、課題の単位の確かめも「満たした」を返す → 1 回のサイクルで、課題は完了の承認待ちになる。
func TestRunCycle_Rounds_AC2_IntegrationChallengeReachesCompletionApprovalInOneCycle(t *testing.T) {
	t.Parallel()
	f, id, c, res, j5 := runIntegrationCycle(t, []string{"met"})

	if got := f.statusOf(t, id); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval (end_reason %q, starts %v, resumes %v)", got, res.Cycle.EndReason, c.starts, c.resumes)
	}
	if res.Cycle.EndReason != CycleEndSettled || res.Cycle.Result != CycleResultCompleted {
		t.Errorf("end_reason = %q result = %q, want settled/completed", res.Cycle.EndReason, res.Cycle.Result)
	}
	// 2 つのタスクの検証と、課題の単位の確かめで、J5 は 3 回。
	if j5 != 3 {
		t.Errorf("J5 calls = %d, want 3 (task a, task b, the challenge-level check)", j5)
	}
	cid, _ := parseChallengeID(id)
	for _, key := range []string{"a", "b"} {
		if got := taskStatusMap(t, f.s, cid)[key]; got != TaskStatusDone {
			t.Errorf("task %s = %s, want done", key, got)
		}
	}
	reflects := 0
	for _, s := range c.starts {
		if s[1] == "reflect" {
			reflects++
		}
	}
	if reflects != 1 {
		t.Errorf("reflect starts = %d, want 1", reflects)
	}
}

// AC-3・AC-4: 同じ課題で、1 個目のタスクの J5 が、1 度「満たしていない」を返し、その次に「満たした」を返す → 1 回のサイクルで、課題は完了の承認待ちになり、
// 差し戻し（respond）は、1 個目のタスクの実行に、ちょうど 1 回返る。
func TestRunCycle_Rounds_AC3_AC4_IntegrationChallengeWithOneNotMetVerdict(t *testing.T) {
	t.Parallel()
	f, id, c, res, j5 := runIntegrationCycle(t, []string{"not_met", "met"})

	if got := f.statusOf(t, id); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval (end_reason %q, starts %v, resumes %v)", got, res.Cycle.EndReason, c.starts, c.resumes)
	}
	var responds []string
	for _, r := range c.resumes {
		if line := strings.Join(r, " "); strings.Contains(line, "--choice respond") {
			responds = append(responds, r[2])
		}
	}
	if len(responds) != 1 || responds[0] != "ext-1" { // 1 個目のタスクの実行は、最初に起動される
		t.Errorf("responds = %v, want exactly one, to the run of the first task (ext-1)", responds)
	}
	if j5 != 4 {
		t.Errorf("J5 calls = %d, want 4 (task a not met, task a met, task b, the challenge-level check)", j5)
	}
}
