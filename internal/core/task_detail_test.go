package core

import (
	"context"
	"database/sql"
	"testing"
)

// タスクの詳細（S3d AC-35〜AC-59）を、実ストアへ契約の実行・成果物・作業ログを書いて、課題の詳細から読む。

type tdCR struct {
	state, summary, reqKind, facts, landRef, verifiedRef string
	ended                                                bool
	landDelivered                                        int
}

func (f *contractFixture) tdRun(t *testing.T, cid, task int64) int64 {
	t.Helper()
	costRun(t, f.s, cid, task, "delegate", "", "impl", 0, 0)
	var id int64
	writeForTest(t, f.s, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT MAX(id) FROM run`).Scan(&id) })
	return id
}

func (f *contractFixture) tdContractRun(t *testing.T, cid, task int64, ext string, c tdCR) {
	t.Helper()
	start := f.tdRun(t, cid, task)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		var ended any
		if c.ended {
			ended = "2026-11-01T00:00:09.000Z"
		}
		_, err := tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, requested_kind, facts,
			land_ref, land_delivered, verified_ref, max_budget_usd, started_at, observed_at, ended_at, task_id, kind)
			VALUES (?, 'tool', ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), 1, '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', ?, ?, 'task')`,
			cid, ext, start, c.state, c.summary, c.reqKind, c.facts, c.landRef, c.landDelivered, c.verifiedRef, ended, task)
		return err
	})
}

func (f *contractFixture) tdTask(t *testing.T, id, key string) (Task, int64) {
	t.Helper()
	for _, tk := range f.detailOf(t, id).Tasks {
		if tk.Key == key {
			return tk, taskIntID(&tk)
		}
	}
	t.Fatalf("task %s missing", key)
	return Task{}, 0
}

func (f *contractFixture) detailOf(t *testing.T, id string) *ChallengeDetail {
	t.Helper()
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	return d
}

