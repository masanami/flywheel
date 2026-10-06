package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// このファイルは #102（親要件チケット #98 §意思決定の主体の判定・§J3 ブリーフと委譲の起動）の
// Store.RunDelegation を、実ストア（t.TempDir() の SQLite）・偽の判断／委譲の IF・偽の上流の
// 取得・偽の SlotGit で検証する（AC-190〜217・281・365 の core 側）。

const delegateConnectorsJSON = `{
  "version": 1,
  "human_question_kinds": [{"id": "release_timing", "label": "リリース時期"}],
  "connectors": [
    {"id": "harness", "form": "plugin", "permission_mode": "acceptEdits", "operations": [
      {"id": "impl", "invocation": "/h:impl {challenge_id}", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
      {"id": "impl-src", "invocation": "/h:impl {issue_number} {challenge_id}", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
      {"id": "define-human", "invocation": "/h:def {challenge_id}", "interactive": true, "counterpart": "human", "artifacts": "pr"},
      {"id": "define-parent", "invocation": "/h:def {challenge_id}", "interactive": true, "counterpart": "parent", "artifacts": "pr"},
      {"id": "define-default", "invocation": "/h:def {challenge_id}", "artifacts": "pr"},
      {"id": "impl-branch", "invocation": "/h:ib {challenge_id}", "interactive": false, "child_may_decide": true, "artifacts": "branch"},
      {"id": "impl-none", "invocation": "/h:in {challenge_id}", "interactive": false, "child_may_decide": true, "artifacts": "none"},
      {"id": "quiet", "invocation": "/h:q {challenge_id}", "interactive": false, "artifacts": "pr"},
      {"id": "omitted", "invocation": "/h:o {challenge_id}", "child_may_decide": true, "artifacts": "pr"}
    ]},
    {"id": "direct", "form": "brief", "permission_mode": "default", "operations": [
      {"id": "brief", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]}
  ],
  "repos": [
    {"name": "main-repo", "remote": "o/r", "default_branch": "main", "connector": "harness", "slots": {"provider": "clone", "paths": ["slot1"]}},
    {"name": "sibling", "remote": "o/s", "default_branch": "main", "connector": "harness", "slots": {"provider": "clone", "paths": ["slot-s"]}},
    {"name": "direct-repo", "remote": "o/d", "default_branch": "main", "connector": "direct", "slots": {"provider": "clone", "paths": ["slot-d"]}}
  ]
}`

// fakeDelegator は DelegationInvoker の偽の実装。
type fakeDelegator struct {
	mu       sync.Mutex
	inputs   []DelegateLaunchInput
	result   JudgmentLaunchOutput
	block    chan struct{}
	started  chan struct{}
	onInvoke func(in DelegateLaunchInput)
	// queue が空でなければ、起動のたびに先頭を返して取り除く（無くなったら result）。
	queue []JudgmentLaunchOutput
}

func (f *fakeDelegator) InvokeDelegation(_ context.Context, in DelegateLaunchInput) (JudgmentLaunchOutput, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	f.mu.Unlock()
	if f.onInvoke != nil {
		f.onInvoke(in)
	}
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) > 0 {
		out := f.queue[0]
		f.queue = f.queue[1:]
		return out, nil
	}
	return f.result, nil
}

func (f *fakeDelegator) launched() []DelegateLaunchInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]DelegateLaunchInput(nil), f.inputs...)
}

func reportJSON(mut func(m map[string]any)) []byte {
	m := map[string]any{
		"outcome": "completed", "summary": "done", "branch": "feat/x", "pr_urls": []string{"https://github.com/o/r/pull/1"},
		"commits": []string{"abc123"}, "quality_gate": "pass", "assumptions": []string{}, "unverified": []string{}, "questions": []any{},
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func briefJSON(brief string) []byte { b, _ := json.Marshal(map[string]any{"brief": brief}); return b }

type delegateFixture struct {
	s        *Store
	agent    *AgentDeclaration
	conn     *ConnectorsDeclaration
	upstream *fakeUpstreamThreads
	git      *fakeSlotGit
	branches *fakeBranchSource
	j3       *fakeJudgmentInvoker
	deleg    *fakeDelegator
	j3Inputs []JudgmentLaunchInput
}

func newDelegateFixture(t *testing.T) *delegateFixture {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	conn, err := parseConnectorsDeclaration([]byte(delegateConnectorsJSON))
	if err != nil {
		t.Fatalf("parseConnectorsDeclaration: %v", err)
	}
	if err := conn.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	f := &delegateFixture{s: s, agent: defaultAgentDeclaration(), conn: conn, upstream: newFakeUpstreamThreads(), git: newFakeSlotGit(), branches: newFakeBranchSource()}
	for p, remote := range map[string]string{"slot1": "o/r", "slot-s": "o/s", "slot-d": "o/d"} {
		f.git.states[filepath.Join(s.Workspace(), p)] = SlotTreeState{Exists: true, PointerOK: true, OriginURL: "https://github.com/" + remote + ".git"}
	}
	f.j3 = &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: briefJSON("BRIEF-MARKER")}}
	f.j3.onInvoke = func(in JudgmentLaunchInput) { f.j3Inputs = append(f.j3Inputs, in) }
	f.deleg = &fakeDelegator{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: reportJSON(nil), ReportedTotalCostUSD: float64Ptr(2.5)}}
	return f
}

