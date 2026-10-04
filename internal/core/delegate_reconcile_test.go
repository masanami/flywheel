package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeBranchSource は UpstreamBranchSource の偽の実装。ブランチ名ごとの結果を持ち、
// 呼び出しの引数（repo・branch）を記録する。
type fakeBranchSource struct {
	mu       sync.Mutex
	branches map[string]bool
	prs      map[string][]UpstreamPullRequest
	err      error
	calls    []string // "exists repo branch" | "prs repo branch"
}

func newFakeBranchSource() *fakeBranchSource {
	return &fakeBranchSource{branches: map[string]bool{}, prs: map[string][]UpstreamPullRequest{}}
}

func (f *fakeBranchSource) BranchExists(_ context.Context, repo, branch string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "exists "+repo+" "+branch)
	if f.err != nil {
		return false, f.err
	}
	return f.branches[branch], nil
}

func (f *fakeBranchSource) ListPullRequestsByHead(_ context.Context, repo, branch string) ([]UpstreamPullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "prs "+repo+" "+branch)
	if f.err != nil {
		return nil, f.err
	}
	return append([]UpstreamPullRequest(nil), f.prs[branch]...), nil
}

func (f *fakeBranchSource) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

var errFakeGH = errors.New("fake gh down")

// このファイルの下半分は #103（親要件チケット #98 §合流と照合。AC-220〜240）の
// 委譲の後の照合を、実ストア・偽の SlotGit・偽の UpstreamBranchSource で検証する。

func mkPR(n int, state, base string) UpstreamPullRequest {
	return UpstreamPullRequest{URL: "https://github.com/o/r/pull/" + string(rune('0'+n)), Title: "PR title " + string(rune('0'+n)), State: state, Base: base}
}

// reconFixture は課題 C-1（artifacts の宣言が operation のもの）を着手中で用意する。
func newReconFixture(t *testing.T, operation string) *delegateFixture {
	t.Helper()
	f := newDelegateFixture(t)
	f.newInProgress(t, "t", "P1", planSpec(func(m map[string]any) { m["operation"] = operation }))
	return f
}

func (f *delegateFixture) setSlotBranch(branch string) {
	for p := range f.git.states {
		st := f.git.states[p]
		st.Branch = branch
		f.git.states[p] = st
	}
}

