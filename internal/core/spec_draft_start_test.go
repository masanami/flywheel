package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sdsClient は開始のコマンドが呼ばれた時点を確かめられる契約の偽。
type sdsClient struct {
	*fakeContractClient
	onStart func(ContractLaunchInput)
}

func (c *sdsClient) Start(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	if c.onStart != nil {
		c.onStart(in)
	}
	return c.fakeContractClient.Start(ctx, in)
}

type sdsFixture struct {
	*delegateFixture
	client *sdsClient
}

// newSDSFixture は、下書きづくりの対象にできるリポジトリ（flow-wt）を宣言に足した委譲の足場。開始と状態の確認のコマンドは running を返す。
func newSDSFixture(t *testing.T) *sdsFixture {
	t.Helper()
	f := &sdsFixture{delegateFixture: newDelegateFixture(t),
		client: &sdsClient{fakeContractClient: &fakeContractClient{uniqueRunIDs: true, startOut: contractOut(ContractStateRunning, nil, nil), last: contractOut(ContractStateRunning, nil, nil)}}}
	sd := specDraftConn(t, specDraftConnectorsJSON)
	flowConn := sd.Connectors[0]
	flowConn.ID = "sdflow"
	repo := sd.Repos[0]
	repo.Connector = "sdflow"
	f.conn.Connectors = append(f.conn.Connectors, flowConn)
	f.conn.Repos = append(f.conn.Repos, repo)
	return f
}

func (f *sdsFixture) flowConnector() *Connector { return f.conn.connectorByID("sdflow") }

func (f *sdsFixture) classified(t *testing.T, title string) string {
	t.Helper()
	ch, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: title, Description: "DESC-" + title})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ClassifyChallenge(context.Background(), ChannelCLI, ch.ID, ClassifyInput{Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	return ch.ID
}

// record は分類済の課題と、実行待ちの記録を作る。mut で記録の元（置き換える元・答えなど）を変える。
func (f *sdsFixture) record(t *testing.T, title string, d SpecDraft) string {
	t.Helper()
	id := f.classified(t, title)
	cid, _ := parseChallengeID(id)
	d.Path, d.Repo, d.Reason = specDraftPathFor(cid), "flow-wt", needsSpecReason
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		if err := insertSpecDraft(ctx, tx, cid, d, time.Now()); err != nil {
			return err
		}
		state := d.State
		if state == "" {
			state = SpecDraftStatePending
		}
		return updateSpecDraft(ctx, tx, cid, specDraftUpdate{State: state, Attempts: d.Attempts, Questions: d.Questions, Answers: d.Answers}, time.Now())
	})
	return id
}

