package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 形態 cli の接続ツールの契約（docs/architecture.md §10）の core 側（宣言の差し込み・出力の検査・
// 状態の写像・委譲の流れ）を、実ストアと偽の委譲の IF で検証する。

func contractDoc(state, runID string, mut func(m map[string]any)) []byte {
	m := map[string]any{
		"contract_version": 1, "run_id": runID, "state": state, "summary": "SUMMARY",
		"requested_action": nil, "artifacts": []any{}, "cost_usd": 12.5,
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func contractOut(doc []byte) JudgmentLaunchOutput {
	return JudgmentLaunchOutput{Result: RunResultSucceeded, ContractOutput: doc}
}

func TestParseContractDocument(t *testing.T) {
	if d, err := parseContractDocument(contractDoc("waiting", "R1", func(m map[string]any) {
		m["requested_action"] = map[string]any{"kind": "approve", "decider": "human", "text": "承認して"}
	})); err != nil || d.RequestedAction == nil || d.RequestedAction.Kind != "approve" || d.CostUSD == nil || *d.CostUSD != 12.5 {
		t.Fatalf("valid waiting document: d=%+v err=%v", d, err)
	}
	bad := map[string][]byte{
		"not json":      []byte("hello"),
		"unknown key":   contractDoc("succeeded", "R1", func(m map[string]any) { m["extra"] = 1 }),
		"missing key":   contractDoc("succeeded", "R1", func(m map[string]any) { delete(m, "cost_usd") }),
		"version 2":     contractDoc("succeeded", "R1", func(m map[string]any) { m["contract_version"] = 2 }),
		"empty run id":  contractDoc("succeeded", "", nil),
		"negative cost": contractDoc("succeeded", "R1", func(m map[string]any) { m["cost_usd"] = -1 }),
		"unknown state": contractDoc("paused", "R1", nil),
		"unknown decider": contractDoc("waiting", "R1", func(m map[string]any) {
			m["requested_action"] = map[string]any{"kind": "answer", "decider": "bot", "text": "x"}
		}),
		"unknown kind": contractDoc("waiting", "R1", func(m map[string]any) {
			m["requested_action"] = map[string]any{"kind": "wait", "decider": "human", "text": "x"}
		}),
	}
	for name, doc := range bad {
		if _, err := parseContractDocument(doc); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestMapContractOutput_StatesMapToTheDelegationReport(t *testing.T) {
	declared := []HumanQuestionKind(nil)
	ok := mapContractOutput(contractDoc("succeeded", "R1", func(m map[string]any) {
		m["artifacts"] = []any{
			map[string]any{"kind": "pr", "ref": "https://github.com/o/r/pull/9"},
			map[string]any{"kind": "pr", "ref": "9"},
			map[string]any{"kind": "branch", "ref": "feat/x"},
			map[string]any{"kind": "commit", "ref": "abc"},
		}
	}), false)
	rep, reason := checkDelegationReport(ok.Report, declared)
	if ok.Result != RunResultSucceeded || reason != "" || rep.Outcome != DelegationOutcomeCompleted ||
		!reflect.DeepEqual(rep.PRURLs, []string{"https://github.com/o/r/pull/9"}) || rep.Branch == nil || *rep.Branch != "feat/x" ||
		!reflect.DeepEqual(rep.Commits, []string{"abc"}) || ok.RunID != "R1" || ok.CostUSD == nil || *ok.CostUSD != 12.5 {
		t.Errorf("succeeded: %+v report=%s reason=%q", ok, ok.Report, reason)
	}

	w := mapContractOutput(contractDoc("waiting", "R2", func(m map[string]any) {
		m["requested_action"] = map[string]any{"kind": "approve", "decider": "human", "text": "ASK-TEXT"}
	}), false)
	wr, reason := checkDelegationReport(w.Report, declared)
	if w.Result != RunResultSucceeded || reason != "" || wr.Outcome != DelegationOutcomeBlocked ||
		!strings.Contains(wr.Summary, "ASK-TEXT") || !strings.Contains(wr.Summary, "approve") || !strings.Contains(wr.Summary, "R2") {
		t.Errorf("waiting: %+v report=%s", w, w.Report)
	}

	for _, st := range []string{"failed", "cancelled", "running"} {
		m := mapContractOutput(contractDoc(st, "R3", nil), false)
		if m.Result != RunResultErrored || !strings.Contains(m.Error, st) || m.RunID != "R3" || m.Report != nil {
			t.Errorf("%s: %+v", st, m)
		}
	}
}

func TestMapContractOutput_InvalidOutput(t *testing.T) {
	if m := mapContractOutput([]byte("oops"), false); m.Result != RunResultErrored || m.RunID != "" {
		t.Errorf("start with a bad document = %+v, want errored (the connector ran; no run id to resume)", m)
	}
	if m := mapContractOutput([]byte("oops"), true); m.Result != RunResultInvalidOutput || !strings.HasPrefix(m.Error, invalidReportErrorPrefix) {
		t.Errorf("resume with a bad document = %+v, want invalid_output", m)
	}
}

func TestFillCLIRunArgs(t *testing.T) {
	args := []string{"harness", "contract", "resume", "--input", "{input}", "{run_id}"}
	if got := fillCLIRunArgs(args, "R1", "ANS"); !reflect.DeepEqual(got, []string{"harness", "contract", "resume", "--input", "ANS", "R1"}) {
		t.Errorf("with input = %v", got)
	}
	if got := fillCLIRunArgs(args, "R1", ""); !reflect.DeepEqual(got, []string{"harness", "contract", "resume", "R1"}) {
		t.Errorf("without input = %v (the option name must go with its value)", got)
	}
	// 回答の中身はそのまま 1 つの引数（空白・先頭の `-` を含んでも分割・解釈しない）。
	if got := fillCLIRunArgs([]string{"c", "{input}"}, "R1", "a b --x"); !reflect.DeepEqual(got, []string{"c", "a b --x"}) {
		t.Errorf("input with spaces = %v", got)
	}
	// 入力の中の `{run_id}` は置換しない。`--input={input}` の形は、入力が無ければその引数だけを外す。
	if got := fillCLIRunArgs([]string{"c", "--json", "--input={input}", "{run_id}"}, "R1", ""); !reflect.DeepEqual(got, []string{"c", "--json", "R1"}) {
		t.Errorf("joined option without input = %v", got)
	}
	if got := fillCLIRunArgs([]string{"c", "{input}", "{run_id}"}, "R1", "x {run_id}"); !reflect.DeepEqual(got, []string{"c", "x {run_id}", "R1"}) {
		t.Errorf("input containing a placeholder = %v", got)
	}
}

func TestConnectorsDeclaration_CLICommandPlaceholdersAreAClosedSetPerCommand(t *testing.T) {
	mk := func(start, status, resume, cancel string) string {
		return `{"version":1,"connectors":[{"id":"c","form":"cli","permission_mode":"default","contract_version":1,
 "commands":{"start":` + start + `,"status":` + status + `,"resume":` + resume + `,"cancel":` + cancel + `},
 "operations":[{"id":"op","interactive":false,"artifacts":"pr"}]}],
 "repos":[{"name":"r","remote":"o/r","default_branch":"main","connector":"c"}]}`
	}
	good := mk(`["h","start","{issue_number}","{challenge_id}"]`, `["h","status","{run_id}"]`, `["h","resume","--input","{input}","{run_id}"]`, `["h","cancel","{run_id}"]`)
	d, err := parseConnectorsDeclaration([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.validate(); err != nil {
		t.Fatalf("valid cli declaration rejected: %v", err)
	}
	for name, raw := range map[string]string{
		"run_id in start":        mk(`["h","start","{run_id}"]`, `["h","s"]`, `["h","r"]`, `["h","c"]`),
		"issue_number in cancel": mk(`["h","start"]`, `["h","s"]`, `["h","r"]`, `["h","c","{issue_number}"]`),
		"input in status":        mk(`["h","start"]`, `["h","s","{input}"]`, `["h","r"]`, `["h","c"]`),
		"unknown placeholder":    mk(`["h","start"]`, `["h","s"]`, `["h","r","{answer}"]`, `["h","c"]`),
	} {
		d, err := parseConnectorsDeclaration([]byte(raw))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := d.validate(); err == nil {
			t.Errorf("%s: want ErrConfigInvalid", name)
		}
	}
}

// --- 委譲の流れ ---

const cliConnectorsPatch = `{"id": "direct", "form": "brief"`

func newCLIFixture(t *testing.T) *delegateFixture {
	t.Helper()
	f := newDelegateFixture(t)
	raw := strings.Replace(delegateConnectorsJSON, cliConnectorsPatch, `{"id": "ct", "form": "cli", "permission_mode": "default", "contract_version": 1,
      "commands": {"start": ["harness", "contract", "start", "ticket", "--input", "challenge={challenge_id}"],
                   "status": ["harness", "contract", "status", "{run_id}"],
                   "resume": ["harness", "contract", "resume", "--input", "{input}", "{run_id}"],
                   "cancel": ["harness", "contract", "cancel", "{run_id}"]},
      "operations": [{"id": "cli-impl", "interactive": false, "child_may_decide": true, "artifacts": "none"}]},
    `+cliConnectorsPatch, 1)
	raw = strings.Replace(raw, `"connector": "direct"`, `"connector": "ct"`, 1)
	conn, err := parseConnectorsDeclaration([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.validate(); err != nil {
		t.Fatal(err)
	}
	f.conn = conn
	return f
}

func cliPlanSpec() string {
	return planSpec(func(m map[string]any) { m["repo"] = "direct-repo"; m["operation"] = "cli-impl" })
}

func TestRunDelegation_CLI_StartRunsTheContractCommandWithoutJ3AndTakesCostAndRunIDFromTheDocument(t *testing.T) {
	f := newCLIFixture(t)
	id := f.newInProgress(t, "t", "P1", cliPlanSpec())
	f.deleg.result = contractOut(contractDoc("succeeded", "20261110-080303-cf1b1e", nil))
	if _, err := f.run(t, &id); err != nil {
		t.Fatal(err)
	}
	in := f.deleg.launched()
	if len(in) != 1 || in[0].Form != ConnectorFormCLI || in[0].IsResume ||
		!reflect.DeepEqual(in[0].CLIStart, []string{"harness", "contract", "start", "ticket", "--input", "challenge=C-1"}) {
		t.Fatalf("launch = %+v", in)
	}
	if len(f.j3Inputs) != 0 {
		t.Errorf("J3 was called %d times for a cli connector, want 0", len(f.j3Inputs))
	}
	runs := f.delegateRuns(t)
	if len(runs) != 1 || runs[0].Result != RunResultSucceeded || runs[0].SessionID != "20261110-080303-cf1b1e" ||
		runs[0].SessionIDMismatch || runs[0].CostUSD == nil || *runs[0].CostUSD != 12_500_000 {
		t.Fatalf("run = %+v, want the connector's run id as session id and cost 12.5 USD", runs)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(runs[0].Output), &rep); err != nil || rep["outcome"] != "completed" {
		t.Errorf("run output = %q, want the mapped delegation report", runs[0].Output)
	}
	if d := f.detail(t, id); d.Status != StatusVerifying {
		t.Errorf("status = %s, want verifying (completed, artifacts none)", d.Status)
	}
}

func TestRunDelegation_CLI_WaitingBecomesAGenericHoldAndTheAnswerResumesTheSameRun(t *testing.T) {
	f := newCLIFixture(t)
	id := f.newInProgress(t, "t", "P1", cliPlanSpec())
	f.deleg.queue = []JudgmentLaunchOutput{
		contractOut(contractDoc("waiting", "RUN-1", func(m map[string]any) {
			m["requested_action"] = map[string]any{"kind": "approve", "decider": "human", "text": "ASK-TEXT"}
			m["cost_usd"] = 5.0
		})),
		contractOut(contractDoc("succeeded", "RUN-1", func(m map[string]any) { m["cost_usd"] = 12.0 })),
	}
	res, err := f.run(t, &id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Outcome != "blocked" || res.Items[0].Status == nil || *res.Items[0].Status != string(StatusAwaitingHuman) {
		t.Fatalf("item = %+v, want a hold (awaiting_human)", res.Items[0])
	}
	if d := f.detail(t, id); len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, "ASK-TEXT") {
		t.Fatalf("holds = %+v, want the requested action in the question", d.Holds)
	}
	f.answerLatestHold(t, id, "ANS")
	if _, err := f.run(t, &id); err != nil {
		t.Fatal(err)
	}
	in := f.deleg.launched()
	if len(in) != 2 || !in[1].IsResume || in[1].SessionID != "RUN-1" ||
		!reflect.DeepEqual(in[1].CLIResume, []string{"harness", "contract", "resume", "--input", "ANS", "RUN-1"}) ||
		!reflect.DeepEqual(in[1].CLIStatus, []string{"harness", "contract", "status", "RUN-1"}) ||
		!reflect.DeepEqual(in[1].CLICancel, []string{"harness", "contract", "cancel", "RUN-1"}) {
		t.Fatalf("resume launch = %+v", in)
	}
	runs := f.delegateRuns(t)
	if len(runs) != 2 || runs[1].Result != RunResultSucceeded || runs[1].SessionID != "RUN-1" || runs[1].SessionIDMismatch {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestRunDelegation_CLI_FailedStateIsAnErroredRunAndBadDocumentOnStartIsAnErroredRunWithoutASessionID(t *testing.T) {
	f := newCLIFixture(t)
	id := f.newInProgress(t, "t", "P1", cliPlanSpec())
	f.deleg.result = contractOut(contractDoc("failed", "RUN-F", func(m map[string]any) { m["summary"] = "BOOM" }))
	if _, err := f.run(t, &id); err != nil {
		t.Fatal(err)
	}
	runs := f.delegateRuns(t)
	if len(runs) != 1 || runs[0].Result != RunResultErrored || !strings.Contains(runs[0].Error, "BOOM") || runs[0].SessionID != "RUN-F" {
		t.Fatalf("failed state: runs = %+v", runs)
	}
	if st := f.slotStates(t)["slot-d"]; st != "idle" {
		t.Errorf("slot = %q, want idle", st)
	}

	f2 := newCLIFixture(t)
	id2 := f2.newInProgress(t, "t", "P1", cliPlanSpec())
	f2.deleg.result = contractOut([]byte("not a contract document"))
	if _, err := f2.run(t, &id2); err != nil {
		t.Fatal(err)
	}
	runs = f2.delegateRuns(t)
	if len(runs) != 1 || runs[0].Result != RunResultErrored || runs[0].SessionID != "" || runs[0].CostUSD == nil || *runs[0].CostUSD == 0 {
		t.Fatalf("bad document: runs = %+v, want errored with no session id (nothing to resume) and the budget counted", runs)
	}
	if st := f2.slotStates(t)["slot-d"]; st != "idle" {
		t.Errorf("slot = %q, want idle (reconciled)", st)
	}
}