// planSpec は承認済みの計画の構造化した出力（J2 の plan の出力）。
func planSpec(mut func(m map[string]any)) string {
	return string(j2PlanOutput(func(m map[string]any) {
		m["repo"] = "main-repo"
		m["operation"] = "impl"
		if mut != nil {
			mut(m)
		}
	}))
}

// newInProgress は着手中で承認済みの計画（版 1。spec 付き）を持つ課題を作る。spec が空なら
// 構造化した出力の無い計画（人が登録した計画）にする。
func (f *delegateFixture) newInProgress(t *testing.T, title, priority, spec string) string {
	t.Helper()
	ctx := context.Background()
	ch, err := f.s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: title, Description: "DESC-" + title, DoneCriteria: "DC-" + title})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	if priority == "" {
		priority = "P2"
	}
	if _, err := f.s.ClassifyChallenge(ctx, ChannelCLI, ch.ID, ClassifyInput{Priority: priority}); err != nil {
		t.Fatalf("ClassifyChallenge: %v", err)
	}
	cid, _ := parseChallengeID(ch.ID)
	var specArg any
	if spec != "" {
		specArg = spec
	}
	err = f.s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 1, ?, ?, ?)`,
			cid, "PLAN-BODY-"+title, "2026-09-25T00:00:00.000Z", specArg); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, plan_version, actor, channel, verification, decided_at)
			VALUES (?, 'plan', 'approved', (SELECT version FROM challenge WHERE id = ?), 1, 'alice', 'cli', 'tty_confirm', '2026-09-25T00:00:00.000Z')`, cid, cid); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE challenge SET status = 'in_progress', version = version + 1 WHERE id = ?`, cid)
		return err
	})
	if err != nil {
		t.Fatalf("make in_progress: %v", err)
	}
	return ch.ID
}

func (f *delegateFixture) input(cycleID string, id *string) DelegateInput {
	return DelegateInput{ChallengeID: id, AgentDecl: f.agent, ConnDecl: f.conn, Judgment: f.j3, Delegate: f.deleg,
		Upstream: f.upstream, Git: f.git, Reconcile: f.branches, CycleID: cycleID}
}

func (f *delegateFixture) cycle(t *testing.T, budget float64) string {
	t.Helper()
	c, err := f.s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "run", BudgetUSD: budget, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	return c.ID
}

func (f *delegateFixture) run(t *testing.T, id *string) (*DelegateResult, error) {
	t.Helper()
	return f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), id))
}

func (f *delegateFixture) delegateRuns(t *testing.T) []runRow {
	t.Helper()
	var out []runRow
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM run WHERE kind = 'delegate' ORDER BY id`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		_ = rows.Close()
		for _, id := range ids {
			r, err := loadRunByID(context.Background(), tx, id)
			if err != nil {
				return err
			}
			out = append(out, *r)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read delegate runs: %v", err)
	}
	return out
}

func (f *delegateFixture) slotStates(t *testing.T) map[string]string {
	t.Helper()
	slots, err := f.s.ListSlots(context.Background())
	if err != nil {
		t.Fatalf("ListSlots: %v", err)
	}
	out := map[string]string{}
	for _, sl := range slots {
		out[filepath.Base(sl.Path)] = sl.State
	}
	return out
}

// --- 意思決定者の判定（AC-190〜199） ---

func TestDecideDecider_FiveRowsEvaluatedTopDown(t *testing.T) {
	cases := []struct {
		name    string
		op      ConnectorOperation
		cross   bool
		related []string
		want    Decider
		row     int
	}{
		{"row1 human", ConnectorOperation{Interactive: true, Counterpart: CounterpartHuman}, false, nil, DeciderHuman, 1},
		{"row1 beats row2", ConnectorOperation{Interactive: true, Counterpart: CounterpartHuman}, true, nil, DeciderHuman, 1},
		{"row2 cross_repo", ConnectorOperation{Interactive: false, ChildMayDecide: true}, true, nil, DeciderParent, 2},
		{"row2 two related repos", ConnectorOperation{Interactive: false, ChildMayDecide: true}, false, []string{"a", "b"}, DeciderParent, 2},
		{"one related repo is single-repo", ConnectorOperation{Interactive: false, ChildMayDecide: true}, false, []string{"a"}, DeciderChild, 4},
		{"row3 parent", ConnectorOperation{Interactive: true, Counterpart: CounterpartParent}, false, nil, DeciderParent, 3},
		{"row3 counterpart omitted", ConnectorOperation{Interactive: true}, false, nil, DeciderParent, 3},
		{"row3 interactive with child_may_decide", ConnectorOperation{Interactive: true, ChildMayDecide: true}, false, nil, DeciderParent, 3},
		{"row4 child", ConnectorOperation{Interactive: false, ChildMayDecide: true}, false, nil, DeciderChild, 4},
		{"row5 not allowed", ConnectorOperation{Interactive: false}, false, nil, DeciderParent, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, row := DecideDecider(c.op, c.cross, c.related)
			if got != c.want || row != c.row {
				t.Errorf("DecideDecider = (%s, %d), want (%s, %d)", got, row, c.want, c.row)
			}
		})
	}
}