func (f *sdsFixture) write(t *testing.T, fn func(context.Context, *sql.Tx) error) {
	t.Helper()
	ctx := context.Background()
	if err := f.s.db.Write(ctx, func(tx *sql.Tx) error { return fn(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
}

func (f *sdsFixture) delegateIn(t *testing.T, cycleID string) *DelegateResult {
	t.Helper()
	in := f.input(cycleID, nil)
	in.Contract = f.client
	res, err := f.s.RunDelegation(context.Background(), in)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	return res
}

// delegate は新しいサイクルで委譲の段を 1 回走らせ、サイクルを終える。
func (f *sdsFixture) delegate(t *testing.T, budget float64) *DelegateResult {
	t.Helper()
	c := f.cycle(t, budget)
	res := f.delegateIn(t, c)
	if _, err := f.s.EndCycle(context.Background(), c, CycleResultCompleted); err != nil {
		t.Fatal(err)
	}
	return res
}

func (f *sdsFixture) detail(t *testing.T, id string) *ChallengeDetail {
	t.Helper()
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *sdsFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func sdsArg(command []string, prefix string) string {
	for _, a := range command {
		if v, ok := strings.CutPrefix(a, prefix); ok {
			return v
		}
	}
	return ""
}

func sdsRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// requireSpecFailed は、課題が理由 spec_failed の保留で人間対応待ちで、記録の回数が attempts であることを確かめる。
func (f *sdsFixture) requireSpecFailed(t *testing.T, id string, attempts int) Hold {
	t.Helper()
	d := f.detail(t, id)
	sd := d.SpecDraft
	if d.Status != StatusAwaitingHuman || sd == nil || sd.State != SpecDraftStateAwaitingOwner || sd.Attempts != attempts ||
		sd.HoldReason == nil || *sd.HoldReason != SpecDraftHoldFailed {
		t.Fatalf("status=%s spec_draft=%+v, want awaiting_human / spec_failed / attempts %d", d.Status, sd, attempts)
	}
	for _, h := range d.Holds {
		if h.Open() {
			return h
		}
	}
	t.Fatal("no open hold")
	return Hold{}
}

func (f *sdsFixture) requireRecord(t *testing.T, id string, state SpecDraftState, attempts int) {
	t.Helper()
	sd := f.detail(t, id).SpecDraft
	if sd.State != state || sd.Attempts != attempts {
		t.Fatalf("spec_draft = %+v, want %s / attempts %d", sd, state, attempts)
	}
}

// AC-51・53・54・57〜63・65・66・68〜73・114・117
func TestStartSpecDraft_CommandAndRecords(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	f.flowConnector().ChallengeIDPrefix = "pre-"
	id := f.record(t, "start", SpecDraft{})
	var approvalsAtStart int
	f.client.onStart = func(ContractLaunchInput) {
		approvalsAtStart = len(f.detail(t, id).Approvals)
	}
	res := f.delegate(t, 100)
	if len(f.client.starts) != 1 || len(res.NotStarted) != 0 {
		t.Fatalf("starts = %v, not started = %+v", f.client.starts, res.NotStarted)
	}
	if approvalsAtStart != 0 || len(f.detail(t, id).Approvals) != 0 {
		t.Fatalf("approvals = %d, want none", approvalsAtStart)
	}
	runs, err := f.s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v %v", runs, err)
	}
	runID := runs[0].ID
	ws := f.s.Workspace()
	runDir := filepath.Join(ws, ".flywheel", "runs", runID)
	cid, _ := parseChallengeID(id)
	want := []string{flowTestExe, "flow", "start", "--workspace", ws, "--permission-mode", "auto", "--delegated-run", runID,
		"--input", "draft_file=c-" + strconv.FormatInt(cid, 10) + ".md",
		"--input", "brief_file=" + filepath.Join(runDir, "spec-brief.md"),
		"--input", "brief_dir=" + runDir,
		"--input", "repo_dir=" + filepath.Join(ws, "w1"),
		"--input", "challenge_id=pre-" + id,
		"--budget-usd", "15", "spec-draft"}
	if !slices.Equal(f.client.starts[0], want) {
		t.Fatalf("command =\n%q\nwant\n%q", f.client.starts[0], want)
	}
	workDir := filepath.Join(ws, ".flywheel", "specs")
	if got := f.client.workDirs["start"]; !slices.Equal(got, []string{workDir}) {
		t.Fatalf("work dir = %v", got)
	}
	f.requireRecord(t, id, SpecDraftStateRunning, 1)
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND kind = 'spec' AND work_dir = ? AND task_id IS NULL
		AND max_budget_usd = ? AND ended_at IS NULL`, cid, workDir, usdToMicros(15)); n != 1 {
		t.Fatalf("spec contract runs = %d, want 1", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE challenge_id = ? AND budget_bucket = 'spec' AND task_id IS NULL AND max_budget_usd = ?`,
		cid, usdToMicros(15)); n != 1 {
		t.Fatalf("spec runs = %d, want 1", n)
	}
}

// AC-52
func TestRunCycle_StartsTheSpecDraftInTheCycleThatDecidedIt(t *testing.T) {
	t.Parallel()
	f := newCycleFixture(t)
	f.conn = specDraftConn(t, specDraftConnectorsJSON)
	f.s.SetFlowExecutable(flowTestExe)
	ch := f.classified(t, "needs a spec", "P1")
	client := &fakeContractClient{startOut: contractOut(ContractStateRunning, nil, nil)}
	in := f.input(&cycleFakeInvoker{handle: func(cycleCall) JudgmentLaunchOutput { return succeeded(needsSpecOutput("flow-wt", nil)) }})
	// 下書きづくりの実行が開いている間、サイクルは回りを続けるので、開始のコマンドの中で停止を指示して、この回りで終える。
	in.Contract = &sdsClient{fakeContractClient: client, onStart: func(ContractLaunchInput) {
		if _, err := f.s.RequestCycleStop(context.Background()); err != nil {
			t.Error(err)
		}
	}}
	f.run(t, in)
	if len(client.starts) != 1 {
		t.Fatalf("starts = %v, want 1", client.starts)
	}
	d, err := f.s.GetChallenge(context.Background(), ch.ID)
	if err != nil || d.SpecDraft == nil || d.SpecDraft.State != SpecDraftStateRunning {
		t.Fatalf("detail = %+v %v", d, err)
	}
}

// AC-55・56・104・105・106
func TestStartSpecDraft_NotStartedForIneligibleChallenges(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		state  SpecDraftState
		status Status
	}{
		"AC-55 取り下げ":         {SpecDraftStatePending, StatusWithdrawn},
		"AC-56 ほかの理由の人間対応待ち": {SpecDraftStatePending, StatusAwaitingHuman},
		"AC-104 回答待ち":        {SpecDraftStateAwaitingOwner, StatusClassified},
		"AC-105 済み":          {SpecDraftStateDone, StatusClassified},
		"AC-106 省いた":         {SpecDraftStateSkipped, StatusClassified},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSDSFixture(t)
			id := f.record(t, "x", SpecDraft{State: tc.state})
			cid, _ := parseChallengeID(id)
			f.write(t, func(ctx context.Context, tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `UPDATE challenge SET status = ? WHERE id = ?`, string(tc.status), cid)
				return err
			})
			f.delegate(t, 100)
			if len(f.client.starts) != 0 {
				t.Fatalf("starts = %v", f.client.starts)
			}
			f.requireRecord(t, id, tc.state, 0)
		})
	}
}