func TestTaskDetail_DelegateIsTheLastContractRun_AC35_AC42(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", false, multiTask{key: "a"})
	cid, _ := parseChallengeID(id)
	tk, tid := f.tdTask(t, id, "a")
	if tk.Detail.Delegate != nil || tk.Detail.Landing != nil || tk.Detail.Verification != nil || len(tk.Detail.Artifacts) != 0 || len(tk.Detail.Facts) != 0 {
		t.Fatalf("AC-42/47/49/53: empty detail = %+v", tk.Detail)
	}
	f.tdContractRun(t, cid, tid, "e1", tdCR{state: "failed", summary: "old", ended: true})
	f.tdContractRun(t, cid, tid, "e2", tdCR{state: "waiting", summary: "new", reqKind: "review", facts: `{"self_review":"complete"}`})
	tk, _ = f.tdTask(t, id, "a")
	d := tk.Detail.Delegate
	if d == nil || d.State != ContractStateWaiting || d.Summary != "new" {
		t.Fatalf("AC-35/36/38: delegate = %+v, want the larger id's (waiting/new)", d)
	}
	if d.RequestedKind == nil || *d.RequestedKind != "review" {
		t.Errorf("AC-40: requested_kind = %v, want review", d.RequestedKind)
	}
	var start int64
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT start_run_id FROM contract_run WHERE external_run_id = 'e2'`).Scan(&start)
	})
	if d.RunID != formatRunID(start) {
		t.Errorf("AC-37: run_id = %s, want %s", d.RunID, formatRunID(start))
	}
	if tk.Detail.Facts["self_review"] != "complete" {
		t.Errorf("AC-48: facts = %v", tk.Detail.Facts)
	}
}

func TestTaskDetail_DoneTaskKeepsTheEndedRunAndNoRequestedKind_AC39_AC41(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", false, multiTask{key: "a"})
	cid, _ := parseChallengeID(id)
	_, tid := f.tdTask(t, id, "a")
	f.tdContractRun(t, cid, tid, "e1", tdCR{state: "succeeded", summary: "done", ended: true})
	setBudgetTaskStatus(t, f.s, tid, "done")
	tk, _ := f.tdTask(t, id, "a")
	d := tk.Detail.Delegate
	if d == nil || d.State != ContractStateSucceeded || d.RequestedKind != nil {
		t.Fatalf("AC-39/41: delegate = %+v", d)
	}
	if tk.Detail.Landing != nil {
		t.Errorf("AC-59: landing = %v, want nil for a challenge without integration", *tk.Detail.Landing)
	}
}

func TestTaskDetail_ArtifactsOfTheLargestRunWithArtifacts_AC43_AC47(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", false, multiTask{key: "a"})
	cid, _ := parseChallengeID(id)
	_, tid := f.tdTask(t, id, "a")
	r1, r2, r3 := f.tdRun(t, cid, tid), f.tdRun(t, cid, tid), f.tdRun(t, cid, tid) // r3 は成果物を持たない
	_ = r3
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		for _, q := range [][]any{
			{r1, "pr", "https://example.test/old", "open", nil},
			{r2, "pr", "https://example.test/pr/1", "open", nil},
			{r2, "branch", "feat/x", nil, "integration"},
		} {
			if _, err := tx.Exec(`INSERT INTO run_artifact (run_id, kind, ref, state, role) VALUES (?, ?, ?, ?, ?)`, q...); err != nil {
				return err
			}
		}
		return nil
	})
	tk, _ := f.tdTask(t, id, "a")
	got := tk.Detail.Artifacts
	if len(got) != 2 || got[0].Ref != "https://example.test/pr/1" || got[0].Kind != "pr" || got[0].State != "open" || got[0].Role != nil {
		t.Fatalf("AC-43/44/46: artifacts = %+v", got)
	}
	if got[1].Role == nil || *got[1].Role != "integration" {
		t.Errorf("AC-45: role = %v, want integration", got[1].Role)
	}
}

func TestTaskDetail_VerificationIsTheLargestTaskVerifyRecord_AC50_AC53(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", false, multiTask{key: "a"}, multiTask{key: "b"})
	_, tid := f.tdTask(t, id, "a")
	_, bid := f.tdTask(t, id, "b")
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		for _, r := range [][]any{
			{"2026-11-01T00:00:01.000Z", tid, `{"verdict":"not_met","reason":"first"}`},
			{"2026-11-01T00:00:02.000Z", tid, `{"verdict":"met","reason":"second","content_ref":"abc"}`},
			{"2026-11-01T00:00:03.000Z", bid, `{"verdict":"uncertain","reason":"other task"}`},
		} {
			if _, err := tx.Exec(`INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, after) VALUES (?, 'x', 'invoker', 'none', 'task', ?, 'task_verify', ?)`, r...); err != nil {
				return err
			}
		}
		return nil
	})
	tk, _ := f.tdTask(t, id, "a")
	v := tk.Detail.Verification
	if v == nil || v.Verdict != "met" || v.Reason != "second" || v.At.UTC().Format("2006-01-02T15:04:05.000Z") != "2026-11-01T00:00:02.000Z" {
		t.Fatalf("AC-50/51/52: verification = %+v", v)
	}
}

func TestTaskDetail_Landing_AC54_AC59(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", true, multiTask{key: "a"}, multiTask{key: "b"}, multiTask{key: "c"}, multiTask{key: "d"})
	cid, _ := parseChallengeID(id)
	_, a := f.tdTask(t, id, "a")
	_, b := f.tdTask(t, id, "b")
	_, c := f.tdTask(t, id, "c")
	_, d := f.tdTask(t, id, "d")
	f.tdContractRun(t, cid, a, "ea", tdCR{state: "waiting", summary: "s", verifiedRef: "v1"})
	f.tdContractRun(t, cid, b, "eb", tdCR{state: "waiting", summary: "s", verifiedRef: "v1", landRef: "v1", landDelivered: 0})
	f.tdContractRun(t, cid, c, "ec", tdCR{state: "succeeded", summary: "s", verifiedRef: "v1", landRef: "v1", ended: true})
	f.tdContractRun(t, cid, d, "ed", tdCR{state: "running", summary: "s"})
	setBudgetTaskStatus(t, f.s, c, "done")
	want := map[string]*TaskLanding{"a": ptrLanding(TaskLandingVerified), "b": ptrLanding(TaskLandingSent), "c": ptrLanding(TaskLandingLanded), "d": nil}
	for key, w := range want {
		tk, _ := f.tdTask(t, id, key)
		got := tk.Detail.Landing
		if (got == nil) != (w == nil) || (got != nil && *got != *w) {
			t.Errorf("task %s: landing = %v, want %v", key, got, w)
		}
	}
}

// 終わった実行の verified_ref・land_ref は、済みでないタスクでも landing にならない。統合先を使わない課題も、実行が開いていて印があっても null。
func TestTaskDetail_LandingIsNilForEndedRunOrNonIntegration_AC58_AC59(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", true, multiTask{key: "a"})
	cid, _ := parseChallengeID(id)
	_, a := f.tdTask(t, id, "a")
	f.tdContractRun(t, cid, a, "ea", tdCR{state: "failed", summary: "s", verifiedRef: "v1", landRef: "v1", ended: true})
	if tk, _ := f.tdTask(t, id, "a"); tk.Detail.Landing != nil {
		t.Errorf("ended run: landing = %v, want nil", *tk.Detail.Landing)
	}
	g := newContractFixture(t)
	id2 := g.newMulti(t, "d", "P2", false, multiTask{key: "a"})
	cid2, _ := parseChallengeID(id2)
	_, a2 := g.tdTask(t, id2, "a")
	g.tdContractRun(t, cid2, a2, "ea", tdCR{state: "waiting", summary: "s", verifiedRef: "v1", landRef: "v1"})
	if tk, _ := g.tdTask(t, id2, "a"); tk.Detail.Landing != nil {
		t.Errorf("non-integration: landing = %v, want nil", *tk.Detail.Landing)
	}
}

func ptrLanding(l TaskLanding) *TaskLanding { return &l }

// AC-146〜AC-150（#1229）: 委譲の実行の flow と、ステップの行（seq の昇順・全種類・費用は USD に直す・無ければ空）。
func TestTaskDetail_DelegateFlowAndSteps_AC146_AC150(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", false, multiTask{key: "a"})
	cid, _ := parseChallengeID(id)
	_, tid := f.tdTask(t, id, "a")

	f.tdContractRun(t, cid, tid, "e1", tdCR{state: "running", summary: "s"})
	tk, _ := f.tdTask(t, id, "a")
	if d := tk.Detail.Delegate; d.Flow != nil || d.Steps == nil || len(d.Steps) != 0 {
		t.Fatalf("AC-148/149: flow=%v steps=%#v, want nil and an empty non-nil slice", d.Flow, d.Steps)
	}

	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE contract_run SET flow = 'ticket' WHERE external_run_id = 'e1'`)
		return err
	})
	tk, _ = f.tdTask(t, id, "a")
	if d := tk.Detail.Delegate; d.Flow == nil || *d.Flow != "ticket" || len(d.Steps) != 0 {
		t.Fatalf("AC-146: flow=%v steps=%v, want ticket with no step rows", d.Flow, d.Steps)
	}

	writeForTest(t, f.s, func(tx *sql.Tx) error {
		ins := `INSERT INTO contract_run_step (contract_run_id, seq, step, kind, attempt, state, outcome, started_at, ended_at, cost_usd, cost_unknown, model, models_used)
			VALUES ((SELECT id FROM contract_run WHERE external_run_id = 'e1'), ?, ?, ?, ?, ?, ?, '2026-11-01T00:00:00Z', ?, ?, ?, ?, ?)`
		// seq 2 を先に入れる（並びが挿入順でないこと。時刻は小数の桁数が違う RFC 3339 でも読めること）。
		if _, err := tx.Exec(ins, 2, "implement", "llm", 2, "running", nil, nil, 1942900, 1, "opus", `["opus","sonnet"]`); err != nil {
			return err
		}
		_, err := tx.Exec(ins, 1, "resolve", "select", 1, "finished", "pass", "2026-11-01T00:00:05.123456789Z", 0, 0, nil, `[]`)
		return err
	})
	tk, _ = f.tdTask(t, id, "a")
	d := tk.Detail.Delegate
	if d.Flow == nil || *d.Flow != "ticket" {
		t.Errorf("AC-146: flow = %v, want ticket", d.Flow)
	}
	if len(d.Steps) != 2 || d.Steps[0].Step != "resolve" || d.Steps[1].Step != "implement" || d.Steps[0].Kind != "select" {
		t.Fatalf("AC-147: steps = %+v, want seq order [resolve(select), implement]", d.Steps)
	}
	if c := d.Steps[1].CostUSD; c == nil || *c != 1.9429 || !d.Steps[1].CostUnknown {
		t.Errorf("AC-150: cost = %v unknown=%v, want 1.9429 true", c, d.Steps[1].CostUnknown)
	}
	if d.Steps[0].CostUSD == nil || *d.Steps[0].CostUSD != 0 {
		t.Errorf("AC-150: zero cost must stay a number, got %v", d.Steps[0].CostUSD)
	}
	if got := d.Steps[1].ModelsUsed; len(got) != 2 || got[0] != "opus" || got[1] != "sonnet" || d.Steps[1].Model == nil || *d.Steps[1].Model != "opus" {
		t.Errorf("models = %v model=%v", got, d.Steps[1].Model)
	}
	if d.Steps[1].EndedAt != nil || d.Steps[1].Outcome != nil {
		t.Errorf("running step must have nil ended_at/outcome: %+v", d.Steps[1])
	}
}