func TestRunDelegation_RecordsDeciderAndRowOnTheRun(t *testing.T) {
	cases := []struct {
		name string
		mut  func(m map[string]any)
		want Decider
		row  int64
	}{
		{"human", func(m map[string]any) { m["operation"] = "define-human" }, DeciderHuman, 1},
		{"cross_repo", func(m map[string]any) { m["cross_repo"] = true }, DeciderParent, 2},
		{"related repos", func(m map[string]any) { m["related_repos"] = []string{"main-repo", "sibling"} }, DeciderParent, 2},
		{"parent counterpart", func(m map[string]any) { m["operation"] = "define-parent" }, DeciderParent, 3},
		{"counterpart omitted", func(m map[string]any) { m["operation"] = "define-default" }, DeciderParent, 3},
		{"child", nil, DeciderChild, 4},
		{"child_may_decide omitted", func(m map[string]any) { m["operation"] = "quiet" }, DeciderParent, 5},
		{"interactive omitted is interactive", func(m map[string]any) { m["operation"] = "omitted" }, DeciderParent, 3},
		{"human beats cross_repo", func(m map[string]any) { m["operation"] = "define-human"; m["cross_repo"] = true }, DeciderHuman, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDelegateFixture(t)
			f.newInProgress(t, "t", "P1", planSpec(c.mut))
			if _, err := f.run(t, nil); err != nil {
				t.Fatalf("RunDelegation: %v", err)
			}
			runs := f.delegateRuns(t)
			if len(runs) != 1 {
				t.Fatalf("delegate runs = %d, want 1", len(runs))
			}
			if runs[0].Decider != c.want || runs[0].DeciderRow == nil || *runs[0].DeciderRow != c.row {
				t.Errorf("run decider = (%q, %v), want (%q, %d)", runs[0].Decider, runs[0].DeciderRow, c.want, c.row)
			}
			in := f.deleg.launched()[0]
			if in.Decider != c.want || int64(in.DeciderRow) != c.row {
				t.Errorf("launch decider = (%s, %d), want (%s, %d)", in.Decider, in.DeciderRow, c.want, c.row)
			}
		})
	}
}

// --- 対象（AC-200〜202） ---

func TestRunDelegation_OmittedID_OnlyInProgressWithApprovedPlanAndNoActiveRun(t *testing.T) {
	f := newDelegateFixture(t)
	ctx := context.Background()
	target := f.newInProgress(t, "target", "P1", planSpec(nil))

	unclassified, _ := f.s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "unclassified"})
	_ = unclassified
	classified := f.newInProgress(t, "classified", "P1", planSpec(nil))
	awaitingApproval := f.newInProgress(t, "awaiting", "P1", planSpec(nil))
	verifying := f.newInProgress(t, "verifying", "P1", planSpec(nil))
	onHold := f.newInProgress(t, "hold", "P1", planSpec(nil))
	noSpec := f.newInProgress(t, "nospec", "P1", "")
	busy := f.newInProgress(t, "busy", "P1", planSpec(nil))
	for id, st := range map[string]string{classified: "classified", awaitingApproval: "awaiting_plan_approval", verifying: "verifying", onHold: "awaiting_human"} {
		n, _ := parseChallengeID(id)
		if err := f.s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, st, n)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 終了していない run を持つ着手中の課題。
	bn, _ := parseChallengeID(busy)
	now := time.Now()
	if err := f.s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := insertRun(ctx, tx, bn, insertRunInput{Kind: runKindJudgment, Judgment: JudgmentJ5, ChallengeVersion: 1, SessionID: "11111111-1111-4111-8111-111111111111",
			PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketJudgment})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = noSpec

	res, err := f.run(t, nil)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != target {
		t.Fatalf("items = %+v, want only %s", res.Items, target)
	}
	if got := len(f.j3Inputs); got != 1 {
		t.Errorf("J3 was started %d times, want 1 (non-target challenges must not start J3)", got)
	}
}

