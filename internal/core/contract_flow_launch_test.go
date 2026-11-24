package core

// このファイルは、form: flow の委譲先の契約のコマンドを core が自分の実行ファイルの flow のコマンドとして組み立てて起動すること
// （flow-port S1 の AC-71〜AC-87・AC-139〜AC-141・AC-145）を、実ストア・偽の契約のコマンドで確かめる。
// 契約のコマンドを受ける偽は、出力を返すだけで、本物の実行ファイルを起動しない。

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

const flowTestExe = "/opt/flywheel/bin/flywheel"

const flowLaunchRepos = `[
    {"name": "main-repo", "remote": "o/r", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot1"]}},
    {"name": "other-repo", "remote": "o/s", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot-s"]}}
  ]`

// flowLaunchConnectorsJSON は form: flow の委譲先 tool（permission_mode: auto）と 2 つのリポジトリの宣言。extra は委譲先直下へ足すキー。
func flowLaunchConnectorsJSON(extra string) string {
	return `{
  "version": 1,
  "human_question_kinds": [],
  "connectors": [
    {"id": "tool", "form": "flow", "permission_mode": "auto"` + extra + `,
     "operations": [{"id": "impl", "flow": "ticket", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]}
  ],
  "repos": ` + flowLaunchRepos + `
}`
}

func flowMultiConnectorsJSON() string { return flowLaunchConnectorsJSON("") }

// flowOneRepoConnectorsJSON は contractConnectorsJSON と同じ 1 つのリポジトリを持つ、form: flow の宣言。
func flowOneRepoConnectorsJSON(extra string) string {
	return strings.Replace(flowLaunchConnectorsJSON(extra), flowLaunchRepos, `[
    {"name": "main-repo", "remote": "o/r", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot1"]}}
  ]`, 1)
}

