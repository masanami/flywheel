package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// このファイルは `form: cli` の接続ツールの委譲の前半（開始・状態の確認・成果物と費用の取り込み）を、
// 実ストア・偽の ContractClient で検証する。

const contractConnectorsJSON = `{
  "version": 1,
  "human_question_kinds": [],
  "connectors": [
    {"id": "tool", "form": "cli", "permission_mode": "default",
     "commands": {
       "start": ["tool", "start", "--challenge", "{challenge_id}"],
       "status": ["tool", "status", "{run_id}"],
       "resume": ["tool", "resume", "{run_id}", "--answer", "{answer}"],
       "cancel": ["tool", "cancel", "{run_id}"]
     },
     "contract_version": 1,
     "operations": [{"id": "impl", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]}
  ],
  "repos": [
    {"name": "main-repo", "remote": "o/r", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot1"]}}
  ]
}`

type fakeContractClient struct {
	starts   [][]string
	statuses [][]string
	resumes  [][]string
	cancels  [][]string
	// resumeOut・cancelOut は resume・cancel が返す値。
	resumeOut ContractLaunchOutput
	cancelOut ContractLaunchOutput
	// startOut・queue は呼び出しのたびに返す値（queue が空なら last を返す）。
	startOut ContractLaunchOutput
	queue    []ContractLaunchOutput
	last     ContractLaunchOutput
	// workDirs は呼び出しごとに受け取った WorkDir（コマンド名: start・status・resume・cancel）。
	workDirs map[string][]string
}

func (f *fakeContractClient) recordWorkDir(name, dir string) {
	if f.workDirs == nil {
		f.workDirs = map[string][]string{}
	}
	f.workDirs[name] = append(f.workDirs[name], dir)
}

func (f *fakeContractClient) Start(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	f.starts = append(f.starts, in.Command)
	f.recordWorkDir("start", in.WorkDir)
	return f.startOut, nil
}

func (f *fakeContractClient) Status(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	f.statuses = append(f.statuses, in.Command)
	f.recordWorkDir("status", in.WorkDir)
	if len(f.queue) > 0 {
		f.last = f.queue[0]
		f.queue = f.queue[1:]
	}
	return f.last, nil
}

func (f *fakeContractClient) Resume(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	f.resumes = append(f.resumes, in.Command)
	f.recordWorkDir("resume", in.WorkDir)
	return f.resumeOut, nil
}

func (f *fakeContractClient) Cancel(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	f.cancels = append(f.cancels, in.Command)
	f.recordWorkDir("cancel", in.WorkDir)
	return f.cancelOut, nil
}

func contractOut(state ContractState, cost *float64, mut func(st *ContractStatus)) ContractLaunchOutput {
	st := &ContractStatus{ContractVersion: 1, RunID: "ext-1", State: state, Summary: "s-" + string(state), CostUSD: cost}
	if mut != nil {
		mut(st)
	}
	return ContractLaunchOutput{Result: RunResultSucceeded, Status: st, RawOutput: []byte(`{}`)}
}

type contractFixture struct {
	*delegateFixture
	client *fakeContractClient
	cid    string
}

func newContractFixture(t *testing.T) *contractFixture {
	t.Helper()
	return newContractFixtureWith(t, contractConnectorsJSON)
}

func newContractFixtureWith(t *testing.T, connectorsJSON string) *contractFixture {
	t.Helper()
	f := newDelegateFixture(t)
	conn, err := parseConnectorsDeclaration([]byte(connectorsJSON))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := conn.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	f.conn = conn
	cf := &contractFixture{delegateFixture: f, client: &fakeContractClient{startOut: contractOut(ContractStateRunning, nil, nil)}}
	cf.cid = f.newInProgress(t, "T", "P2", planSpec(nil))
	return cf
}

func (f *contractFixture) start(t *testing.T) (*ContractRunReport, error) {
	t.Helper()
	return f.s.StartContractDelegation(context.Background(), ContractStartInput{
		ChallengeID: f.cid, ConnectorID: "tool", AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
		CycleID: f.cycle(t, 300), MaxBudgetUSD: 5, Reconcile: f.branches,
	})
}