func TestRunDelegation_OrdersByPriorityThenID(t *testing.T) {
	f := newDelegateFixture(t)
	a := f.newInProgress(t, "a-p2", "P2", planSpec(func(m map[string]any) { m["repo"] = "direct-repo"; m["operation"] = "brief" }))
	b := f.newInProgress(t, "b-p0", "P0", planSpec(nil))
	c := f.newInProgress(t, "c-p0", "P0", planSpec(func(m map[string]any) { m["repo"] = "sibling" }))
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range res.Items {
		got = append(got, it.ChallengeID)
	}
	if want := []string{b, c, a}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestRunDelegation_ExplicitID_ErrorsForStatusAndActiveRun(t *testing.T) {
	f := newDelegateFixture(t)
	ctx := context.Background()
	ch, _ := f.s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "x"})
	if _, err := f.run(t, &ch.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("unclassified: err = %v, want ErrInvalidTransition", err)
	}
	missing := "C-999"
	if _, err := f.run(t, &missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: err = %v, want ErrNotFound", err)
	}

	id := f.newInProgress(t, "active", "P1", planSpec(nil))
	n, _ := parseChallengeID(id)
	now := time.Now()
	if err := f.s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := insertRun(ctx, tx, n, insertRunInput{Kind: runKindJudgment, Judgment: JudgmentJ5, ChallengeVersion: 1, SessionID: "11111111-1111-4111-8111-111111111111",
			PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketJudgment})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(t, &id); !errors.Is(err, ErrRunInProgress) {
		t.Errorf("active run: err = %v, want ErrRunInProgress", err)
	}
	if len(f.j3Inputs) != 0 || len(f.deleg.launched()) != 0 {
		t.Error("nothing may be started for a challenge with an unfinished run")
	}
}

func TestRunDelegation_ExplicitID_PlanWithoutSpecIsValidationError(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "manual", "P1", "")
	if _, err := f.run(t, &id); !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if len(f.j3Inputs) != 0 {
		t.Error("J3 must not start")
	}
}

// --- J3 の入力・出力（AC-203〜211） ---