// AC-103
func TestStartSpecDraft_RunningWithAnOpenRunIsLeftAlone(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	id := f.record(t, "x", SpecDraft{})
	f.delegate(t, 100)
	f.delegate(t, 100)
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v, want 1", f.client.starts)
	}
	f.requireRecord(t, id, SpecDraftStateRunning, 1)
}

// AC-64・74〜79・82〜84・87・88・91〜94
func TestStartSpecDraft_FilesExistWhenTheCommandIsCalled(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	base := "docs/features/old.md"
	q := shQuestions()
	id := f.record(t, "全部を持つ課題", SpecDraft{BasePath: &base, Attempts: 1, Questions: q, Answers: []SpecDraftAnswer{
		{Attempt: 1, Question: "問い A", Answer: "答え A", By: "owner"},
		{Attempt: 1, Question: "問い B", Answer: "答え B\n2 行目", By: "someone-new"},
	}})
	ch := &Challenge{ID: id}
	(&j2Fixture{s: f.s, upstream: f.upstream}).bind(t, ch, 7, simpleThread("上流の本文"))
	f.git.changedPaths = map[string][]string{filepath.Join(f.s.Workspace(), "w1"): {"a.txt", "dir/b.go"}}
	var brief, status string
	f.client.onStart = func(in ContractLaunchInput) {
		brief = sdsRead(t, sdsArg(in.Command, "brief_file="))
		status = sdsRead(t, filepath.Join(in.RunDir, "repo-status-before.txt"))
	}
	f.delegate(t, 100)

	if status != "a.txt\ndir/b.go\n" {
		t.Fatalf("repo-status-before.txt = %q", status)
	}
	d := f.detail(t, id)
	want := "# 全部を持つ課題\n\n## 説明\nDESC-全部を持つ課題\n\n## 取り込み元\n- 指定: o/r#7\n- URL: " + d.SourceBinding.URL +
		"\n\n## 回す理由\n" + needsSpecReason + "\n\n## 置き換える元\n" + base +
		"\n\n## これまでの問いと答え\n### 1-1\n- 問い: 問い A\n- 答え: 答え A\n- 答えた者: owner\n### 1-2\n- 問い: 問い B\n- 答え: 答え B\n  2 行目\n- 答えた者: someone-new\n"
	if brief != want {
		t.Fatalf("brief =\n%s\nwant\n%s", brief, want)
	}
	command := f.client.starts[0]
	i := slices.Index(command, "repo_dir="+filepath.Join(f.s.Workspace(), "w1"))
	if i < 0 || command[i+2] != "base_path="+base {
		t.Fatalf("base_path does not follow repo_dir: %q", command)
	}
	// AC-73・74: 回数が 1 増え、問いは空になり、答えは残る。
	if d.SpecDraft.Attempts != 2 || d.SpecDraft.Questions != nil || len(d.SpecDraft.Answers) != 2 {
		t.Fatalf("spec_draft = %+v", d.SpecDraft)
	}
	// AC-80: 上流の Issue を読まない。
	if len(f.upstream.threadCalls)+len(f.upstream.refCalls) != 0 {
		t.Fatalf("upstream was read: %v %v", f.upstream.threadCalls, f.upstream.refCalls)
	}
}