func flowConnector(t *testing.T, extra string) *Connector {
	t.Helper()
	d, err := parseConnectorsDeclaration([]byte(flowLaunchConnectorsJSON(extra)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := d.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return &d.Connectors[0]
}

// ---- 組み立て（AC-71〜AC-81・AC-139〜AC-141） ----

func TestFlowCommand_Builder(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	s.SetFlowExecutable(flowTestExe)
	ws := s.Workspace()
	pre := []string{flowTestExe, "flow"}
	delegated := []string{"--permission-mode", "auto", "--delegated-run", delegatedRunPlaceholder}
	join := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	source := &sourceBinding{ExternalKey: "o/r#42", URL: "https://github.com/o/r/issues/42"}
	startFill := contractFill{ChallengeID: "C-186", Source: source, EmptySource: true, TaskFile: "/w/run/task.md", TaskID: "T-9", UsesIntegration: true, Flow: "ticket"}
	inputs := func(challengeID string) []string {
		return []string{"--input", "issue=42", "--input", "task_file=/w/run/task.md", "--input", "task_id=T-9", "--input", "uses_integration=true", "--input", "challenge_id=" + challengeID}
	}

	t.Run("AC-71〜AC-75 始める: 先頭は渡された実行ファイル、並びは表のとおり、最後は操作の flow", func(t *testing.T) {
		got, err := s.contractCommandArgs(flowConnector(t, ""), contractCmdStart, startFill)
		if err != nil {
			t.Fatal(err)
		}
		want := join(pre, []string{"start", "--workspace", ws}, delegated, inputs("C-186"), []string{"ticket"})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("start = %q\nwant  %q", got, want)
		}
	})
	t.Run("AC-73 --permission-mode は宣言の値", func(t *testing.T) {
		d, err := parseConnectorsDeclaration([]byte(strings.Replace(flowLaunchConnectorsJSON(""), `"permission_mode": "auto"`, `"permission_mode": "acceptEdits"`, 1)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.contractCommandArgs(&d.Connectors[0], contractCmdStart, startFill)
		if err != nil || argAfter(t, got, "--permission-mode") != "acceptEdits" {
			t.Fatalf("start = %q err %v, want --permission-mode acceptEdits", got, err)
		}
	})
	t.Run("AC-76 前置きが tom- なら challenge_id は tom-C-186", func(t *testing.T) {
		got, err := s.contractCommandArgs(flowConnector(t, `, "challenge_id_prefix": "tom-"`), contractCmdStart, startFill)
		if err != nil || !containsPair(got, "--input", "challenge_id=tom-C-186") {
			t.Fatalf("start = %q err %v, want challenge_id=tom-C-186", got, err)
		}
	})
	t.Run("AC-77 前置きが無ければ challenge_id は C-186", func(t *testing.T) {
		got, err := s.contractCommandArgs(flowConnector(t, ""), contractCmdStart, startFill)
		if err != nil || !containsPair(got, "--input", "challenge_id=C-186") {
			t.Fatalf("start = %q err %v, want challenge_id=C-186", got, err)
		}
	})
	t.Run("取り込み元が無いとき issue は空のまま渡す", func(t *testing.T) {
		f := startFill
		f.Source = nil
		got, err := s.contractCommandArgs(flowConnector(t, ""), contractCmdStart, f)
		if err != nil || !containsPair(got, "--input", "issue=") {
			t.Fatalf("start = %q err %v, want an empty issue input", got, err)
		}
	})
	t.Run("AC-139 models の ticket の変数は、変数名の辞書順に --input の前へ入る", func(t *testing.T) {
		c := flowConnector(t, `, "models": {"ticket": {"REVIEW": "sonnet", "IMPL": "opus"}, "reflect": {"R": "haiku"}}`)
		got, err := s.contractCommandArgs(c, contractCmdStart, startFill)
		if err != nil {
			t.Fatal(err)
		}
		want := join(pre, []string{"start", "--workspace", ws}, delegated, []string{"--model", "IMPL=opus", "--model", "REVIEW=sonnet"}, inputs("C-186"), []string{"ticket"})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("start = %q\nwant  %q", got, want)
		}
	})
	t.Run("AC-140 models に ticket の分が無ければ --model を含まない", func(t *testing.T) {
		c := flowConnector(t, `, "models": {"reflect": {"R": "haiku"}}`)
		got, err := s.contractCommandArgs(c, contractCmdStart, startFill)
		if err != nil || argsContain(got, "--model") {
			t.Fatalf("start = %q err %v, want no --model", got, err)
		}
	})
	t.Run("AC-81・AC-141 反映の依頼: 並びは表のとおり、reflect の models を含む", func(t *testing.T) {
		c := flowConnector(t, `, "challenge_id_prefix": "tom-", "models": {"ticket": {"IMPL": "opus"}, "reflect": {"W": "x", "A": "y"}}`)
		got, err := s.contractCommandArgs(c, contractCmdReflect, contractFill{ChallengeID: "C-186", ArtifactRef: "feat/integration-1", EmptySource: true})
		if err != nil {
			t.Fatal(err)
		}
		want := join(pre, []string{"start", "--workspace", ws}, delegated, []string{"--model", "A=y", "--model", "W=x"},
			[]string{"--input", "artifact_ref=feat/integration-1", "--input", "challenge_id=tom-C-186", "reflect"})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("reflect = %q\nwant    %q", got, want)
		}
	})
	t.Run("AC-78 状態を読む", func(t *testing.T) {
		got, err := s.contractCommandArgs(flowConnector(t, ""), contractCmdStatus, contractFill{RunID: "run-77"})
		if want := join(pre, []string{"status", "--workspace", ws, "run-77"}); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("status = %q err %v\nwant     %q", got, err, want)
		}
	})
	t.Run("AC-80 取り消す", func(t *testing.T) {
		got, err := s.contractCommandArgs(flowConnector(t, ""), contractCmdCancel, contractFill{RunID: "run-77"})
		if want := join(pre, []string{"cancel", "--workspace", ws, "run-77"}); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("cancel = %q err %v\nwant     %q", got, err, want)
		}
	})
	t.Run("AC-79 続ける: 選んだもの・添え書き・通した中身は在るものだけを付ける", func(t *testing.T) {
		c := flowConnector(t, "")
		for _, tc := range []struct {
			name   string
			fill   contractFill
			wantIn []string
		}{
			{"全部", contractFill{RunID: "run-77", Answer: contractAnswer{Choice: "land", Note: "n"}, ApprovedRef: "abc"}, []string{"--input", "land", "--note", "n", "--approved-ref", "abc"}},
			{"選んだものだけ", contractFill{RunID: "run-77", Answer: contractAnswer{Choice: "respond"}}, []string{"--input", "respond"}},
			{"何も無い", contractFill{RunID: "run-77"}, nil},
		} {
			got, err := s.contractCommandArgs(c, contractCmdResume, tc.fill)
			want := join(pre, []string{"resume", "--workspace", ws}, delegated, tc.wantIn, []string{"run-77"})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("%s: resume = %q err %v\nwant %q", tc.name, got, err, want)
			}
		}
	})
	t.Run("実行ファイルのパスが渡されていなければ組み立てない", func(t *testing.T) {
		bare := newStoreForTest(t)
		if _, err := bare.contractCommandArgs(flowConnector(t, ""), contractCmdStatus, contractFill{RunID: "r"}); err == nil {
			t.Fatal("want an error when the executable path is not set")
		}
	})
}