func TestRunDelegation_J3InputHasPlanSpecHoldsOperationAndDecider(t *testing.T) {
	f := newDelegateFixture(t)
	spec := planSpec(nil)
	id := f.newInProgress(t, "t", "P1", spec)
	n, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by)
			VALUES (?, 'HOLD-Q', 'classified', '2026-09-25T00:00:00.000Z', 'HOLD-A', '2026-09-26T00:00:00.000Z', 'alice')`, n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	text := sectionsText(f.j3Inputs[0].Sections)
	for _, want := range []string{"PLAN-BODY-t", spec, "HOLD-Q", "HOLD-A", "impl", "意思決定者: child", "該当した行: 4"} {
		if !strings.Contains(text, want) {
			t.Errorf("J3 input does not contain %q:\n%s", want, text)
		}
	}
	if f.j3Inputs[0].Judgment != JudgmentJ3 {
		t.Errorf("judgment = %s, want J3", f.j3Inputs[0].Judgment)
	}
	if len(f.upstream.threadCalls) != 0 {
		t.Error("upstream must not be fetched for a challenge without a source binding")
	}
}

func TestRunDelegation_J3InputFetchesUpstream_AndLeavesReadValuesUnchanged(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "src", "P1", planSpec(func(m map[string]any) { m["operation"] = "impl-src" }))
	ch := &Challenge{ID: id}
	// f.bind 相当（j2Fixture と同じ形）。
	key := "o/r#7"
	in := validBindingInput(key)
	th := simpleThread("UP-BODY-MARKER", "UP-COMMENT-ONE", "UP-COMMENT-TWO")
	in.CommentsCount = th.Issue.Comments
	in.UpstreamUpdatedAt = th.Issue.UpdatedAt
	if _, err := createSourceBindingForTest(f.s, ch.ID, time.Now(), in); err != nil {
		t.Fatal(err)
	}
	th.Issue.ExternalKey, th.Issue.Repo, th.Issue.Number, th.Issue.State = key, "o/r", 7, "open"
	f.upstream.threads[key] = th

	before := f.detail(t, id).SourceBinding
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	text := sectionsText(f.j3Inputs[0].Sections)
	for _, want := range []string{"UP-BODY-MARKER", "UP-COMMENT-ONE", "UP-COMMENT-TWO"} {
		if !strings.Contains(text, want) {
			t.Errorf("J3 input does not contain %q", want)
		}
	}
	after := f.detail(t, id).SourceBinding
	if after.ReadCommentsCount != before.ReadCommentsCount || after.ReadUpstreamUpdatedAt != before.ReadUpstreamUpdatedAt {
		t.Errorf("read values changed: before=%+v after=%+v", before, after)
	}
	// plugin の invocation の差し込みは取り込み元の Issue 番号で埋まる。
	if got := f.deleg.launched()[0].Invocation; got != "/h:impl 7 "+id {
		t.Errorf("Invocation = %q, want %q", got, "/h:impl 7 "+id)
	}
}

func (f *delegateFixture) detail(t *testing.T, id string) *ChallengeDetail {
	t.Helper()
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	return d
}

func TestRunDelegation_UpstreamFetchFailure_StartsNothing(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "src", "P1", planSpec(func(m map[string]any) { m["operation"] = "impl-src" }))
	in := validBindingInput("o/r#7")
	if _, err := createSourceBindingForTest(f.s, id, time.Now(), in); err != nil {
		t.Fatal(err)
	}
	f.upstream.threadErr = errors.New("gh is down")
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamFetchFailed {
		t.Fatalf("result = %+v, want one not-started upstream_fetch_failed", res)
	}
	if len(f.j3Inputs) != 0 || len(f.delegateRuns(t)) != 0 {
		t.Error("no run may be created")
	}
}

func TestRunDelegation_J3OutputSchemaHasOnlyBrief(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(j3OutputSchema(), &m); err != nil {
		t.Fatal(err)
	}
	props := m["properties"].(map[string]any)
	if len(props) != 1 || props["brief"] == nil || m["additionalProperties"] != false {
		t.Errorf("schema = %v, want exactly one property `brief` and additionalProperties=false", m)
	}
}

func TestRunDelegation_J3InvalidOutputOrNotSucceeded_NoDelegationAndStateUnchanged(t *testing.T) {
	cases := []struct {
		name string
		out  JudgmentLaunchOutput
		want RunResult
	}{
		{"extra key", JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{"brief":"b","extra":1}`)}, RunResultInvalidOutput},
		{"empty brief", JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{"brief":"  "}`)}, RunResultInvalidOutput},
		{"missing brief", JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)}, RunResultInvalidOutput},
		{"errored", JudgmentLaunchOutput{Result: RunResultErrored}, RunResultErrored},
		{"timed out", JudgmentLaunchOutput{Result: RunResultTimedOut}, RunResultTimedOut},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDelegateFixture(t)
			id := f.newInProgress(t, "t", "P1", planSpec(nil))
			before := f.detail(t, id)
			f.j3.result = c.out
			res, err := f.run(t, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Items) != 1 || res.Items[0].Result != c.want {
				t.Fatalf("items = %+v, want result %s", res.Items, c.want)
			}
			if len(f.deleg.launched()) != 0 || len(f.delegateRuns(t)) != 0 {
				t.Error("the delegation must not start")
			}
			after := f.detail(t, id)
			if after.Status != before.Status || after.Version != before.Version {
				t.Errorf("challenge changed: %s v%d -> %s v%d", before.Status, before.Version, after.Status, after.Version)
			}
			if st := f.slotStates(t); st["slot1"] == "busy" {
				t.Error("slot must not be assigned")
			}
		})
	}
}

func TestRunDelegation_J3BudgetAndTimeoutComeFromDeclaration(t *testing.T) {
	f := newDelegateFixture(t)
	f.agent.JudgmentBudgetUSD.J3 = 1.25
	f.newInProgress(t, "t", "P1", planSpec(nil))
	var got JudgmentLaunchInput
	f.j3.onInvoke = func(in JudgmentLaunchInput) { got = in }
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got.MaxBudgetUSD != 1.25 {
		t.Errorf("J3 MaxBudgetUSD = %v, want 1.25", got.MaxBudgetUSD)
	}
}

// --- 委譲の起動（AC-209〜215） ---

func TestRunDelegation_LaunchInput_WorkDirPermissionModeBriefInvocationBudget(t *testing.T) {
	modes := map[string]PermissionMode{"harness": PermissionModeAcceptEdits, "direct": PermissionModeDefault}
	f := newDelegateFixture(t)
	f.newInProgress(t, "plugin", "P0", planSpec(nil))
	f.newInProgress(t, "brief", "P1", planSpec(func(m map[string]any) { m["repo"] = "direct-repo"; m["operation"] = "brief" }))
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	all := f.deleg.launched()
	if len(all) != 2 {
		t.Fatalf("launches = %d, want 2", len(all))
	}
	// 委譲は並行に起動するので、記録の順序に依存せず作業ディレクトリで引き当てる。
	byDir := map[string]DelegateLaunchInput{}
	for _, in := range all {
		byDir[in.WorkDir] = in
	}
	pluginIn, ok1 := byDir[filepath.Join(f.s.Workspace(), "slot1")]
	briefIn, ok2 := byDir[filepath.Join(f.s.Workspace(), "slot-d")]
	if !ok1 || !ok2 {
		t.Fatalf("launch work dirs = %+v, want slot1 and slot-d", all)
	}
	ins := []DelegateLaunchInput{pluginIn, briefIn}
	if ins[0].WorkDir != filepath.Join(f.s.Workspace(), "slot1") || ins[0].PermissionMode != modes["harness"] {
		t.Errorf("plugin launch = %+v", ins[0])
	}
	if ins[0].Invocation == "" || ins[0].Brief != "BRIEF-MARKER" {
		t.Errorf("plugin: Invocation=%q Brief=%q", ins[0].Invocation, ins[0].Brief)
	}
	if ins[1].WorkDir != filepath.Join(f.s.Workspace(), "slot-d") || ins[1].PermissionMode != modes["direct"] {
		t.Errorf("brief launch = %+v", ins[1])
	}
	if ins[1].Invocation != "" {
		t.Errorf("a brief-form operation must not carry an invocation, got %q", ins[1].Invocation)
	}
	// 目印（FLYWHEEL_DELEGATED_RUN）の値になる run の ID が、記録した run の ID と一致する。
	runIDs := map[string]bool{}
	for _, r := range f.delegateRuns(t) {
		runIDs[r.ID] = true
	}
	for i, in := range ins {
		if !runIDs[in.RunID] {
			t.Errorf("launch %d RunID = %q, want one of the recorded run IDs %v", i, in.RunID, runIDs)
		}
	}
	if ins[0].RunID == ins[1].RunID {
		t.Errorf("launches share RunID %q", ins[0].RunID)
	}
	for i, in := range ins {
		if in.MaxBudgetUSD != 50 { // サイズ M の実装枠の既定
			t.Errorf("launch %d MaxBudgetUSD = %v, want 50", i, in.MaxBudgetUSD)
		}
		if !strings.Contains(string(in.OutputSchema), `"release_timing"`) {
			t.Errorf("launch %d schema lacks the declared question kind", i)
		}
		if in.SessionID == "" || strings.ToLower(in.SessionID) != in.SessionID {
			t.Errorf("launch %d SessionID = %q, want lowercase uuid", i, in.SessionID)
		}
	}
	for _, r := range f.delegateRuns(t) {
		if r.BudgetBucket != budgetBucketImpl || r.MaxBudgetUSD != 50_000_000 || r.PlanVersion == nil || *r.PlanVersion != 1 || r.SlotID == nil {
			t.Errorf("delegate run = %+v", r)
		}
	}
}

func TestRunDelegation_MaxBudgetUsesPlanImplBudgetAndItsOverride(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(func(m map[string]any) { m["budget_impl_usd"] = 41 }))
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.deleg.launched()[0].MaxBudgetUSD; got != 41 {
		t.Errorf("MaxBudgetUSD = %v, want 41", got)
	}

	n, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = 'in_progress' WHERE id = ?`, n)
		if err != nil {
			return err
		}
		ok, err := setPlanBudgetOverride(context.Background(), tx, n, 1, planBudgetOverride{ImplBudgetUSD: ptrInt64(70_000_000)})
		if !ok {
			return errors.New("no plan")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(t, &id); err != nil {
		t.Fatal(err)
	}
	// 実装枠の残り ＝ 上書きした額 − その版の実装枠の run の費用（最初の run は 2.5 USD）。
	if got := f.deleg.launched()[1].MaxBudgetUSD; got != 67.5 {
		t.Errorf("MaxBudgetUSD after override = %v, want 67.5", got)
	}
}