func (f *contractFixture) poll(t *testing.T) []ContractRunReport {
	t.Helper()
	reps, err := f.s.PollContractRuns(context.Background(), ContractPollInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client, CycleID: f.cycle(t, 300), Reconcile: f.branches})
	if err != nil {
		t.Fatalf("PollContractRuns: %v", err)
	}
	return reps
}

func (f *contractFixture) contractRow(t *testing.T) (state, summary string, cost int64, ended bool) {
	t.Helper()
	var e sql.NullString
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state, summary, cost_usd, ended_at FROM contract_run`).Scan(&state, &summary, &cost, &e)
	})
	if err != nil {
		t.Fatalf("read contract_run: %v", err)
	}
	return state, summary, cost, e.Valid
}

func (f *contractFixture) count(t *testing.T, q string) int64 {
	t.Helper()
	var n int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q).Scan(&n) }); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestStartContractDelegation_SavesExternalRunIDAndReturnsWithoutWaiting(t *testing.T) {
	f := newContractFixture(t)
	rep, err := f.start(t)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if rep.Result != RunResultSucceeded || rep.State != ContractStateRunning || rep.ExternalRunID != "ext-1" {
		t.Fatalf("report = %+v, want succeeded/running/ext-1（running は失敗にしない）", rep)
	}
	if want := []string{"tool", "start", "--challenge", f.cid}; len(f.client.starts) != 1 || !reflect.DeepEqual(f.client.starts[0], want) {
		t.Fatalf("start command = %v, want %v（宣言の配列に差し込みを埋める）", f.client.starts, want)
	}
	state, _, _, ended := f.contractRow(t)
	if state != "running" || ended {
		t.Fatalf("contract_run state=%s ended=%v, want running and not ended", state, ended)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE result IS NULL`); n != 0 {
		t.Fatalf("%d runs are still open, want 0（その周の委譲は終わる）", n)
	}
}

func TestStartContractDelegation_SecondStartWhileActiveIsRunInProgress(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := f.start(t); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("second start err = %v, want ErrRunInProgress", err)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %d, want 1", len(f.client.starts))
	}
}

