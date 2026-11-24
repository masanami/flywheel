package core

// このファイルは、計画の承認でタスクの記録へ planned_flow・flow_reason を写すこと（AC-55〜AC-57）と、委譲を始めるとき承認したフローで
// 始めること（AC-59〜AC-63）を、実ストア・偽の契約のコマンドで確かめる。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// plannedFlowSpec は j2FlowConnectorsJSON の宣言で計画の出力を検査して作った spec（検査が planned_flow を足す）。
func plannedFlowSpec(t *testing.T, repo, op string, mut func(task map[string]any)) string {
	t.Helper()
	v, reason := validateJ2OutputReason(flowPlanOutput(repo, op, mut), j2FlowContext(t))
	if reason != "" {
		t.Fatalf("validate: %s", reason)
	}
	return string(v.Spec)
}

// タスクの行は planned_flow を持ち、flow_reason は前後の空白を除いた値（AC-55・AC-56）。
func TestPlannedFlow_AC55_AC56_TaskRecordsFlowAndTrimmedReason(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "light", "P2", plannedFlowSpec(t, "flow-repo", "light", func(task map[string]any) {
		task["flow_reason"] = "  \n文書だけの変更なので簡略なフローで足りる \t"
	}))
	cid, _ := parseChallengeID(id)
	rows := taskRows(t, f.s, cid)
	if len(rows) != 1 {
		t.Fatalf("tasks = %d, want 1", len(rows))
	}
	if rows[0].PlannedFlow != "ticket-light" {
		t.Errorf("planned_flow = %q, want ticket-light", rows[0].PlannedFlow)
	}
	if want := "文書だけの変更なので簡略なフローで足りる"; rows[0].FlowReason != want {
		t.Errorf("flow_reason = %q, want %q", rows[0].FlowReason, want)
	}
	// 計画の spec の flow_reason は、モデルが書いたまま（空白を除かない）残る。
	var spec struct {
		Tasks []struct {
			FlowReason string `json:"flow_reason"`
		} `json:"tasks"`
	}
	var raw sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT spec FROM task_plan WHERE challenge_id = ?`, cid).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw.String), &spec); err != nil || len(spec.Tasks) != 1 || !strings.HasPrefix(spec.Tasks[0].FlowReason, "  \n") {
		t.Errorf("spec = %+v (err %v), want flow_reason kept as the model wrote", spec, err)
	}
}

// flow の形でない委譲先のタスクは、planned_flow・flow_reason がどちらも NULL（AC-57）。
func TestPlannedFlow_AC57_NonFlowConnectorTaskHasNullColumns(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "brief", "P2", plannedFlowSpec(t, "brief-repo", "brief", nil))
	cid, _ := parseChallengeID(id)
	var pf, fr sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT planned_flow, flow_reason FROM task WHERE challenge_id = ?`, cid).Scan(&pf, &fr)
	}); err != nil {
		t.Fatal(err)
	}
	if pf.Valid || fr.Valid {
		t.Errorf("planned_flow = %+v, flow_reason = %+v, want both NULL", pf, fr)
	}
}

// 続きのタスクを足す経路・フォローアップをタスクにする経路の入力も、同じに写す。
func TestPlannedFlow_ExpandInputsCarryFlowAndReason(t *testing.T) {
	t.Parallel()
	vc := j2FlowContext(t)
	for _, skip := range []bool{false, true} {
		task := j2TestTask("b", "flow-repo", "light")
		task["budget_impl_usd"], task["budget_review_usd"] = 1.0, 1.0
		task["flow_reason"] = "小さな追加"
		data, _ := json.Marshal(map[string]any{"verdict": "add", "tasks": []any{task}})
		ec := j2ExpandValidationContext{Agent: vc.Agent, Conn: vc.Conn, Repos: []string{"flow-repo"}, TakenKeys: map[string]bool{}, DepKeys: map[string]bool{}, SkipFlowReason: skip}
		v, reason := validateJ2ExpandOutputReason(data, ec)
		if reason != "" {
			t.Fatalf("skip=%v: %s", skip, reason)
		}
		in := j2ExpandTaskInputs(v.Tasks)
		if in[0].PlannedFlow != "ticket-light" || in[0].FlowReason != "小さな追加" {
			t.Errorf("skip=%v: planned_flow = %q, flow_reason = %q", skip, in[0].PlannedFlow, in[0].FlowReason)
		}
	}
	// flow の形でない委譲先は空（NULL になる）。
	task := j2TestTask("c", "brief-repo", "brief")
	task["budget_impl_usd"], task["budget_review_usd"] = 1.0, 1.0
	data, _ := json.Marshal(map[string]any{"verdict": "add", "tasks": []any{task}})
	v, reason := validateJ2ExpandOutputReason(data, j2ExpandValidationContext{Agent: vc.Agent, Conn: vc.Conn, Repos: []string{"brief-repo"}, TakenKeys: map[string]bool{}, DepKeys: map[string]bool{}})
	if reason != "" {
		t.Fatal(reason)
	}
	if in := j2ExpandTaskInputs(v.Tasks); in[0].PlannedFlow != "" || in[0].FlowReason != "" {
		t.Errorf("non-flow add = %+v, want empty", in[0])
	}
}

func (f *contractFixture) setPlannedFlow(t *testing.T, flow string) {
	t.Helper()
	var v any
	if flow != "" {
		v = flow
	}
	if _, err := f.exec(`UPDATE task SET planned_flow = ?`, v); err != nil {
		t.Fatal(err)
	}
}