func ptrInt64(v int64) *int64 { return &v }

func TestRunDelegation_RecordsResultCostAndReleasesSlot(t *testing.T) {
	f := newDelegateFixture(t)
	f.newInProgress(t, "t", "P1", planSpec(nil))
	// 結末 blocked は保留（人間対応待ち）になる。
	f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) { m["outcome"] = "blocked" })
	var busyDuring string
	f.deleg.onInvoke = func(DelegateLaunchInput) { busyDuring = f.slotStates(t)["slot1"] }
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if busyDuring != "busy" {
		t.Errorf("slot during the delegation = %q, want busy", busyDuring)
	}
	if st := f.slotStates(t)["slot1"]; st != "idle" {
		t.Errorf("slot after the run = %q, want idle", st)
	}
	runs := f.delegateRuns(t)
	if len(runs) != 1 || runs[0].Result != RunResultSucceeded || runs[0].CostUSD == nil || *runs[0].CostUSD != 2_500_000 {
		t.Fatalf("run = %+v", runs)
	}
	if res.Items[0].Outcome != "blocked" || res.Items[0].Status == nil || *res.Items[0].Status != string(StatusAwaitingHuman) {
		t.Errorf("item = %+v, want outcome blocked and awaiting_human", res.Items[0])
	}
	if d := f.detail(t, "C-1"); d.Status != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human", d.Status)
	}
}

func TestRunDelegation_NonSucceededResultsStillReleaseSlot(t *testing.T) {
	for _, r := range []RunResult{RunResultErrored, RunResultTimedOut, RunResultMalformed, RunResultBudgetExhausted, RunResultInvalidOutput, RunResultLaunchFailed} {
		t.Run(string(r), func(t *testing.T) {
			f := newDelegateFixture(t)
			f.newInProgress(t, "t", "P1", planSpec(nil))
			f.deleg.result = JudgmentLaunchOutput{Result: r}
			if _, err := f.run(t, nil); err != nil {
				t.Fatal(err)
			}
			if st := f.slotStates(t)["slot1"]; st != "idle" {
				t.Errorf("slot = %q, want idle", st)
			}
			if runs := f.delegateRuns(t); len(runs) != 1 || runs[0].Result != r || runs[0].EndedAt == nil {
				t.Errorf("run = %+v, want result %s ended", runs, r)
			}
		})
	}
}