func TestStartContractDelegation_FailedOrInvalidOutputCreatesNoContractRun(t *testing.T) {
	cases := map[string]struct {
		out  ContractLaunchOutput
		want RunResult
	}{
		"errored":        {ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "exit 1"}, RunResultErrored},
		"wrong version":  {contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.ContractVersion = 2 }), RunResultInvalidOutput},
		"empty run id":   {contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = "" }), RunResultInvalidOutput},
		"unknown state":  {contractOut("paused", nil, nil), RunResultInvalidOutput},
		"unknown action": {contractOut(ContractStateWaiting, nil, func(st *ContractStatus) { st.RequestedAction = &ContractRequestedAction{Kind: "x", Decider: "human"} }), RunResultInvalidOutput},
		"negative cost":  {contractOut(ContractStateRunning, float64Ptr(-1), nil), RunResultInvalidOutput},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newContractFixture(t)
			f.client.startOut = c.out
			rep, err := f.start(t)
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			if rep.Result != c.want {
				t.Fatalf("result = %s, want %s", rep.Result, c.want)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM contract_run`); n != 0 {
				t.Fatalf("contract_run rows = %d, want 0", n)
			}
		})
	}
}

func TestStartContractDelegation_RejectsNonCLIConnectorAndNotInProgress(t *testing.T) {
	f := newContractFixture(t)
	other := newDelegateFixture(t)
	_, err := f.s.StartContractDelegation(context.Background(), ContractStartInput{
		ChallengeID: f.cid, ConnectorID: "harness", AgentDecl: f.agent, ConnDecl: other.conn, Client: f.client, MaxBudgetUSD: 5,
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("non-cli connector err = %v, want ErrValidation", err)
	}
	ch, _ := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "N", Description: "d", DoneCriteria: "c"})
	_, err = f.s.StartContractDelegation(context.Background(), ContractStartInput{
		ChallengeID: ch.ID, ConnectorID: "tool", AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client, MaxBudgetUSD: 5,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("not in progress err = %v, want ErrInvalidTransition", err)
	}
}

func TestPollContractRuns_RunningAndWaitingAreRecordedAndCarriedToTheNextCycle(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{
		contractOut(ContractStateRunning, nil, nil),
		contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
			st.RequestedAction = &ContractRequestedAction{Kind: "observe", Decider: "any", Text: "見守る"}
		}),
	}
	if reps := f.poll(t); len(reps) != 1 || reps[0].State != ContractStateRunning {
		t.Fatalf("poll 1 = %+v, want running", reps)
	}
	reps := f.poll(t)
	if len(reps) != 1 || reps[0].State != ContractStateWaiting || reps[0].RequestedAction == nil || reps[0].RequestedAction.Text != "見守る" {
		t.Fatalf("poll 2 = %+v, want waiting with requested action", reps)
	}
	if want := []string{"tool", "status", "ext-1"}; !reflect.DeepEqual(f.client.statuses[0], want) {
		t.Fatalf("status command = %v, want %v（{run_id} に保存した実行 ID を差し込む）", f.client.statuses[0], want)
	}
	state, _, _, ended := f.contractRow(t)
	if state != "waiting" || ended {
		t.Fatalf("state=%s ended=%v, want waiting and not ended（次の周でも読む）", state, ended)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 0 {
		t.Fatalf("holds = %d, want 0（observe は保留にせず次の周で読み直す）", n)
	}
	if reps := f.poll(t); len(reps) != 1 {
		t.Fatalf("poll 3 = %+v, want the waiting run to be read again", reps)
	}
}

func TestPollContractRuns_SucceededImportsPRArtifactsOnceAndStopsPolling(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) {
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/9"}, {Kind: "other", Ref: "x"}}
	})}
	reps := f.poll(t)
	if len(reps) != 1 || reps[0].State != ContractStateSucceeded || !reflect.DeepEqual(reps[0].PRs, []string{"https://github.com/o/r/pull/9"}) {
		t.Fatalf("report = %+v, want succeeded with the PR", reps)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact WHERE kind = 'pr' AND ref = 'https://github.com/o/r/pull/9'`); n != 1 {
		t.Fatalf("pr artifacts = %d, want 1", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact`); n != 1 {
		t.Fatalf("artifacts = %d, want 1（other は取り込まない）", n)
	}
	if _, _, _, ended := f.contractRow(t); !ended {
		t.Fatal("contract_run is not ended")
	}
	if reps := f.poll(t); len(reps) != 0 || len(f.client.statuses) != 1 {
		t.Fatalf("after success: reports=%v status calls=%d, want none（終わった契約は読まない）", reps, len(f.client.statuses))
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact`); n != 1 {
		t.Fatalf("artifacts = %d after another poll, want still 1", n)
	}
}