// AC-65・67・81
func TestStartSpecDraft_WithoutOptionalParts(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	f.record(t, "最小", SpecDraft{})
	dir := filepath.Join(f.s.Workspace(), ".flywheel", "specs")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	f.delegate(t, 100)
	command := f.client.starts[0]
	if slices.ContainsFunc(command, func(a string) bool { return strings.HasPrefix(a, "base_path=") }) {
		t.Fatalf("command = %q", command)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf(".flywheel/specs was not created: %v", err)
	}
	want := "# 最小\n\n## 説明\nDESC-最小\n\n## 回す理由\n" + needsSpecReason + "\n"
	if got := sdsRead(t, sdsArg(command, "brief_file=")); got != want {
		t.Fatalf("brief = %q, want %q", got, want)
	}
}

// AC-85・86
func TestStartSpecDraft_BriefCannotBeWritten(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	id := f.record(t, "x", SpecDraft{})
	runs := filepath.Join(f.s.Workspace(), ".flywheel", "runs")
	if err := os.MkdirAll(filepath.Dir(runs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runs, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.delegate(t, 100)
	if len(f.client.starts) != 0 {
		t.Fatalf("starts = %v", f.client.starts)
	}
	f.requireSpecFailed(t, id, 1)
}

// AC-89・90
func TestStartSpecDraft_ChangedPathsCannotBeRead(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	id := f.record(t, "x", SpecDraft{})
	f.git.changedErr = errors.New("git broke")
	f.delegate(t, 100)
	if len(f.client.starts) != 0 {
		t.Fatalf("starts = %v", f.client.starts)
	}
	if h := f.requireSpecFailed(t, id, 1); !strings.Contains(h.Question, "git broke") {
		t.Fatalf("question = %q", h.Question)
	}
}

// AC-95・96・97
func TestStartSpecDraft_ParallelLimit(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	f.agent.MaxParallelRuns = 1
	first := f.record(t, "first", SpecDraft{})
	second := f.record(t, "second", SpecDraft{})
	res := f.delegate(t, 100)
	if len(f.client.starts) != 1 || len(res.NotStarted) != 0 {
		t.Fatalf("starts = %v, not started = %+v", f.client.starts, res.NotStarted)
	}
	f.requireRecord(t, first, SpecDraftStateRunning, 1)
	f.requireRecord(t, second, SpecDraftStatePending, 0)
	res = f.delegate(t, 100)
	if len(f.client.starts) != 1 || len(res.NotStarted) != 0 {
		t.Fatalf("starts = %v, not started = %+v", f.client.starts, res.NotStarted)
	}
}

// AC-98
func TestStartSpecDraft_RunningSpecDraftFillsTheParallelLimit(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	f.agent.MaxParallelRuns = 1
	f.record(t, "spec", SpecDraft{})
	f.delegate(t, 100)
	task := f.newInProgress(t, "task", "P1", planSpec(nil))
	res := f.delegate(t, 100)
	if len(f.deleg.launched()) != 0 {
		t.Fatalf("the task was delegated while the spec draft runs")
	}
	if !slices.ContainsFunc(res.NotStarted, func(n NotStarted) bool { return n.ChallengeID == task }) {
		t.Fatalf("not started = %+v", res.NotStarted)
	}
}

// AC-99・100・101
func TestStartSpecDraft_CycleBudget(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	id := f.record(t, "x", SpecDraft{})
	res := f.delegate(t, 14.999999)
	if len(f.client.starts) != 0 || len(res.NotStarted) != 0 {
		t.Fatalf("starts = %v, not started = %+v", f.client.starts, res.NotStarted)
	}
	f.requireRecord(t, id, SpecDraftStatePending, 0)
	f.delegate(t, 15)
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v, want 1", f.client.starts)
	}
}

// AC-107・108・109、終わった状態で返った開始
func TestStartSpecDraft_FailedStart(t *testing.T) {
	t.Parallel()
	for name, out := range map[string]ContractLaunchOutput{
		"起動できない":   {Result: RunResultLaunchFailed, ErrorSummary: "no such file"},
		"出力が不正":    {Result: RunResultMalformed, ErrorSummary: "not json"},
		"失敗で返った":   contractOut(ContractStateFailed, nil, nil),
		"成功で返った":   contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) { st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "o/wt#1"}} }),
		"取り消しで返った": contractOut(ContractStateCancelled, nil, nil),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSDSFixture(t)
			f.client.startOut = out
			id := f.record(t, "x", SpecDraft{})
			res := f.delegate(t, 100)
			if len(res.NotStarted) != 0 {
				t.Fatalf("not started = %+v", res.NotStarted)
			}
			f.requireSpecFailed(t, id, 1)
			cid, _ := parseChallengeID(id)
			// 下書きづくりの実行は、終わった状態でも成果物を取り込まない。
			if n := f.count(t, `SELECT COUNT(*) FROM run_artifact`); n != 0 {
				t.Fatalf("artifacts = %d", n)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ?`, cid); n != 1 {
				t.Fatalf("holds = %d, want 1", n)
			}
		})
	}
}

// AC-110
func TestStartSpecDraft_RunningWithoutAnOpenRunIsHeld(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	id := f.record(t, "x", SpecDraft{})
	f.delegate(t, 100)
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE contract_run SET ended_at = ?`, formatTimestamp(time.Now()))
		return err
	})
	f.delegate(t, 100)
	f.requireSpecFailed(t, id, 1)
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v", f.client.starts)
	}
}