func TestRunDelegation_InvalidReportBecomesInvalidOutput(t *testing.T) {
	bad := map[string][]byte{
		"unknown outcome": reportJSON(func(m map[string]any) { m["outcome"] = "done" }),
		"missing key":     reportJSON(func(m map[string]any) { delete(m, "unverified") }),
		"extra key":       reportJSON(func(m map[string]any) { m["extra"] = 1 }),
		"unknown kind": reportJSON(func(m map[string]any) {
			m["outcome"] = "questions"
			m["questions"] = []any{map[string]any{"kind": "whatever", "text": "q", "options": []string{}, "recommendation": nil}}
		}),
		"question without a key": reportJSON(func(m map[string]any) {
			m["questions"] = []any{map[string]any{"kind": "minor", "text": "q", "options": []string{}}}
		}),
		"null pr_urls": reportJSON(func(m map[string]any) { m["pr_urls"] = nil }),
	}
	for name, out := range bad {
		t.Run(name, func(t *testing.T) {
			f := newDelegateFixture(t)
			f.newInProgress(t, "t", "P1", planSpec(nil))
			f.deleg.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: out}
			res, err := f.run(t, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.Items[0].Result != RunResultInvalidOutput || res.Items[0].Outcome != "" {
				t.Errorf("item = %+v, want invalid_output", res.Items[0])
			}
			if runs := f.delegateRuns(t); runs[0].Result != RunResultInvalidOutput {
				t.Errorf("run result = %s", runs[0].Result)
			}
		})
	}
}

func TestDelegationReport_QuestionKindsAreBaseEightPlusDeclared(t *testing.T) {
	declared := []HumanQuestionKind{{ID: "release_timing", Label: "x"}}
	for _, k := range append(QuestionKindValues, QuestionKind("release_timing")) {
		out := reportJSON(func(m map[string]any) {
			m["outcome"] = "questions"
			m["questions"] = []any{map[string]any{"kind": string(k), "text": "q", "options": []string{"a"}, "recommendation": "a"}}
		})
		r, ok := validateDelegationReport(out, declared)
		if !ok || len(r.Questions) != 1 || r.Questions[0].Kind != string(k) {
			t.Errorf("kind %q must be accepted: ok=%v", k, ok)
		}
	}
	if _, ok := validateDelegationReport(reportJSON(func(m map[string]any) {
		m["questions"] = []any{map[string]any{"kind": "release_timing", "text": "q", "options": []string{}, "recommendation": nil}}
	}), nil); ok {
		t.Error("a declared kind must not be accepted when it is not declared")
	}
	var schema map[string]any
	if err := json.Unmarshal(delegationReportSchema(declared), &schema); err != nil {
		t.Fatal(err)
	}
	outcomes := schema["properties"].(map[string]any)["outcome"].(map[string]any)["enum"].([]any)
	if len(outcomes) != 3 {
		t.Errorf("outcome enum = %v, want the three closed values", outcomes)
	}
	if req := schema["required"].([]any); len(req) != 9 {
		t.Errorf("required = %v, want the nine keys", req)
	}
}

// --- スロット（AC-281・365） ---

func TestRunDelegation_ExplicitID_NoUsableSlot_SlotUnavailableAndNoRun(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	if err := f.s.EnsureSlots(context.Background(), f.conn.Repos[0]); err != nil {
		t.Fatal(err)
	}
	slots, _ := f.s.ListSlots(context.Background())
	sid, _ := parseSlotID(slots[0].ID)
	if err := f.s.mutateSlots(context.Background(), func(tx *sql.Tx) error {
		_, err := updateSlotState(context.Background(), tx, sid, slotStateNeedsAttention, nil, "x")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(t, &id); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("err = %v, want ErrSlotUnavailable", err)
	}
	if len(f.deleg.launched()) != 0 || len(f.delegateRuns(t)) != 0 {
		t.Error("no delegation may start")
	}

	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedSlotUnavailable {
		t.Errorf("not started = %+v, want slot_unavailable", res.NotStarted)
	}
}

func TestRunDelegation_OneSlotTwoChallenges_OnlyOneLaunches(t *testing.T) {
	f := newDelegateFixture(t)
	a := f.newInProgress(t, "a", "P1", planSpec(nil))
	b := f.newInProgress(t, "b", "P1", planSpec(nil))
	f.deleg.block = make(chan struct{})
	f.deleg.started = make(chan struct{}, 2)

	errs := make(chan error, 2)
	for _, id := range []string{a, b} {
		id := id
		go func() {
			_, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), &id))
			errs <- err
		}()
	}
	<-f.deleg.started // 一方が起動した
	var first error
	select {
	case first = <-errs: // もう一方は待たずに slot_unavailable で終わる
	case <-time.After(10 * time.Second):
		t.Fatal("the second call did not finish while the first holds the slot")
	}
	close(f.deleg.block)
	second := <-errs
	// 先に起動した課題は取り込み元の対応が無く Issue 番号を渡せないため、後の呼び出しの計画の時点で
	// 起動が見えていれば serialized、見えていなければ割り当ての時点で slot_unavailable になる。
	if (!errors.Is(first, ErrSlotUnavailable) && !errors.Is(first, ErrSerialized)) || second != nil {
		t.Fatalf("results = (%v, %v), want one ErrSlotUnavailable/ErrSerialized and one success", first, second)
	}
	if n := len(f.deleg.launched()); n != 1 {
		t.Errorf("launches = %d, want 1", n)
	}
}