func TestPollContractRuns_FailedAndCancelledEndTheContractRunWithoutArtifacts(t *testing.T) {
	for _, st := range []ContractState{ContractStateFailed, ContractStateCancelled} {
		t.Run(string(st), func(t *testing.T) {
			f := newContractFixture(t)
			if _, err := f.start(t); err != nil {
				t.Fatal(err)
			}
			f.client.queue = []ContractLaunchOutput{contractOut(st, nil, func(s *ContractStatus) {
				s.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/1"}}
			})}
			reps := f.poll(t)
			if len(reps) != 1 || reps[0].State != st || len(reps[0].PRs) != 0 {
				t.Fatalf("report = %+v", reps)
			}
			state, _, _, ended := f.contractRow(t)
			if state != string(st) || !ended {
				t.Fatalf("state=%s ended=%v", state, ended)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM run_artifact`); n != 0 {
				t.Fatalf("artifacts = %d, want 0（成功のときだけ取り込む）", n)
			}
		})
	}
}

func TestPollContractRuns_ReadFailureKeepsTheStateAndRecordsTheResult(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{
		{Result: RunResultTimedOut, ErrorSummary: "timed out"},
		contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = "other" }),
	}
	for i, want := range []RunResult{RunResultTimedOut, RunResultInvalidOutput} {
		reps := f.poll(t)
		if len(reps) != 1 || reps[0].Result != want || reps[0].State != ContractStateRunning {
			t.Fatalf("poll %d = %+v, want result %s and state unchanged", i, reps, want)
		}
	}
	if state, summary, _, ended := f.contractRow(t); state != "running" || summary != "s-running" || ended {
		t.Fatalf("contract_run changed by a failed read: %s %s %v", state, summary, ended)
	}
}

// 状態の確認でも、contract_version が合わない応答は取り込まずに失敗（invalid_output）にする。
func TestPollContractRuns_WrongContractVersionIsInvalidOutputAndImportsNothing(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) {
		st.ContractVersion = 2
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/9"}}
	})}
	reps := f.poll(t)
	if len(reps) != 1 || reps[0].Result != RunResultInvalidOutput {
		t.Fatalf("report = %+v, want result invalid_output", reps)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact`); n != 0 {
		t.Fatalf("artifacts = %d, want 0（版が合わない応答の PR は取り込まない）", n)
	}
	if state, summary, _, ended := f.contractRow(t); state != "running" || summary != "s-running" || ended {
		t.Fatalf("contract_run changed by a wrong-version read: state=%s summary=%s ended=%v", state, summary, ended)
	}
}

// 費用: 契約の cost_usd は孫を含む累計。状態を何度読んでも二重に数えない（差分だけを run に記録し、累計で置き換える）。
func TestContractCost_CumulativeIsNotDoubleCountedAcrossReads(t *testing.T) {
	f := newContractFixture(t)
	f.client.startOut = contractOut(ContractStateRunning, float64Ptr(1), nil)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		cost      *float64
		wantDelta float64
		wantTotal float64
	}{
		{float64Ptr(1), 0, 1},   // 同じ累計を読み直しても増えない
		{float64Ptr(3), 2, 3},   // 増えた分だけ
		{float64Ptr(3), 0, 3},   // もう一度読んでも増えない
		{float64Ptr(2.5), 0, 3}, // 累計が減って見えても減らさない
		{nil, 0, 3},             // 報告が無いときは増分なし
		{float64Ptr(3.25), 0.25, 3.25},
	}
	for i, st := range steps {
		f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, st.cost, nil)}
		reps := f.poll(t)
		if len(reps) != 1 || reps[0].CostDeltaUSD != st.wantDelta || reps[0].CostTotalUSD != st.wantTotal {
			t.Fatalf("step %d: %+v, want delta %v total %v", i, reps, st.wantDelta, st.wantTotal)
		}
		if _, _, cost, _ := f.contractRow(t); cost != usdToMicros(st.wantTotal) {
			t.Fatalf("step %d: contract_run.cost_usd = %d, want %d", i, cost, usdToMicros(st.wantTotal))
		}
		// 課題の実装枠の費用（予算の判定に使う run の費用の合計）は累計と等しい。
		if got := f.count(t, `SELECT COALESCE(SUM(cost_usd), 0) FROM run WHERE challenge_id = 1 AND budget_bucket = 'impl'`); got != usdToMicros(st.wantTotal) {
			t.Fatalf("step %d: sum of run costs = %d, want %d", i, got, usdToMicros(st.wantTotal))
		}
	}
}

func TestContractRun_CountsAsAnActiveRunForDelegationTargets(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	var targets []int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		targets, _, err = selectDelegationTargets(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("targets = %v, want none while the contract run is active", targets)
	}
}

func TestContractCommands_RunInTheChallengeRepoWorkDir(t *testing.T) {
	worktreeJSON := strings.Replace(contractConnectorsJSON,
		`{"provider": "clone", "paths": ["slot1"]}`, `{"provider": "worktree", "base": "repos/base", "count": 2}`, 1)
	for _, tc := range []struct {
		name, connectors, rel string
	}{
		{"clone は idle の作業用クローン", contractConnectorsJSON, "slot1"},
		{"worktree は元のクローン（base）", worktreeJSON, "repos/base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newContractFixtureWith(t, tc.connectors)
			want := filepath.Join(f.s.workspace, tc.rel)
			if _, err := f.start(t); err != nil {
				t.Fatalf("start: %v", err)
			}
			f.client.last = contractOut(ContractStateRunning, nil, nil)
			f.poll(t)
			// 観測の待ちを読んだ後の答え無しの resume も同じ作業ディレクトリで走る。
			observe := waitingOut(&ContractRequestedAction{Kind: "observe", Decider: "any", Text: "見ている"}, nil)
			f.client.queue = []ContractLaunchOutput{observe, observe}
			f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
			f.poll(t) // 観測の待ちを記録する
			f.poll(t) // 記録済みの観測の待ちを読み直し、答え無しの resume を呼ぶ
			f.client.last = contractOut(ContractStateRunning, nil, nil)
			f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
			if _, err := f.s.CancelOpenContractRuns(context.Background(), ContractCancelInput{
				ChallengeID: f.cid, AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client}); err != nil {
				t.Fatalf("cancel: %v", err)
			}
			for _, name := range []string{"start", "status", "resume", "cancel"} {
				got := f.client.workDirs[name]
				if len(got) == 0 {
					t.Fatalf("%s was not called", name)
				}
				for _, d := range got {
					if d != want {
						t.Errorf("%s WorkDir = %q, want %q（ワークスペース %q ではなく課題のリポジトリ）", name, d, want, f.s.workspace)
					}
				}
			}
		})
	}
}