func (f *delegateFixture) artifacts(t *testing.T) []runArtifactRow {
	t.Helper()
	var out []runArtifactRow
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM run_artifact ORDER BY id`)
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
			r, err := scanRunArtifactRow(tx.QueryRow(runArtifactSelectColumns+" WHERE id = ?", id))
			if err != nil {
				return err
			}
			out = append(out, *r)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *delegateFixture) queryString(t *testing.T, q string, args ...any) string {
	t.Helper()
	var v sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&v) }); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v.String
}

func (f *delegateFixture) status(t *testing.T) Status { return f.detail(t, "C-1").Status }

// AC-220・221: launch_failed を除く 7 つの結果のどれでも照合し、launch_failed では照合しない。
func TestReconcile_RunsForEveryResultExceptLaunchFailed(t *testing.T) {
	for _, r := range []RunResult{RunResultSucceeded, RunResultErrored, RunResultMalformed, RunResultInvalidOutput, RunResultTimedOut, RunResultBudgetExhausted, RunResultInterrupted} {
		t.Run(string(r), func(t *testing.T) {
			f := newReconFixture(t, "impl")
			f.setSlotBranch("feat/slot")
			f.deleg.result = JudgmentLaunchOutput{Result: r}
			if r == RunResultSucceeded {
				f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) { m["branch"] = nil })
			}
			if _, err := f.run(t, nil); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(f.branches.callLog(), "|"); got != "exists o/r feat/slot|prs o/r feat/slot" {
				t.Errorf("gh calls = %q", got)
			}
		})
	}
	t.Run("launch_failed", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.setSlotBranch("feat/slot")
		f.deleg.result = JudgmentLaunchOutput{Result: RunResultLaunchFailed}
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if got := f.branches.callLog(); len(got) != 0 {
			t.Errorf("gh was called after launch_failed: %v", got)
		}
	})
}

// AC-222: 報告のブランチと PR の URL は、偽の gh が返さなければ記録されない。
func TestReconcile_DoesNotTrustTheReport(t *testing.T) {
	f := newReconFixture(t, "impl")
	if _, err := f.run(t, nil); err != nil { // 報告は branch feat/x と PR URL を持つ。照合は何も見つけない
		t.Fatal(err)
	}
	if a := f.artifacts(t); len(a) != 0 {
		t.Errorf("artifacts = %+v, want none", a)
	}
}

// AC-223・224: 報告のブランチがあればそれで、無ければスロットの現在のブランチで調べる。
func TestReconcile_BranchSelection(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.setSlotBranch("feat/slot")
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.branches.callLog(), "|"); got != "exists o/r feat/x|prs o/r feat/x" {
		t.Errorf("with a reported branch: calls = %q", got)
	}

	f2 := newReconFixture(t, "impl")
	f2.setSlotBranch("feat/slot")
	f2.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) { m["branch"] = nil })
	if _, err := f2.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f2.branches.callLog(), "|"); got != "exists o/r feat/slot|prs o/r feat/slot" {
		t.Errorf("with a null branch: calls = %q", got)
	}

	// 報告にも作業ツリーにもブランチが無ければ、調べる先が無い（gh を呼ばない）。
	f3 := newReconFixture(t, "impl")
	f3.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) { m["branch"] = nil })
	if _, err := f3.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f3.branches.callLog(); len(got) != 0 {
		t.Errorf("calls = %v, want none", got)
	}
}

// AC-225: 見つけた PR は URL・状態・base とともに run の成果物へ記録される。
func TestReconcile_RecordsBranchAndPRsAsArtifacts(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.branches.branches["feat/x"] = true
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "develop"), mkPR(2, "closed", "develop"), mkPR(3, "merged", "develop")}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	a := f.artifacts(t)
	if len(a) != 4 {
		t.Fatalf("artifacts = %+v", a)
	}
	if a[0].Kind != artifactKindBranch || a[0].Ref != "feat/x" || a[0].State != "" || a[0].VerifiedAt == nil {
		t.Errorf("branch artifact = %+v", a[0])
	}
	for i, want := range []UpstreamPullRequest{mkPR(1, "open", "develop"), mkPR(2, "closed", "develop"), mkPR(3, "merged", "develop")} {
		g := a[i+1]
		if g.Kind != artifactKindPR || g.Ref != want.URL || string(g.State) != want.State || g.Base != "develop" || g.VerifiedAt == nil || g.RunID != "R-2" {
			t.Errorf("pr artifact %d = %+v, want %+v", i, g, want)
		}
	}
}

// AC-227・228: 未コミットの変更が残ると needs_attention になり、clear するまで次の委譲は割り当てられない。
func TestReconcile_DirtySlotNeedsAttentionUntilCleared(t *testing.T) {
	f := newReconFixture(t, "impl")
	slotPath := f.s.Workspace() + "/slot1"
	st := f.git.states[slotPath]
	st.Dirty = true
	f.git.states[slotPath] = st
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.slotStates(t)["slot1"]; got != "needs_attention" {
		t.Fatalf("slot = %q, want needs_attention", got)
	}
	if reason := f.queryString(t, `SELECT attention_reason FROM slot WHERE path = ?`, slotPath); !strings.Contains(reason, "uncommitted") {
		t.Errorf("attention_reason = %q", reason)
	}
	// clear するまで、同じリポジトリの次の委譲は割り当てられない。
	f.git.states[slotPath] = SlotTreeState{Exists: true, PointerOK: true, OriginURL: st.OriginURL}
	f.setStatus(t, "C-1", "in_progress")
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedSlotUnavailable {
		t.Fatalf("result = %+v, want slot_unavailable", res)
	}
	if _, err := f.s.ClearSlot(context.Background(), "SL-1"); err != nil {
		t.Fatal(err)
	}
	if got := f.slotStates(t)["slot1"]; got != "idle" {
		t.Errorf("slot after clear = %q", got)
	}
}

// 作業ツリーの検査に失敗したときも、スロットを解放せず needs_attention にする。
func TestReconcile_InspectFailureNeedsAttention(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.deleg.onInvoke = func(DelegateLaunchInput) { f.git.errs[f.s.Workspace()+"/slot1"] = errFakeGH }
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.slotStates(t)["slot1"]; got != "needs_attention" {
		t.Fatalf("slot = %q", got)
	}
}

func (f *delegateFixture) setStatus(t *testing.T, id, status string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = ?, version = version + 1 WHERE id = ?`, status, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-229〜235: 成果物の確認。
func TestReconcile_ArtifactCheckMapping(t *testing.T) {
	cases := []struct {
		name      string
		operation string
		setup     func(f *delegateFixture)
		want      Status
		wantQ     string // 保留の問いに含まれる文言
	}{
		{"pr open", "impl", func(f *delegateFixture) { f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "develop")} }, StatusVerifying, ""},
		{"pr merged into an integration branch", "impl", func(f *delegateFixture) {
			f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "merged", "develop")}
		}, StatusVerifying, ""},
		{"pr closed only", "impl", func(f *delegateFixture) {
			f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "closed", "develop")}
		}, StatusAwaitingHuman, "成果物が見つからない"},
		{"pr not found", "impl", func(*delegateFixture) {}, StatusAwaitingHuman, "成果物が見つからない"},
		{"pr merged into default, release unapproved", "impl", func(f *delegateFixture) { f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "merged", "main")} }, StatusAwaitingHuman, "承認なしの本番反映"},
		{"branch exists", "impl-branch", func(f *delegateFixture) { f.branches.branches["feat/x"] = true }, StatusVerifying, ""},
		{"branch is the default branch itself", "impl-branch", func(f *delegateFixture) {
			f.branches.branches["main"] = true
			f.setSlotBranch("main")
			f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) { m["branch"] = nil })
		}, StatusAwaitingHuman, "成果物が見つからない"},
		{"branch missing", "impl-branch", func(*delegateFixture) {}, StatusAwaitingHuman, "成果物が見つからない"},
		{"none", "impl-none", func(*delegateFixture) {}, StatusVerifying, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newReconFixture(t, c.operation)
			c.setup(f)
			res, err := f.run(t, nil)
			if err != nil {
				t.Fatal(err)
			}
			d := f.detail(t, "C-1")
			if d.Status != c.want {
				t.Fatalf("status = %s, want %s", d.Status, c.want)
			}
			if res.Items[0].Status == nil || *res.Items[0].Status != string(c.want) {
				t.Errorf("item status = %v", res.Items[0].Status)
			}
			if c.want == StatusAwaitingHuman {
				if len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, c.wantQ) {
					t.Fatalf("holds = %+v, want a question containing %q", d.Holds, c.wantQ)
				}
				if c.wantQ == "成果物が見つからない" && !strings.Contains(d.Holds[0].Question, "照合の結果") {
					t.Errorf("question lacks the check results: %q", d.Holds[0].Question)
				}
				if rid := f.queryString(t, `SELECT run_id FROM hold WHERE challenge_id = 1`); rid != "2" {
					t.Errorf("hold.run_id = %q, want the delegation run", rid)
				}
			} else if len(d.Holds) != 0 {
				t.Errorf("unexpected holds %+v", d.Holds)
			}
		})
	}
}