// setOperationFlow は宣言の操作 impl の flow を書き換える（承認の後に宣言を書き換えた状態）。
func (f *contractFixture) setOperationFlow(flow string) {
	for i := range f.conn.Connectors {
		for j := range f.conn.Connectors[i].Operations {
			f.conn.Connectors[i].Operations[j].Flow = flow
		}
	}
}

// 始めるコマンドの最後の引数は、タスクの planned_flow（AC-59）。承認の後に宣言の操作の flow を書き換えても変わらない（AC-60）。
func TestPlannedFlow_AC59_AC60_StartUsesPlannedFlowEvenAfterDeclarationChange(t *testing.T) {
	t.Parallel()
	f := newFlowContractFixture(t, "")
	f.setPlannedFlow(t, "ticket-light")
	f.setOperationFlow("ticket") // 承認の後に、宣言の操作の flow を ticket へ書き換えた
	if _, err := f.start(t); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v, want one", f.client.starts)
	}
	cmd := f.client.starts[0]
	if got := cmd[len(cmd)-1]; got != "ticket-light" {
		t.Errorf("last arg = %q, want ticket-light (the planned flow)", got)
	}
}

// planned_flow が NULL のタスク（S2 より前に承認したタスク）は、そのときの宣言の操作の flow を使う（AC-63）。
func TestPlannedFlow_AC63_NullPlannedFlowUsesDeclaredOperationFlow(t *testing.T) {
	t.Parallel()
	f := newFlowContractFixture(t, "")
	f.setPlannedFlow(t, "")
	f.setOperationFlow("ticket-light")
	if _, err := f.start(t); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v, want one", f.client.starts)
	}
	cmd := f.client.starts[0]
	if got := cmd[len(cmd)-1]; got != "ticket-light" {
		t.Errorf("last arg = %q, want the flow of the declared operation", got)
	}
}

// planned_flow があるのに委譲先が flow の形でなくなったときは、契約のコマンドを起動せず（AC-61）、オーナーへの保留ができる（AC-62）。
func TestPlannedFlow_AC61_AC62_ConnectorBackToCLIDoesNotStartAndHolds(t *testing.T) {
	t.Parallel()
	f := newFlowContractFixture(t, "")
	f.setPlannedFlow(t, "ticket")
	cli, err := parseConnectorsDeclaration([]byte(contractConnectorsJSON)) // 承認の後に、委譲先を cli の形へ戻した
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.validate(); err != nil {
		t.Fatal(err)
	}
	f.conn = cli
	_, err = f.start(t)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("start err = %v, want ErrValidation", err)
	}
	if len(f.client.starts) != 0 {
		t.Fatalf("starts = %v, want the contract command not started", f.client.starts)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run`); n != 0 {
		t.Errorf("runs = %d, want 0", n)
	}
	q := f.queryString(t, `SELECT question FROM hold WHERE answer IS NULL AND closed_at IS NULL`)
	if !q.Valid || !strings.Contains(q.String, "承認したフローで走れない") || !strings.Contains(q.String, "計画を作り直すか") {
		t.Errorf("hold question = %q, want it to ask about the approved flow and replanning", q.String)
	}
	if got := f.queryString(t, `SELECT status FROM challenge`).String; got != string(StatusAwaitingHuman) {
		t.Errorf("challenge status = %s, want awaiting_human", got)
	}
	if got := taskStatusOfRow(t, f); got != TaskStatusPending {
		t.Errorf("task status = %s, want pending (not started)", got)
	}
}

func taskStatusOfRow(t *testing.T, f *contractFixture) TaskStatus {
	t.Helper()
	return TaskStatus(f.queryString(t, `SELECT status FROM task`).String)
}

// 周の委譲でも、planned_flow があるのに委譲先が flow の形でない（brief）ときは、J3 も子も起動せず、起動しなかった理由を返し、保留ができる。
func TestPlannedFlow_AC61_AC62_DelegationStageHoldsForNonFlowBriefConnector(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "b", "P1", briefSpec(nil))
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET planned_flow = 'ticket' WHERE challenge_id = ?`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	if len(f.j3Inputs) != 0 || len(f.delegateRuns(t)) != 0 {
		t.Errorf("J3 inputs = %d, delegate runs = %d, want nothing started", len(f.j3Inputs), len(f.delegateRuns(t)))
	}
	if got := notStartedByReason(res, NotStartedPlanUnavailable); len(got) != 1 {
		t.Errorf("not started = %+v, want one plan_unavailable", res.NotStarted)
	}
	var holds int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL AND question LIKE '%承認したフローで走れない%'`, cid).Scan(&holds)
	}); err != nil || holds != 1 {
		t.Errorf("open holds = %d (err %v), want 1", holds, err)
	}
}

// 空白だけの flow_reason は NULL、空白だけの planned_flow は planned_flow・flow_reason とも NULL。
func TestPlannedFlow_BlankValuesBecomeNull(t *testing.T) {
	t.Parallel()
	if fl, re := plannedFlowColumns("ticket", " \n\t"); fl != "ticket" || re != nil {
		t.Errorf("blank reason = (%v, %v), want (ticket, nil)", fl, re)
	}
	if fl, re := plannedFlowColumns("  ", "理由"); fl != nil || re != nil {
		t.Errorf("blank flow = (%v, %v), want (nil, nil)", fl, re)
	}
}