// AC-135〜AC-138: 課題の詳細のタスクは、タスクの記録の planned_flow・flow_reason を写す（NULL は空）。
func TestTaskDetail_PlannedFlowAndFlowReasonFromTaskRecord_AC135_AC138(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", false, multiTask{key: "a"}, multiTask{key: "b"})
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET planned_flow = 'ticket-light', flow_reason = '文書だけの変更' WHERE key = 'a'`)
		return err
	})
	a, _ := f.tdTask(t, id, "a")
	if a.PlannedFlow != "ticket-light" || a.FlowReason != "文書だけの変更" {
		t.Errorf("AC-135/137: planned_flow=%q flow_reason=%q", a.PlannedFlow, a.FlowReason)
	}
	b, _ := f.tdTask(t, id, "b")
	if b.PlannedFlow != "" || b.FlowReason != "" {
		t.Errorf("AC-136/138: NULL must read as empty, got %q %q", b.PlannedFlow, b.FlowReason)
	}
}

// AC-141: ステップの行の effort が、課題の詳細の delegate.steps の要素の Effort になる（NULL は nil）。
func TestTaskDetail_StepEffort_AC141(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", false, multiTask{key: "a"})
	cid, _ := parseChallengeID(id)
	_, tid := f.tdTask(t, id, "a")
	f.tdContractRun(t, cid, tid, "e1", tdCR{state: "running", summary: "s"})
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		ins := `INSERT INTO contract_run_step (contract_run_id, seq, step, kind, attempt, state, outcome, started_at, ended_at, cost_usd, cost_unknown, model, models_used, effort)
			VALUES ((SELECT id FROM contract_run WHERE external_run_id = 'e1'), ?, ?, 'llm', 1, 'finished', 'pass', '2026-11-01T00:00:00Z', '2026-11-01T00:00:05Z', 0, 0, NULL, '[]', ?)`
		if _, err := tx.Exec(ins, 1, "implement", "medium"); err != nil {
			return err
		}
		_, err := tx.Exec(ins, 2, "fix", nil)
		return err
	})
	tk, _ := f.tdTask(t, id, "a")
	steps := tk.Detail.Delegate.Steps
	if len(steps) != 2 || steps[0].Effort == nil || *steps[0].Effort != "medium" || steps[1].Effort != nil {
		t.Fatalf("steps = %+v, want effort medium then nil", steps)
	}
}

// AC-139/140（承認の後の分）: 計画の読み取りは、タスクの行があればタスクの記録の planned_flow・flow_reason で出す。
func TestPlanView_ApprovedTaskUsesTaskRecordFlow_AC139_AC140(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", false, multiTask{key: "a"})
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET planned_flow = 'ticket', flow_reason = '標準でよい' WHERE key = 'a'`)
		return err
	})
	pv, err := f.s.GetPlanView(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Tasks) != 1 || pv.Tasks[0].PlannedFlow != "ticket" || pv.Tasks[0].FlowReason != "標準でよい" {
		t.Errorf("tasks = %+v, want the task record's flow and reason", pv.Tasks)
	}
}
