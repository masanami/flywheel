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