// 提出（検証中）は経路 invoker・原因の run の ID つきで作業ログへ残る（M1 T7）。
func TestReconcile_SubmitIsLoggedAsInvokerWithRunID(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "develop")}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	got := f.queryString(t, `SELECT channel || '/' || run_id FROM activity WHERE entity = 'challenge' AND action = 'submit'`)
	if got != "invoker/2" {
		t.Errorf("submit activity = %q, want invoker/2", got)
	}
}

// 結末 completed 以外・succeeded 以外では成果物の確認（検証中・保留）をしない。
func TestReconcile_ArtifactCheckOnlyForSucceededCompleted(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) { m["outcome"] = "blocked" })
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != StatusInProgress {
		t.Errorf("status = %s", got)
	}
	f2 := newReconFixture(t, "impl")
	f2.deleg.result = JudgmentLaunchOutput{Result: RunResultErrored}
	if _, err := f2.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f2.status(t); got != StatusInProgress {
		t.Errorf("status = %s", got)
	}
}

// 取得の失敗は成果物を確かめられなかったものとして保留し、問いに失敗を書く。
func TestReconcile_UpstreamFailureHoldsWithTheError(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.branches.err = errFakeGH
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, "C-1")
	if d.Status != StatusAwaitingHuman || !strings.Contains(d.Holds[0].Question, "fake gh down") {
		t.Fatalf("status=%s holds=%+v", d.Status, d.Holds)
	}
}

