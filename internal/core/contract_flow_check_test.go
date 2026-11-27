package core

// このファイルは、flow の形の委譲先でタスクの委譲を始める前のフローの定義の確かめ（docs/features/flow-customization.md「壊れた定義」
// 「どの定義で動くか」「外せない線」）を、実ストア・偽の契約のコマンドで確かめる。偽は確かめの出力の形を返すだけで、本物の実行ファイルを起動しない。

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// failingFlowCheck は、name のワークスペースの定義が検査を通らない確かめの結果。
func failingFlowCheck(name string) *FlowCheckOutput {
	return &FlowCheckOutput{Result: RunResultSucceeded, Definitions: []FlowCheckDefinition{{
		Name: name, Source: FlowDefinitionSourceWorkspace, Kind: "replace", Path: "/ws/.flywheel/flows/" + name + ".yaml", OK: false,
		Problems: []FlowCheckProblem{{File: "/ws/.flywheel/flows/" + name + ".yaml", Line: 12, Message: "unknown step kind"}, {File: "/ws/.flywheel/flows/" + name + ".yaml", Message: "no start step"}},
	}}}
}

func workspaceFlowCheck(name, sum string) *FlowCheckOutput {
	return &FlowCheckOutput{Result: RunResultSucceeded, Definitions: []FlowCheckDefinition{{
		Name: name, Source: FlowDefinitionSourceWorkspace, Kind: "new", Path: "/ws/.flywheel/flows/" + name + ".yaml", OK: true, SHA256: sum}}}
}