func argsContain(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestWithDelegatedRun(t *testing.T) {
	t.Parallel()
	in := []string{"x", "flow", "resume", "--delegated-run", delegatedRunPlaceholder, "--note", delegatedRunPlaceholder, "ext"}
	got := withDelegatedRun(in, "R-5")
	want := []string{"x", "flow", "resume", "--delegated-run", "R-5", "--note", delegatedRunPlaceholder, "ext"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q（添え書きの値は差し替えない）", got, want)
	}
	if in[4] != delegatedRunPlaceholder {
		t.Fatal("the input must not be modified")
	}
	cli := []string{"tool", "start", "--challenge", "C-1"}
	if got := withDelegatedRun(cli, "R-5"); !reflect.DeepEqual(got, cli) {
		t.Fatalf("cli command = %q, want it unchanged", got)
	}
}

// ---- 起動（AC-74・AC-79・AC-82・AC-83） ----

func newFlowContractFixture(t *testing.T, extra string) *contractFixture {
	t.Helper()
	return newContractFixtureWith(t, flowOneRepoConnectorsJSON(extra))
}

func (f *contractFixture) runIDOfLastCommand(t *testing.T) string {
	t.Helper()
	return f.queryString(t, `SELECT 'R-' || MAX(id) FROM run`).String
}

// AC-74: start の --delegated-run は、そのコマンドの run の ID。その run の行は、起動の前に作られている。
func TestFlowLaunch_AC74_StartDelegatedRunIsTheRunOfTheCommand(t *testing.T) {
	t.Parallel()
	f := newFlowContractFixture(t, `, "challenge_id_prefix": "tom-"`)
	if _, err := f.start(t); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v, want one", f.client.starts)
	}
	cmd := f.client.starts[0]
	if cmd[0] != flowTestExe || cmd[1] != "flow" || cmd[2] != "start" {
		t.Fatalf("start = %q, want the executable passed by the cli and flow start", cmd)
	}
	wantRun := f.queryString(t, `SELECT 'R-' || start_run_id FROM contract_run`).String
	if got := argAfter(t, cmd, "--delegated-run"); got != wantRun || strings.Contains(got, "{") {
		t.Fatalf("--delegated-run = %q, want the run of the start command %q", got, wantRun)
	}
	if got := cmd[len(cmd)-1]; got != "ticket" {
		t.Errorf("last arg = %q, want the flow of the operation", got)
	}
	if !containsPair(cmd, "--input", "challenge_id=tom-"+f.cid) {
		t.Errorf("start = %q, want challenge_id with the prefix", cmd)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM slot WHERE run_id IS NOT NULL`); n != 0 { // AC-82
		t.Errorf("busy slots = %d, want none (a flow run does not use a slot)", n)
	}
}

// AC-79・AC-78・AC-80: 状態を読む・続ける・取り消すは表の並びで起動し、続けるの --delegated-run は、その resume の run の ID。
func TestFlowLaunch_AC78_AC79_AC80_StatusResumeCancelCommands(t *testing.T) {
	t.Parallel()
	f := newFlowContractFixture(t, "")
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t) // 状態を読む → 検証中
	if len(f.client.statuses) != 1 {
		t.Fatalf("statuses = %v", f.client.statuses)
	}
	if want := []string{flowTestExe, "flow", "status", "--workspace", f.s.Workspace(), "ext-1"}; !reflect.DeepEqual(f.client.statuses[0], want) {
		t.Fatalf("status = %q, want %q", f.client.statuses[0], want)
	}
	// 完了の検証が満たさないと、差し戻しが respond で返る（続ける）。
	j := &j5Fixture{delegateFixture: f.delegateFixture, checks: newFakeChecks(), j5: &fakeJudgmentInvoker{result: j5Out("not_met", "FEEDBACK-TEXT", nil)}}
	j.checks.prs["o/r#1"] = prChecks("open", checkDone)
	f.setPRForFlow(1)
	if _, err := f.s.VerifyAutoJ5(context.Background(), j.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	f.poll(t)
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %v, want one respond", f.client.resumes)
	}
	r := f.client.resumes[0]
	wantRun := f.runIDOfLastCommand(t)
	if r[2] != "resume" || argAfter(t, r, "--delegated-run") != wantRun || argAfter(t, r, "--input") != "respond" || r[len(r)-1] != "ext-1" || !argsContain(r, "--note") {
		t.Fatalf("resume = %q, want resume … --delegated-run %s --input respond --note … ext-1", r, wantRun)
	}
	// 取り消す（課題の取り下げ）。
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	if err := f.s.cancelAllOpenForTest(t, f); err != nil {
		t.Fatal(err)
	}
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %v, want one", f.client.cancels)
	}
	if want := []string{flowTestExe, "flow", "cancel", "--workspace", f.s.Workspace(), "ext-1"}; !reflect.DeepEqual(f.client.cancels[0], want) {
		t.Fatalf("cancel = %q, want %q", f.client.cancels[0], want)
	}
}

func (f *contractFixture) setPRForFlow(n int) {
	f.branches.pulls[releaseKey(n)] = UpstreamPullRequest{URL: "https://github.com/o/r/pull/" + "1", Title: "PR TITLE", State: "open", Base: "main"}
}

// cancelAllOpenForTest は課題の終わっていない契約の実行を取り消す（CancelContractRuns）。
func (s *Store) cancelAllOpenForTest(t *testing.T, f *contractFixture) error {
	t.Helper()
	_, err := s.CancelOpenContractRuns(context.Background(), ContractCancelInput{
		ChallengeID: f.cid, AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
	})
	return err
}

// AC-83: 作業している実行の数が同時に走らせる数の上限に達しているとき、次のタスクを始めない（flow の形も、同時に走らせる数に数える）。
func TestFlowLaunch_AC83_AtTheParallelLimitTheNextTaskIsNotStarted(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 1)
	conn, err := parseConnectorsDeclaration([]byte(flowMultiConnectorsJSON()))
	if err != nil {
		t.Fatal(err)
	}
	f.conn = conn
	f.newMulti(t, "first", "P1", false, multiTask{key: "a"})
	f.runDelegationStage(t, nil)
	if len(f.client.starts) != 1 || f.client.starts[0][1] != "flow" {
		t.Fatalf("starts = %q, want one flow start", f.client.starts)
	}
	other := f.newMulti(t, "second", "P2", false, multiTask{key: "x"})
	res := f.runDelegationStage(t, nil)
	if got := notStartedByReason(res, NotStartedSlotUnavailable); len(got) != 1 || got[0].ChallengeID != other {
		t.Fatalf("not_started = %+v, want slot_unavailable for %s", res.NotStarted, other)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %d, want still 1", len(f.client.starts))
	}
	if n := f.count(t, `SELECT COUNT(*) FROM slot WHERE run_id IS NOT NULL`); n != 0 { // AC-82
		t.Errorf("busy slots = %d, want none", n)
	}
}

// AC-84: required_facts を満たさないレビュー待ちは、cli の形と同じに受け取らず、同じ契約の run へ respond を返す。
func TestFlowLaunch_AC84_ReviewWithoutRequiredFactsIsNotReceived(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, flowOneRepoConnectorsJSON(`, "required_facts": {"self_review": ["complete"]}`))
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"self_review": "incomplete"}))
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress (not verifying)", got)
	}
	if len(f.client.resumes) != 1 || argAfter(t, f.client.resumes[0], "--input") != "respond" {
		t.Fatalf("resumes = %q, want one respond", f.client.resumes)
	}
	// 満たす facts のレビュー待ちは受け取る（検証中へ進む）。
	g := newIntakeFixture(t, flowOneRepoConnectorsJSON(`, "required_facts": {"self_review": ["complete"]}`))
	g.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"self_review": "complete"}))
	if got := g.status(t, g.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
}

// ---- 通し（AC-85・AC-86） ----

type landTrace struct {
	statuses  []Status
	approvals int64
	opState   string
	landed    string
	runEnded  bool
}

// landScenario は検証中のタスクの実行を、完了と反映の承認の後に「入れてよい」を返し、入ったことを読んで終えるまで進める
// （契約のコマンドの出力の並びは、cli の形でも flow の形でも同じ）。
func landScenario(t *testing.T, connectorsJSON string, facts map[string]string) (*landFixture, landTrace) {
	t.Helper()
	f := newLandFixtureWith(t, connectorsJSON, facts)
	f.sendLand(t)
	f.merged(f.head)
	f.client.queue = []ContractLaunchOutput{f.endedOut(ContractStateSucceeded)}
	if item := f.release(t); item.Result != ExecutionResultLanded {
		t.Fatalf("item = %+v, want landed", item)
	}
	tr := landTrace{
		statuses:  append(append([]Status(nil), f.trace...), f.status(t, f.cid)),
		approvals: f.count(t, `SELECT COUNT(*) FROM approval`),
		opState:   f.opState(t),
		runEnded:  f.runEnded(t),
	}
	tr.landed, _ = f.landRow(t)
	return f, tr
}

// AC-85・AC-86: flow の形で、状態は 着手中 → 検証中 → 完了確認待ち → 完了 の順に変わり、cli の形（同じ出力を返す）と、状態の変わる順・
// 承認の記録の件数・不可逆操作の状態が一致する。
func TestFlowLaunch_AC85_AC86_StatesApprovalsAndOperationMatchTheCLIForm(t *testing.T) {
	t.Parallel()
	_, cli := landScenario(t, landConnectorsJSON, nil)
	flow, got := landScenario(t, flowOneRepoConnectorsJSON(`, "required_facts": {"self_review": ["complete"]}`), map[string]string{"self_review": "complete"})

	want := []Status{StatusInProgress, StatusVerifying, StatusAwaitingCompletionApproval, StatusDone}
	if !reflect.DeepEqual(got.statuses, want) {
		t.Fatalf("flow statuses = %v, want %v", got.statuses, want)
	}
	if !reflect.DeepEqual(got, cli) {
		t.Fatalf("flow trace = %+v, cli trace = %+v, want identical", got, cli)
	}
	if got.approvals == 0 || got.opState != "executed" || !got.runEnded {
		t.Fatalf("trace = %+v, want approvals recorded, the operation executed and the run ended", got)
	}
	// 「入れてよい」は、続けるのコマンドで、land と通した中身を付けて返す。
	if len(flow.client.resumes) != 1 {
		t.Fatalf("resumes = %q, want one land", flow.client.resumes)
	}
	r := flow.client.resumes[0]
	if r[2] != "resume" || argAfter(t, r, "--input") != "land" || argAfter(t, r, "--approved-ref") != flow.head || argsContain(r, "--note") ||
		argAfter(t, r, "--permission-mode") != "auto" || r[len(r)-1] != "ext-1" {
		t.Fatalf("resume = %q, want resume … --input land --approved-ref %s ext-1", r, flow.head)
	}
	if rid := argAfter(t, r, "--delegated-run"); !runIDPattern.MatchString(rid) {
		t.Fatalf("--delegated-run = %q, want a run id (R-<n>)", rid)
	}
}

// ---- 統合先を使う課題（AC-87） ----

// flowShimClient は flow のコマンドを cli の形のコマンドへ読み替え、台本の委譲先（integFlowClient）へ渡す。
type flowShimClient struct {
	mu    sync.Mutex
	inner *integFlowClient
	raw   [][]string // 届いた flow のコマンド（start のもの）
}

func legacyCommand(cmd []string) []string {
	verb, last := cmd[2], cmd[len(cmd)-1]
	switch verb {
	case "start":
		if last == "reflect" {
			return []string{"tool", "reflect"}
		}
		for _, a := range cmd {
			if v, ok := strings.CutPrefix(a, "task_id="); ok {
				return []string{"tool", "start", "--task", v}
			}
		}
	case "resume":
		out := []string{"tool", "resume", last}
		for i, a := range cmd {
			if a == "--input" && i+1 < len(cmd) {
				out = append(out, "--choice", cmd[i+1])
			}
		}
		return out
	}
	return []string{"tool", verb, last}
}

func (c *flowShimClient) with(in ContractLaunchInput) ContractLaunchInput {
	in.Command = legacyCommand(in.Command)
	return in
}

func (c *flowShimClient) Start(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	c.mu.Lock()
	c.raw = append(c.raw, in.Command)
	c.mu.Unlock()
	return c.inner.Start(ctx, c.with(in))
}
func (c *flowShimClient) Status(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	return c.inner.Status(ctx, c.with(in))
}
func (c *flowShimClient) Resume(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	return c.inner.Resume(ctx, c.with(in))
}
func (c *flowShimClient) Cancel(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	return c.inner.Cancel(ctx, c.with(in))
}

// AC-87: flow の形の、統合先を使う課題（タスク 2 件）で、タスクごとのレビュー待ちと、統合先を上げる反映の依頼の出力を順に返すと、
// 2 件のタスクが済みになった後に、反映の依頼のコマンドが 1 回だけ起動する。
func TestFlowLaunch_AC87_ReflectStartsOnceAfterBothTasksAreDone(t *testing.T) {
	t.Parallel()
	shim := &flowShimClient{}
	f, id, _, res, _ := runIntegrationCycleWith(t, []string{"met"}, shim)

	if got := f.statusOf(t, id); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval (end_reason %q, starts %q)", got, res.Cycle.EndReason, shim.raw)
	}
	cid, _ := parseChallengeID(id)
	for _, key := range []string{"a", "b"} {
		if got := taskStatusMap(t, f.s, cid)[key]; got != TaskStatusDone {
			t.Errorf("task %s = %s, want done", key, got)
		}
	}
	var tickets, reflects [][]string
	for _, c := range shim.raw {
		if c[len(c)-1] == "reflect" {
			reflects = append(reflects, c)
		} else {
			tickets = append(tickets, c)
		}
	}
	if len(tickets) != 2 || len(reflects) != 1 {
		t.Fatalf("flow starts = %q, want 2 tickets and exactly 1 reflect", shim.raw)
	}
	r := reflects[0]
	runID := f.queryString(t, `SELECT 'R-' || start_run_id FROM contract_run WHERE kind = 'reflect'`).String
	want := []string{flowTestExe, "flow", "start", "--workspace", f.s.Workspace(), "--permission-mode", "auto", "--delegated-run", runID,
		"--input", "artifact_ref=" + upIntegRef, "--input", "challenge_id=" + id, "reflect"}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("reflect = %q\nwant      %q", r, want)
	}
}

// ---- 計画を作る判断点の出力の型（AC-145） ----

// AC-145: タスクの欄の一覧に、モデルの欄が無い（型の欄の一覧は、flow-port の前と同じ）。
func TestFlowLaunch_AC145_J2TaskFieldsHaveNoModel(t *testing.T) {
	t.Parallel()
	var schema struct {
		Properties struct {
			Tasks struct {
				Items struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"items"`
			} `json:"tasks"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(j2OutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range schema.Properties.Tasks.Items.Properties {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"body", "budget_impl_usd", "budget_review_usd", "depends_on", "done_criteria", "expand_after", "key", "non_goals", "operation", "repo", "source", "title"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("task fields = %v, want %v", got, want)
	}
}