func TestRunDelegation_DirtySlotIsNotAssigned(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	p := filepath.Join(f.s.Workspace(), "slot1")
	st := f.git.states[p]
	st.Dirty = true
	f.git.states[p] = st
	if _, err := f.run(t, &id); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("err = %v, want ErrSlotUnavailable", err)
	}
	if got := f.slotStates(t)["slot1"]; got != "needs_attention" {
		t.Errorf("slot = %q, want needs_attention", got)
	}
}

// --- 周の上限・枠超過 ---

func TestRunDelegation_CycleBudgetTooSmallForImplSlot(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	cycleID := f.cycle(t, 10) // 実装枠 50 は入らない（J3 の 3 は入る）
	if _, err := f.s.RunDelegation(context.Background(), f.input(cycleID, &id)); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if len(f.deleg.launched()) != 0 {
		t.Error("the delegation must not start")
	}
	if st := f.slotStates(t)["slot1"]; st == "busy" {
		t.Error("slot must not stay assigned")
	}

	res, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 10), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedCycleBudget {
		t.Errorf("not started = %+v, want cycle_budget", res.NotStarted)
	}
}

func TestRunDelegation_RateLimitedStopsLaterCandidates(t *testing.T) {
	f := newDelegateFixture(t)
	// 別のリポジトリの候補は別のグループで並行に起動されうるので、同時の起動を 1 つにして順に処理する。
	f.agent.MaxParallelRuns = 1
	a := f.newInProgress(t, "a", "P0", planSpec(nil))
	b := f.newInProgress(t, "b", "P1", planSpec(func(m map[string]any) { m["repo"] = "sibling" }))
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != a || len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != b || res.NotStarted[0].Reason != NotStartedRateLimited {
		t.Errorf("result = %+v", res)
	}
}

// 中断した委譲の run のスロットは、子がまだ動いているかもしれないので needs_attention にする。
// 回収の後に子が戻っても、回収済みの run は上書きされない。
func TestReapInterruptedRuns_MarksTheDelegationSlotNeedsAttention(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.s.staleAfter = time.Nanosecond
	f.deleg.block = make(chan struct{})
	f.deleg.started = make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), &id))
	}()
	<-f.deleg.started
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE run SET pid = 0, heartbeat_at = '2020-01-01T00:00:00.000Z' WHERE kind = 'delegate'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReapInterruptedRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := f.slotStates(t)["slot1"]; st != "needs_attention" {
		t.Errorf("slot = %q, want needs_attention after the run was reaped", st)
	}
	close(f.deleg.block)
	<-done
	if runs := f.delegateRuns(t); runs[0].Result != RunResultInterrupted {
		t.Errorf("run result = %s, the reaped run must not be overwritten", runs[0].Result)
	}
	if st := f.slotStates(t)["slot1"]; st != "needs_attention" {
		t.Errorf("slot = %q, must stay needs_attention", st)
	}
}

func TestDelegationReport_QuestionsOutcomeNeedsAQuestion(t *testing.T) {
	if _, ok := validateDelegationReport(reportJSON(func(m map[string]any) { m["outcome"] = "questions" }), nil); ok {
		t.Error("outcome questions without any question must be invalid")
	}
}

func TestRunDelegation_StatusChangedAfterJ3_ExplicitIDIsInvalidTransition(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	n, _ := parseChallengeID(id)
	f.j3.onInvoke = func(JudgmentLaunchInput) {
		_ = f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, n)
			return err
		})
	}
	if _, err := f.run(t, &id); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
	if st := f.slotStates(t)["slot1"]; st == "busy" {
		t.Error("slot must not stay assigned")
	}
}

func TestRunDelegation_OmittedID_OneBadPlanDoesNotStopTheOthers(t *testing.T) {
	f := newDelegateFixture(t)
	f.newInProgress(t, "bad", "P0", planSpec(func(m map[string]any) { m["repo"] = "gone-repo" }))
	good := f.newInProgress(t, "good", "P1", planSpec(nil))
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != good {
		t.Errorf("items = %+v, want only %s", res.Items, good)
	}
}

func TestRunDelegation_SourceIssuePassedToTheLaunch(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "src", "P1", planSpec(func(m map[string]any) { m["operation"] = "impl-src" }))
	in := validBindingInput("o/r#7")
	th := simpleThread("b")
	in.UpstreamUpdatedAt = th.Issue.UpdatedAt
	if _, err := createSourceBindingForTest(f.s, id, time.Now(), in); err != nil {
		t.Fatal(err)
	}
	th.Issue.ExternalKey, th.Issue.Repo, th.Issue.Number, th.Issue.State = "o/r#7", "o/r", 7, "open"
	f.upstream.threads["o/r#7"] = th
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.deleg.launched()[0]; got.SourceIssueNumber != 7 || got.SourceIssueURL == "" {
		t.Errorf("launch = %+v", got)
	}
}

func TestRunDelegation_RejectsIncompleteInput(t *testing.T) {
	f := newDelegateFixture(t)
	in := f.input(f.cycle(t, 300), nil)
	in.Delegate = nil
	if _, err := f.s.RunDelegation(context.Background(), in); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
	_ = fmt.Sprint
}