// clone に idle の作業用クローンが無いとき、周の委譲は周を止めず slot_unavailable で見送り、start を呼ばない。
func TestDelegateViaContract_NoIdleCloneIsSlotUnavailable(t *testing.T) {
	f := newContractFixture(t)
	if err := f.s.EnsureSlots(context.Background(), f.conn.Repos[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE slot SET state = 'needs_attention', attention_reason = 'x'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	in := f.input(f.cycle(t, 300), nil)
	in.Contract = f.client
	dc := &delegationContext{Challenge: f.detail(t, f.cid).Challenge, Connector: f.conn.Connectors[0], ImplBudgetMicros: usdToMicros(3), ReviewBudgetMicros: usdToMicros(2)}
	item, ns, err := f.s.delegateViaContract(context.Background(), in, NewJudgmentCycle(in.CycleID), dc, nil)
	if err != nil || item != nil || ns == nil || ns.Reason != NotStartedSlotUnavailable {
		t.Fatalf("item = %+v not_started = %+v err = %v, want slot_unavailable", item, ns, err)
	}
	if len(f.client.starts) != 0 {
		t.Fatalf("starts = %v, want none", f.client.starts)
	}
}

// status・resume・cancel は start で記録した作業ディレクトリで実行し、決め直さない（start で使ったスロットが idle で
// なくなり、別の idle のスロットがあっても start のスロットのまま）。記録の無い既存の run はワークスペースで実行する。
func TestContractCommands_UseTheWorkDirRecordedAtStart(t *testing.T) {
	twoSlots := strings.Replace(contractConnectorsJSON, `"paths": ["slot1"]`, `"paths": ["slot1", "slot2"]`, 1)
	for _, tc := range []struct {
		name   string
		legacy bool // start の後に記録を消し、記録を足す前に始めた run として扱う
		rel    string
	}{
		{"start で記録したスロット", false, "slot1"},
		{"記録の無い既存の run はワークスペース", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newContractFixtureWith(t, twoSlots)
			if _, err := f.start(t); err != nil {
				t.Fatalf("start: %v", err)
			}
			if got, want := f.client.workDirs["start"], []string{filepath.Join(f.s.workspace, "slot1")}; len(got) != 1 || got[0] != want[0] {
				t.Fatalf("start WorkDir = %v, want %v", got, want)
			}
			if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
				if _, err := tx.Exec(`UPDATE slot SET state = 'needs_attention', attention_reason = 'x' WHERE path LIKE '%slot1'`); err != nil {
					return err
				}
				if tc.legacy {
					_, err := tx.Exec(`UPDATE contract_run SET work_dir = NULL`)
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			want := f.s.workspace
			if tc.rel != "" {
				want = filepath.Join(f.s.workspace, tc.rel)
			}
			f.client.last = contractOut(ContractStateRunning, nil, nil)
			f.poll(t)
			observe := waitingOut(&ContractRequestedAction{Kind: "observe", Decider: "any", Text: "見ている"}, nil)
			f.client.queue = []ContractLaunchOutput{observe, observe}
			f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
			f.poll(t)
			f.poll(t)
			f.client.last = contractOut(ContractStateRunning, nil, nil)
			f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
			if _, err := f.s.CancelOpenContractRuns(context.Background(), ContractCancelInput{
				ChallengeID: f.cid, AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client}); err != nil {
				t.Fatalf("cancel: %v", err)
			}
			for _, name := range []string{"status", "resume", "cancel"} {
				got := f.client.workDirs[name]
				if len(got) == 0 {
					t.Fatalf("%s was not called", name)
				}
				for _, d := range got {
					if d != want {
						t.Errorf("%s WorkDir = %q, want %q", name, d, want)
					}
				}
			}
		})
	}
}