func (f *contractFixture) openFlowHolds(t *testing.T, id string) int64 {
	t.Helper()
	cid, _ := parseChallengeID(id)
	return f.count(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = `+strconv.FormatInt(cid, 10)+` AND reason = 'flow_definition_invalid' AND answer IS NULL AND closed_at IS NULL`)
}

// 始める前に、同じ実行ファイルの flow check --json <フローの名前> を、契約のコマンドと同じ --workspace で呼ぶ。通れば始める。
func TestFlowCheck_RunsTheCheckWithTheSameExecutableBeforeStart(t *testing.T) {
	t.Parallel()
	f := newFlowContractFixture(t, "")
	f.runDelegationStage(t, nil)
	want := []string{flowTestExe, "flow", "check", "--workspace", f.s.Workspace(), "--json", "ticket"}
	if len(f.client.checks) != 1 || !reflect.DeepEqual(f.client.checks[0], want) {
		t.Fatalf("checks = %q, want %q", f.client.checks, want)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %q, want one", f.client.starts)
	}
}

// cli の形の委譲先では確かめを呼ばない。
func TestFlowCheck_NotForTheCLIForm(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	f.runDelegationStage(t, nil)
	if len(f.client.checks) != 0 || len(f.client.starts) != 1 {
		t.Fatalf("checks = %q starts = %q, want no check and one start", f.client.checks, f.client.starts)
	}
}

// 定義が検査を通らないとき、委譲を始めず（実行も記録しない）、どのファイルのどこが悪いかと、直せば次のサイクルで始めることを書いた保留を立てる。
// 組み込みの定義で代わりに動かさない。ほかの課題の委譲は続ける。同じタスクで同じ理由の保留が開いていれば立て直さない。
func TestFlowCheck_InvalidDefinitionHoldsWithoutStarting(t *testing.T) {
	t.Parallel()
	f := newContractFixtureWith(t, strings.Replace(flowOneRepoConnectorsJSON(""), `"artifacts": "pr"}]`,
		`"artifacts": "pr"}, {"id": "light", "flow": "ticket-light", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]`, 1))
	other := f.newInProgress(t, "U", "P2", planSpec(func(m map[string]any) { m["tasks"] = []any{j2TestTask("a", "main-repo", "light")} }))
	f.client.checkFor = map[string]*FlowCheckOutput{"ticket": failingFlowCheck("ticket")}
	f.runDelegationStage(t, nil)
	// 壊れた定義（ticket）の課題は保留にして始めない。壊れていない定義（ticket-light）の課題は始める。
	if len(f.client.starts) != 1 || f.client.starts[0][len(f.client.starts[0])-1] != "ticket-light" {
		t.Fatalf("starts = %q, want only the ticket-light start", f.client.starts)
	}
	if got := f.statusOf(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	if n := f.openFlowHolds(t, f.cid); n != 1 {
		t.Fatalf("open flow holds = %d, want 1", n)
	}
	if got := f.statusOf(t, other); got != StatusInProgress || f.openFlowHolds(t, other) != 0 {
		t.Fatalf("the other challenge = %s, want in_progress without a hold", got)
	}
	q := f.queryString(t, `SELECT question FROM hold WHERE reason = 'flow_definition_invalid' ORDER BY id LIMIT 1`).String
	for _, want := range []string{`"ticket"`, "ワークスペースの定義（/ws/.flywheel/flows/ticket.yaml）",
		"/ws/.flywheel/flows/ticket.yaml:12: unknown step kind", "/ws/.flywheel/flows/ticket.yaml: no start step",
		"定義を直せば、次のサイクルで自動で委譲を始める"} {
		if !strings.Contains(q, want) {
			t.Errorf("question = %q, want it to contain %q", q, want)
		}
	}
	// 同じ課題を名指しで委譲し直しても（保留中なので選ばれない）、保留は増えない。
	f.runDelegationStage(t, nil)
	if n := f.openFlowHolds(t, f.cid); n != 1 {
		t.Errorf("open flow holds after the next cycle = %d, want still 1", n)
	}
}

// 保留を立て直さない: 開いている同じ理由の保留があるタスクには、もう 1 つ立てない（タスクを止める保留が重なる課題で確かめる）。
func TestFlowCheck_DoesNotRaiseTheSameHoldTwice(t *testing.T) {
	t.Parallel()
	f := newFlowContractFixture(t, "")
	f.client.checkOut = failingFlowCheck("ticket")
	cid, _ := parseChallengeID(f.cid)
	task := taskRows(t, f.s, cid)[0]
	d := &failingFlowCheck("ticket").Definitions[0]
	for i := 0; i < 2; i++ {
		_ = f.s.holdFlowDefinitionInvalid(context.Background(), f.cid, task, "ticket", d)
	}
	if n := f.openFlowHolds(t, f.cid); n != 1 {
		t.Fatalf("open flow holds = %d, want 1", n)
	}
}

// 定義を直した後のサイクルでは、確かめ直して保留を閉じ（作業ログに残す）、委譲を始める。
func TestFlowCheck_NextCycleClosesTheHoldAndStarts(t *testing.T) {
	t.Parallel()
	f := newFlowContractFixture(t, "")
	f.client.checkOut = failingFlowCheck("ticket")
	f.runDelegationStage(t, nil)
	if f.openFlowHolds(t, f.cid) != 1 {
		t.Fatal("no flow hold")
	}
	// 直らないうちは閉じない。
	f.runDelegationStage(t, nil)
	if f.openFlowHolds(t, f.cid) != 1 || len(f.client.starts) != 0 {
		t.Fatalf("hold closed or started while the definition is still broken (starts %q)", f.client.starts)
	}
	f.client.checkOut = nil // 直った（組み込みの定義が通る）
	f.runDelegationStage(t, nil)
	if n := f.openFlowHolds(t, f.cid); n != 0 {
		t.Fatalf("open flow holds = %d, want 0", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold WHERE reason = 'flow_definition_invalid' AND closed_at IS NOT NULL AND answer IS NULL`); n != 1 {
		t.Errorf("closed holds without an answer = %d, want 1", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'flow_definition_unhold'`); n != 1 {
		t.Errorf("activity flow_definition_unhold = %d, want 1", n)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %q, want one after the fix", f.client.starts)
	}
	if got := f.statusOf(t, f.cid); got != StatusInProgress {
		t.Errorf("status = %s, want in_progress", got)
	}
}

// 確かめのコマンド自体が起動できない・JSON が読めないときは、委譲を始めず、始めるコマンドが失敗したときと同じに記録して保留にする。
func TestFlowCheck_UnavailableIsTreatedAsAStartFailure(t *testing.T) {
	t.Parallel()
	for name, out := range map[string]*FlowCheckOutput{
		"launch_failed": {Result: RunResultLaunchFailed, ErrorSummary: "no such file"},
		"malformed":     {Result: RunResultMalformed, ErrorSummary: "not json"},
		"name missing":  {Result: RunResultSucceeded, Definitions: []FlowCheckDefinition{{Name: "other", OK: true, SHA256: "x"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFlowContractFixture(t, "")
			f.client.checkOut = out
			f.runDelegationStage(t, nil)
			if len(f.client.starts) != 0 {
				t.Fatalf("starts = %q, want none", f.client.starts)
			}
			res := f.queryString(t, `SELECT result FROM run WHERE kind = 'contract'`).String
			if res == "" || res == string(RunResultSucceeded) {
				t.Fatalf("delegation run result = %q, want a failure", res)
			}
			if e := f.queryString(t, `SELECT error FROM run WHERE kind = 'contract'`).String; !strings.Contains(e, "flow check") {
				t.Errorf("run error = %q, want the check failure", e)
			}
			if got := f.statusOf(t, f.cid); got != StatusAwaitingHuman {
				t.Errorf("status = %s, want awaiting_human", got)
			}
			if n := f.openFlowHolds(t, f.cid); n != 0 {
				t.Errorf("flow definition holds = %d, want 0 (it is a start failure)", n)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM contract_run`); n != 0 {
				t.Errorf("contract runs = %d, want 0", n)
			}
		})
	}
}