// AC-236〜238: release の登録（既定ブランチの base だけ・重複なし）。
func TestReconcile_ReleaseRegistration(t *testing.T) {
	t.Run("default base registers once per PR URL", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "main")}
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		f.setStatus(t, "C-1", "in_progress")
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		ops := f.detail(t, "C-1").Operations
		if len(ops) != 1 {
			t.Fatalf("operations = %+v, want exactly one", ops)
		}
		o := ops[0]
		if o.Kind != OperationKindRelease || o.Summary != "PR title 1" || o.Ref == nil || *o.Ref != mkPR(1, "open", "main").URL || o.State != OperationStatePending {
			t.Errorf("operation = %+v", o)
		}
		got := f.queryString(t, `SELECT channel || '/' || run_id FROM activity WHERE entity = 'operation' AND action = 'op_add'`)
		if got != "invoker/2" {
			t.Errorf("op_add activity = %q, want invoker/2", got)
		}
	})
	t.Run("a different PR URL registers another release", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "main"), mkPR(2, "open", "main")}
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if ops := f.detail(t, "C-1").Operations; len(ops) != 2 {
			t.Fatalf("operations = %+v", ops)
		}
	})
	t.Run("non-default base registers nothing", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "develop"), mkPR(2, "merged", "develop")}
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if ops := f.detail(t, "C-1").Operations; len(ops) != 0 {
			t.Fatalf("operations = %+v", ops)
		}
	})
}

// AC-239・240・M3P41: 既定ブランチへのマージ済みの PR は、release が未承認なら本番反映の検出が
// 成果物の確認より優先し、承認済みならこの規則では保留しない。
func TestReconcile_UnapprovedProductionReleaseDetection(t *testing.T) {
	pr := mkPR(1, "merged", "main")
	t.Run("unapproved", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.branches.prs["feat/x"] = []UpstreamPullRequest{pr}
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		d := f.detail(t, "C-1")
		if d.Status != StatusAwaitingHuman || !strings.Contains(d.Holds[0].Question, "承認なしの本番反映を検出") || !strings.Contains(d.Holds[0].Question, pr.URL) {
			t.Fatalf("status=%s holds=%+v", d.Status, d.Holds)
		}
	})
	t.Run("detected even when the result is not succeeded", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.branches.prs["feat/x"] = []UpstreamPullRequest{pr}
		f.deleg.result = JudgmentLaunchOutput{Result: RunResultErrored}
		f.setSlotBranch("feat/x")
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if got := f.status(t); got != StatusAwaitingHuman {
			t.Errorf("status = %s", got)
		}
	})
	t.Run("approved", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.branches.prs["feat/x"] = []UpstreamPullRequest{pr}
		// 先に承認済みの release を持たせる（同じ PR の URL）。
		cid := int64(1)
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at) VALUES (?, 'release', 's', ?, 'approved', 2, '2026-09-25T00:00:00.000Z')`, cid, pr.URL)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		d := f.detail(t, "C-1")
		if d.Status != StatusVerifying || len(d.Holds) != 0 || len(d.Operations) != 1 {
			t.Fatalf("status=%s holds=%+v ops=%+v", d.Status, d.Holds, d.Operations)
		}
	})
}

// 子が報告したブランチ名が git のブランチ名として不正なら、gh を呼ばない。
func TestReconcile_InvalidReportedBranchNameIsNotLookedUp(t *testing.T) {
	for _, b := range []string{"x/../..", "-rf", "a b", "a//b", "feat/.hidden", "x.lock", "a~b"} {
		f := newReconFixture(t, "impl-branch")
		f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) { m["branch"] = b })
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if got := f.branches.callLog(); len(got) != 0 {
			t.Errorf("branch %q: gh was called: %v", b, got)
		}
		if f.status(t) != StatusAwaitingHuman {
			t.Errorf("branch %q: status = %s", b, f.status(t))
		}
	}
}

// タイトルの無い PR でも release の登録で失敗しない（要約は URL）。
func TestReconcile_ReleaseForPRWithoutTitle(t *testing.T) {
	f := newReconFixture(t, "impl")
	pr := mkPR(1, "open", "main")
	pr.Title = ""
	f.branches.prs["feat/x"] = []UpstreamPullRequest{pr}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if ops := f.detail(t, "C-1").Operations; len(ops) != 1 || ops[0].Summary != pr.URL {
		t.Fatalf("operations = %+v", ops)
	}
}