// 開始の失敗を記録へ写す前に止まった（回収された）下書きづくりは、始め直さず、回数に数えて失敗の保留にする。
// タスクの契約の開始の回収の保留は作らない。
func TestStartSpecDraft_InterruptedStartIsHeld(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	id := f.record(t, "x", SpecDraft{})
	c := f.cycle(t, 100)
	in := f.input(c, nil)
	in.Contract = f.client
	sc, err := f.s.beginSpecDraftRun(context.Background(), in, mustChallengeID(t, id))
	if err != nil || sc == nil {
		t.Fatalf("begin = %+v %v", sc, err)
	}
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		_, err := updateRunEnd(ctx, tx, sc.runID, updateRunEndInput{EndedAt: time.Now(), Result: RunResultInterrupted})
		return err
	})
	if n, err := f.s.HoldUnrecordedContractStops(context.Background()); err != nil || n != 0 {
		t.Fatalf("HoldUnrecordedContractStops = %d %v", n, err)
	}
	f.delegateIn(t, c)
	if len(f.client.starts) != 0 {
		t.Fatalf("starts = %v", f.client.starts)
	}
	f.requireSpecFailed(t, id, 1)
}

func mustChallengeID(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := parseChallengeID(id)
	if !ok {
		t.Fatalf("bad id %q", id)
	}
	return n
}