// 組み込みに無い名前の操作は、ワークスペースの定義の確かめが通れば、その名前で始め、契約の出力の definition を委譲の実行の記録に持つ。
func TestFlowCheck_WorkspaceFlowStartsAndRecordsTheDefinition(t *testing.T) {
	t.Parallel()
	f := newContractFixtureWith(t, strings.Replace(flowOneRepoConnectorsJSON(""), `"flow": "ticket"`, `"flow": "quick-fix"`, 1))
	sum := strings.Repeat("c", 64)
	f.client.checkOut = workspaceFlowCheck("quick-fix", sum)
	f.client.startOut = contractOut(ContractStateRunning, nil, func(st *ContractStatus) {
		st.Definition = &ContractDefinition{Source: FlowDefinitionSourceWorkspace, SHA256: sum}
	})
	f.runDelegationStage(t, nil)
	if len(f.client.starts) != 1 || f.client.starts[0][len(f.client.starts[0])-1] != "quick-fix" {
		t.Fatalf("starts = %q, want one start of quick-fix", f.client.starts)
	}
	if got := f.queryString(t, `SELECT definition_source || '|' || definition_sha256 FROM contract_run`).String; got != "workspace|"+sum {
		t.Fatalf("contract_run definition = %q", got)
	}
	// タスクの詳細の「委譲先の実行」に、定義の出どころとハッシュが載る。
	for _, tk := range f.detailOf(t, f.cid).Tasks {
		if d := tk.Detail.Delegate; d == nil || d.Definition == nil || d.Definition.Source != FlowDefinitionSourceWorkspace || d.Definition.SHA256 != sum {
			t.Fatalf("task detail delegate = %+v", tk.Detail.Delegate)
		}
	}
	// 状態の読みに definition が無い・壊れた値なら、記録を変えない。
	f.client.queue = []ContractLaunchOutput{
		contractOut(ContractStateRunning, nil, nil),
		contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.Definition = &ContractDefinition{Source: "elsewhere", SHA256: sum} }),
	}
	f.poll(t)
	f.poll(t)
	if got := f.queryString(t, `SELECT definition_source || '|' || definition_sha256 FROM contract_run`).String; got != "workspace|"+sum {
		t.Fatalf("contract_run definition after reads = %q", got)
	}
}

// 外せない線: 委譲の実行の上限額は core が課題とタスクの枠から決め、始めるコマンドに予算を渡さない（定義の予算の上限で上限が広がらない）。
// 費用の累計がその上限を上回れば、定義に関わらず取り消す。
func TestFlowCheck_BudgetLimitIsTheCoresEvenIfTheDefinitionIsLarger(t *testing.T) {
	t.Parallel()
	f := newFlowContractFixture(t, "")
	f.client.startOut = contractOut(ContractStateRunning, nil, func(st *ContractStatus) {
		st.Definition = &ContractDefinition{Source: FlowDefinitionSourceWorkspace, SHA256: strings.Repeat("d", 64)}
	})
	if _, err := f.start(t); err != nil { // 上限 5 USD
		t.Fatal(err)
	}
	if argsContain(f.client.starts[0], "--budget-usd") {
		t.Fatalf("start = %q, want no budget passed to the flow", f.client.starts[0])
	}
	if got := f.count(t, `SELECT max_budget_usd FROM contract_run`); got != usdToMicros(5) {
		t.Fatalf("max_budget_usd = %d, want %d", got, usdToMicros(5))
	}
	over := 1000.0 // 定義の上限が 1000 USD でも、core の上限 5 USD で止める
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, &over, nil)}
	f.client.cancelOut = contractOut(ContractStateCancelled, &over, nil)
	f.poll(t)
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %q, want one (the cost is over the core's limit)", f.client.cancels)
	}
}

// 統合先を使う課題では、保留はタスクを止める保留になり、直った後のサイクルで、閉じたタスクから委譲を始める。
func TestFlowCheck_IntegrationChallengeHoldsPerTask(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 4)
	conn, err := parseConnectorsDeclaration([]byte(flowMultiConnectorsJSON()))
	if err != nil {
		t.Fatal(err)
	}
	f.conn = conn
	id := f.newMulti(t, "integ", "P2", true, multiTask{key: "a"}, multiTask{key: "b"})
	f.client.checkOut = failingFlowCheck("ticket")
	f.runDelegationStage(t, nil)
	if len(f.client.starts) != 0 {
		t.Fatalf("starts = %q, want none", f.client.starts)
	}
	cid, _ := parseChallengeID(id)
	if n := f.count(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = `+strconv.FormatInt(cid, 10)+` AND reason = 'flow_definition_invalid' AND scope = 'task' AND closed_at IS NULL`); n != 2 {
		t.Fatalf("task-scoped flow holds = %d, want 2", n)
	}
	f.client.checkOut = nil
	f.runDelegationStage(t, nil)
	if n := f.openFlowHolds(t, id); n != 0 {
		t.Fatalf("open flow holds = %d, want 0", n)
	}
	if len(f.client.starts) != 2 {
		t.Fatalf("starts = %q, want both tasks", f.client.starts)
	}
	if got := f.statusOf(t, id); got != StatusInProgress {
		t.Errorf("status = %s, want in_progress", got)
	}
}