// 開いている下書きづくりの実行への状態の確認の実行が枠 spec でも、開始の実行と取り違えて回数を数えない。
func TestStartSpecDraft_LaterCommandRunIsNotAFailedStart(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	id := f.record(t, "x", SpecDraft{})
	f.delegate(t, 100)
	cid := mustChallengeID(t, id)
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		now := time.Now()
		row, err := insertRun(ctx, tx, cid, insertRunInput{Kind: runKindContract, ChallengeVersion: 1, SessionID: "s", PID: 1, Host: "h",
			HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketSpec})
		if err != nil {
			return err
		}
		n, _ := parseRunID(row.ID)
		later := now.Add(time.Second)
		if _, err := updateRunEnd(ctx, tx, n, updateRunEndInput{EndedAt: later, Result: RunResultSucceeded}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET ended_at = ?`, formatTimestamp(later)); err != nil {
			return err
		}
		return updateSpecDraft(ctx, tx, cid, specDraftUpdate{State: SpecDraftStatePending, Attempts: 1}, now)
	})
	f.delegate(t, 100)
	if len(f.client.starts) != 2 {
		t.Fatalf("starts = %v, want 2", f.client.starts)
	}
	f.requireRecord(t, id, SpecDraftStateRunning, 2)
}

// AC-111
func TestStartSpecDraft_DefaultPermissionModeStarts(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	f.flowConnector().PermissionMode = PermissionModeDefault
	f.record(t, "x", SpecDraft{})
	f.delegate(t, 100)
	if len(f.client.starts) != 1 || !strings.Contains(strings.Join(f.client.starts[0], " "), "--permission-mode default") {
		t.Fatalf("starts = %v", f.client.starts)
	}
}

// AC-112。AC-113 のうち開始まで（実行が終わって done になる所は、この処理の対象ではない）。
func TestStartSpecDraft_RestartsAfterTheRecordIsPendingAgain(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	f.client.startOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	id := f.record(t, "x", SpecDraft{})
	f.delegate(t, 100)
	f.requireSpecFailed(t, id, 1)
	cid := mustChallengeID(t, id)
	// retry の回答の後の形（保留を閉じ、分類済と実行待ちへ戻す）。
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE hold SET closed_at = ? WHERE challenge_id = ?`, formatTimestamp(time.Now()), cid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE challenge SET status = 'classified' WHERE id = ?`, cid); err != nil {
			return err
		}
		return updateSpecDraft(ctx, tx, cid, specDraftUpdate{State: SpecDraftStatePending, Attempts: 1}, time.Now())
	})
	f.client.startOut = contractOut(ContractStateRunning, nil, nil)
	f.delegate(t, 100)
	if len(f.client.starts) != 2 {
		t.Fatalf("starts = %v", f.client.starts)
	}
	f.requireRecord(t, id, SpecDraftStateRunning, 2)
}

// AC-114〜117
func TestStartSpecDraft_BudgetAmount(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		declared *float64
		max      float64
		want     string
		micros   int64
	}{
		"AC-114 既定":     {nil, 200, "15", usdToMicros(SpecDraftDefaultBudgetUSD)},
		"AC-115 宣言の値":   {float64Ptr(8), 200, "8", usdToMicros(8)},
		"AC-116 小さいほう":  {float64Ptr(8), 5, "5", usdToMicros(5)},
		"既定より max が小さい": {nil, 2.5, "2.5", usdToMicros(2.5)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSDSFixture(t)
			f.flowConnector().SpecDraftBudgetUSD, f.agent.MaxRunBudgetUSD = tc.declared, tc.max
			f.record(t, "x", SpecDraft{})
			f.delegate(t, 100)
			command := f.client.starts[0]
			if got := command[slices.Index(command, "--budget-usd")+1]; got != tc.want {
				t.Fatalf("--budget-usd = %s, want %s", got, tc.want)
			}
			if n := f.count(t, `SELECT max_budget_usd FROM contract_run WHERE kind = 'spec'`); int64(n) != tc.micros {
				t.Fatalf("max_budget_usd = %d, want %d", n, tc.micros)
			}
		})
	}
}

// 下書きづくりの実行は、反映の段が読む実行にもタスクの状態の確認にも入らず、取り下げの取り消しでは止まる。
func TestSpecContractRun_StaysOutOfTaskAndReleasePaths(t *testing.T) {
	t.Parallel()
	f := newSDSFixture(t)
	id := f.record(t, "x", SpecDraft{})
	f.delegate(t, 100)
	cid := mustChallengeID(t, id)
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND `+releaseStageRunWhere, cid); n != 0 {
		t.Fatalf("release stage reads the spec run")
	}
	if len(f.client.statuses) != 0 {
		t.Fatalf("statuses = %v", f.client.statuses)
	}
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	n, err := f.s.CancelOpenContractRuns(context.Background(), ContractCancelInput{ChallengeID: id, AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client})
	if err != nil || n != 1 {
		t.Fatalf("cancel = %d %v", n, err)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ?`, cid); got != 0 {
		t.Fatalf("holds = %d", got)
	}
}

// 実行待ちの記録と行き止まりの記録だけでも、サイクルを回すと進むものに数える。
func TestCountCycleWork_CountsSpecDrafts(t *testing.T) {
	t.Parallel()
	for name, state := range map[string]SpecDraftState{"実行待ち": SpecDraftStatePending, "行き止まり": SpecDraftStateRunning} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSDSFixture(t)
			f.record(t, "x", SpecDraft{})
			if state == SpecDraftStateRunning {
				f.delegate(t, 100)
				f.write(t, func(ctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `UPDATE contract_run SET ended_at = ?`, formatTimestamp(time.Now()))
					return err
				})
			}
			w, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
			if err != nil || w.Stages.Run != 1 || w.Total != 1 {
				t.Fatalf("work = %+v %v", w, err)
			}
		})
	}
}

// AC-102・118・119
func TestLoadConnectorsDeclaration_SpecDraftKeys(t *testing.T) {
	t.Parallel()
	flow := func(extra string) string { return flowConnectorJSON("t", "auto", flowOp, extra) }
	repo := `{"name": "r", "remote": "o/r", "default_branch": "main", "connector": "t", "slots": {"provider": "worktree", "base": "w", "count": 1}}`
	d, _ := loadFlowDecl(t, flow(`, "spec_draft_budget_usd": 8`), repo)
	if v := d.Connectors[0].SpecDraftBudgetUSD; v == nil || *v != 8 {
		t.Fatalf("spec_draft_budget_usd = %v", v)
	}
	for name, connector := range map[string]string{
		"AC-102 操作の flow に spec-draft": flowConnectorJSON("t", "auto", `, "flow": "spec-draft"`, ""),
		"AC-118 flow でない形":             `{"id": "t", "form": "brief", "permission_mode": "auto", "spec_draft_budget_usd": 8, "operations": [{"id": "op"}]}`,
		"AC-119 0":                     flow(`, "spec_draft_budget_usd": 0`),
		"負の数":                          flow(`, "spec_draft_budget_usd": -1`),
		"null":                         flow(`, "spec_draft_budget_usd": null`),
	} {
		if _, err := LoadConnectorsDeclaration(writeConnectorsJSON(t, declWith(connector, repo))); !errors.Is(err, ErrConfigInvalid) {
			t.Errorf("%s: err = %v, want ErrConfigInvalid", name, err)
		}
	}
}
