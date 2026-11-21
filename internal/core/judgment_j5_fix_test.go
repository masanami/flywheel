package core

// このファイルは、コンフリクトの解消の確かめ（J5 の 3 つ目の動き方。区切り S3c）の、対象選び・入力・実行の記録・結果の振り分けの口を、実ストア・
// 偽の判断の呼び出し・偽の比較の口で固定する。受入基準ごとに、基準の文が述べる条件・結果・値を確かめる。結果の書き込み（列の更新・作業ログ・
// 承認の取り消し）は別のタスクが足すので、ここでは「結果を書く直前の読み直しを通ったときだけ、宛先ごとの口へ、結果が届く」ことまでを確かめる。

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
)

const (
	fixPassed  = "a0a0a0a"
	fixPending = "c0ffee1" // 統合先の試験の足場の契約の実行の中身の識別（reviewContentArtifact）と同じ
)

// fixHeadGit は確かめ待ちの中身を取り出す一時の worktree の口の偽の実装（手元のクローンに中身がある）。
type fixHeadGit struct {
	noHeadGit
	mu    sync.Mutex
	added []string // 取り出した sha
	paths []string
}

func (g *fixHeadGit) HasCommit(context.Context, string, string) (bool, error) { return true, nil }
func (g *fixHeadGit) AddHeadWorktree(_ context.Context, _, path, sha string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.added, g.paths = append(g.added, sha), append(g.paths, path)
	return nil
}

// fixWriterLog は宛先ごとの書き込みの口へ届いた結果の記録。
type fixWriterLog struct {
	mu      sync.Mutex
	results []j5FixResult
	dests   []j5FixDest
}

func (l *fixWriterLog) writer(d j5FixDest) j5FixWriter {
	return func(_ context.Context, _ *sql.Tx, _ *activityRecorder, r *j5FixResult) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.results, l.dests = append(l.results, *r), append(l.dests, d)
		return nil
	}
}

// setConflictFixHistory は、通した中身 fixPassed から確かめ待ちの中身 fixPending へ、宛先 base の最新を取り込むマージ（コンフリクトの解消あり）で動いた履歴を置く。
// 直したファイルは a.go・b.go（どちらも差分あり。差分を上流が返さないファイルがあると「満たした」は通らないので、通る足場は読めるファイルだけにする）。
func setConflictFixHistory(cmp *fakeCompare, base string) {
	cmp.set(fixPassed, fixPending, ahead(commit("m1", "main change", "m0"), commit(fixPending, "Merge branch 'main'", fixPassed, "m1")))
	cmp.set(base, fixPending, ahead(commit("p0", "feat", "m0"), commit(fixPassed, "fix", "p0"), commit(fixPending, "Merge branch 'main'", fixPassed, "m1")))
	files := []UpstreamFile{{Filename: "a.go", Patch: "@@ -1 +1 @@\n-old\n+RESOLVED-LINE"}, {Filename: "b.go", Patch: "@@ -2 +2 @@\n-old2\n+RESOLVED-B"}}
	cmp.set(fixPassed, fixPending, withFiles(cmp.results[[2]string{fixPassed, fixPending}], files...))
	cmp.set("m1", fixPending, UpstreamComparison{Status: UpstreamCompareAhead, TotalCommits: 1,
		Commits: []UpstreamCommit{commit(fixPassed, "fix", "p0")}, Files: files})
}

type fixFixture struct {
	*reviewFlowFixture
	cmp *fakeCompare
	git *fixHeadGit
	log *fixWriterLog
}

// newFixIntegrationFixture は、統合先を使う着手中の課題 C-1 の検証中のタスクが、検証した中身（fixPassed）と確かめ待ちの中身（fixPending）を持つ足場。
func newFixIntegrationFixture(t *testing.T) *fixFixture {
	t.Helper()
	f := &fixFixture{reviewFlowFixture: newTaskVerifyFixture(t), cmp: newFakeCompare(), git: &fixHeadGit{}, log: &fixWriterLog{}}
	f.sql(t, `UPDATE contract_run SET verified_ref = ?, fix_pending_ref = ?`, fixPassed, fixPending)
	setConflictFixHistory(f.cmp, integBranch)
	f.s.fixWriters = j5FixWriters{Integration: f.log.writer(j5FixDestIntegration), Reflect: f.log.writer(j5FixDestReflect)}
	return f
}

// newFixReflectFixture は、完了の課題 C-1（統合先を使わない）の、承認済みの反映の操作（承認した中身 fixPassed）に対応するタスクの実行が、確かめ待ちの中身を持つ足場。
func newFixReflectFixture(t *testing.T) *fixFixture {
	t.Helper()
	f := &fixFixture{reviewFlowFixture: newTaskVerifyFixture(t), cmp: newFakeCompare(), git: &fixHeadGit{}, log: &fixWriterLog{}}
	cid, _ := parseChallengeID(f.cid)
	f.sql(t, `UPDATE challenge SET uses_integration = 0, status = 'done'`)
	f.sql(t, `INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at) VALUES (?, 'release', 's', ?, 'approved', 1, '2026-09-25T00:00:00.000Z')`, cid, reviewPRURL)
	f.sql(t, `INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, head_sha, decided_at)
		SELECT ?, id, 'release', 'approved', 1, 'alice', 'cli', 'tty_confirm', ?, '2026-09-25T00:00:00.000Z' FROM operation`, cid, fixPassed)
	f.sql(t, `UPDATE contract_run SET fix_pending_ref = ?`, fixPending)
	// 統合先を使わない課題のタスクの実行は、取り込んだ成果物（PR）の参照で反映の操作に対応する（反映の段と同じ決め方）。
	f.sql(t, `INSERT INTO run_artifact (run_id, kind, ref, state, base) SELECT start_run_id, 'pr', ?, 'open', 'main' FROM contract_run`, reviewPRURL)
	setConflictFixHistory(f.cmp, "main")
	f.s.fixWriters = j5FixWriters{Integration: f.log.writer(j5FixDestIntegration), Reflect: f.log.writer(j5FixDestReflect)}
	return f
}

func (f *fixFixture) input(t *testing.T, id *string) J5AutoInput {
	t.Helper()
	in := f.j5.input(f.cycle(t, 300), id)
	in.Compare, in.HeadGit, in.Connectors = f.cmp, f.git, f.conn
	return in
}

func (f *fixFixture) verify(t *testing.T) *JudgmentAutoResult {
	t.Helper()
	res, err := f.s.VerifyAutoJ5(context.Background(), f.input(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (f *fixFixture) fixTargets(t *testing.T) []j5FixTarget {
	t.Helper()
	var out []j5FixTarget
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, _, err = selectJ5FixTargets(context.Background(), tx, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *fixFixture) pendingRef(t *testing.T) string {
	return f.text(t, `SELECT fix_pending_ref FROM contract_run`)
}

func (f *fixFixture) j5RunCount(t *testing.T) int64 {
	return f.count(t, `SELECT COUNT(*) FROM run WHERE judgment = 'J5'`)
}

// 入力の区画の本文（最後の J5 の呼び出し）。
func (f *fixFixture) sectionsOf(t *testing.T) map[string]string {
	t.Helper()
	if len(f.j5.j5Inputs) == 0 {
		t.Fatal("J5 was not invoked")
	}
	out := map[string]string{}
	for _, s := range f.j5.j5Inputs[len(f.j5.j5Inputs)-1].Sections {
		out[s.Label] = s.Content
	}
	return out
}

// AC-7・AC-8: 確かめ待ちの中身を持つタスクは、タスクの検証の対象にならず、コンフリクトの解消の確かめの対象になる。確かめ待ちの中身が無くなれば、逆になる。
func TestJ5Fix_PendingTaskIsFixTargetNotTaskVerificationTarget(t *testing.T) {
	t.Parallel()
	f := newFixIntegrationFixture(t)
	f.sql(t, `UPDATE contract_run SET verified_ref = NULL`) // 検証した中身が無くても、確かめ待ちの中身があれば、タスクの検証の対象にならない
	var taskTargets []j5TaskTarget
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		taskTargets, err = selectJ5TaskTargets(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(taskTargets) != 0 { // AC-7
		t.Fatalf("task verification targets = %+v, want none", taskTargets)
	}
	if got := f.fixTargets(t); len(got) != 1 || got[0].Dest != j5FixDestIntegration || got[0].TaskID == nil { // AC-8
		t.Fatalf("fix targets = %+v, want one integration target", got)
	}
	f.sql(t, `UPDATE contract_run SET fix_pending_ref = NULL`)
	if got := f.fixTargets(t); len(got) != 0 {
		t.Fatalf("fix targets = %+v, want none without a pending ref", got)
	}
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		taskTargets, err = selectJ5TaskTargets(context.Background(), tx)
		return err
	}); err != nil || len(taskTargets) != 1 {
		t.Fatalf("task verification targets = %+v (%v), want one", taskTargets, err)
	}
}

// AC-9: 保留中の課題の、確かめ待ちの中身を持つタスクの実行は、確かめの対象にならない。確かめ待ちの中身は変わらない。
func TestJ5Fix_HeldChallengeIsNotTargeted(t *testing.T) {
	t.Parallel()
	f := newFixIntegrationFixture(t)
	f.holdByTheRealPath(t)
	if got := f.fixTargets(t); len(got) != 0 {
		t.Fatalf("fix targets = %+v, want none", got)
	}
	f.verify(t)
	if n := f.j5RunCount(t); n != 0 {
		t.Fatalf("J5 runs = %d, want 0", n)
	}
	if got := f.pendingRef(t); got != fixPending {
		t.Fatalf("fix_pending_ref = %q, want unchanged", got)
	}
}

// 取り込み元の食い違い（上流が閉じられた）で自動の対象から外す決まりは、確かめにも当たる。外した課題は not_started に理由つきで出る。
func TestJ5Fix_UpstreamMismatchExcludesFromAutoTargets(t *testing.T) {
	t.Parallel()
	f := newFixIntegrationFixture(t)
	f.sql(t, `INSERT INTO source_binding (challenge_id, source_id, external_key, url, fingerprint, upstream_state, policy_state, created_at, updated_at)
		SELECT id, 'src', 'o/r#7', 'https://example.invalid/o/r/issues/7', 'fp', 'closed', 'in_policy', '2026-09-25T00:00:00.000Z', '2026-09-25T00:00:00.000Z' FROM challenge`)
	res := f.verify(t)
	if f.j5RunCount(t) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamClosed {
		t.Fatalf("J5 runs = %d, not_started = %+v, want no run and upstream_closed", f.j5RunCount(t), res.NotStarted)
	}
}

// AC-10・AC-82〜AC-85・AC-87・AC-88・AC-90・AC-94・AC-95・AC-108・AC-109: 統合先宛ての確かめ。
func TestJ5Fix_IntegrationRunInputAndRecords(t *testing.T) {
	t.Parallel()
	f := newFixIntegrationFixture(t)
	f.sql(t, `UPDATE task SET source_key = 'child-1'`)
	addChildRecord(t, f.delegateFixture, f.cid, "child-1")
	f.setVerdict("met")
	task := f.count(t, `SELECT id FROM task`)
	budgetBefore, err := f.s.GetChallengeBudget(context.Background(), f.cid, nil)
	if err != nil {
		t.Fatal(err)
	}

	res := f.verify(t)

	if len(res.Items) != 1 || res.Items[0].Outcome != "met" {
		t.Fatalf("items = %+v, want one met", res.Items)
	}
	ids := f.j5RunTaskIDs(t)
	if len(ids) != 1 || !ids[0].Valid || ids[0].Int64 != task { // AC-10・AC-108
		t.Fatalf("J5 run task_ids = %v, want [%d]", ids, task)
	}
	sec := f.sectionsOf(t)
	if sec[labelFixPassedSection] != fixPassed { // AC-82
		t.Errorf("passed section = %q", sec[labelFixPassedSection])
	}
	if sec[labelFixPendingSection] != fixPending { // AC-83
		t.Errorf("pending section = %q", sec[labelFixPendingSection])
	}
	diff := sec[labelFixDiffSection]
	for name, want := range map[string]string{
		"AC-84 merge commit": fixPending, "AC-84 file name": "a.go", "AC-85 patch": "+RESOLVED-LINE", "AC-85 second file": "+RESOLVED-B",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("%s: diff section lacks %q:\n%s", name, want, diff)
		}
	}
	if !strings.Contains(sec["達成条件"], f.text(t, `SELECT done_criteria FROM task`)) { // AC-87
		t.Errorf("criteria section = %q, want the task's done criteria", sec["達成条件"])
	}
	if !strings.Contains(sec[labelTaskSourceSection], "CHILD-ONLY-BODY") { // AC-88
		t.Errorf("task source section = %q, want the child's body", sec[labelTaskSourceSection])
	}
	if len(f.git.added) != 1 || f.git.added[0] != fixPending { // AC-90
		t.Fatalf("worktrees added for %v, want [%s]", f.git.added, fixPending)
	}
	if got := f.j5.j5Inputs[0].WorkDir; got == "" || got != f.git.paths[0] {
		t.Errorf("work dir = %q, want the worktree %q", got, f.git.paths[0])
	}
	budgetAfter, err := f.s.GetChallengeBudget(context.Background(), f.cid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if budgetAfter.Tasks[0].SpentUSD != budgetBefore.Tasks[0].SpentUSD { // AC-94
		t.Errorf("task spent_usd %v → %v, want unchanged", budgetBefore.Tasks[0].SpentUSD, budgetAfter.Tasks[0].SpentUSD)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE judgment = 'J5' AND budget_bucket = 'judgment' AND cost_usd > 0`); n != 1 { // AC-95
		t.Errorf("J5 runs counted as judgment cost = %d, want 1", n)
	}
	if got := f.pendingRef(t); got != fixPending {
		t.Errorf("fix_pending_ref = %q, J5 must not write the columns", got)
	}
}

// AC-45・AC-46・AC-82〜AC-84・AC-89・AC-108: 反映先宛ての確かめ。run は task_id を持たず、入力に「完了したら見えるもの」が入る。
// 対象は、統合先を使わない課題のタスクの実行・上げる依頼・委譲先のフローを通らない成果物の反映の依頼（実行に結び付く反映の操作）。
func TestJ5Fix_ReflectRunInputAndRecords(t *testing.T) {
	t.Parallel()
	for name, prep := range map[string]func(*fixFixture, *testing.T){
		"task run of a challenge without an integration branch": func(*fixFixture, *testing.T) {},
		"reflect request linked to the operation": func(f *fixFixture, t *testing.T) {
			f.sql(t, `UPDATE contract_run SET kind = 'reflect', task_id = NULL, operation_id = (SELECT id FROM operation)`)
		},
	} {
		prep := prep
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixReflectFixture(t)
			prep(f, t)
			f.sql(t, `UPDATE challenge SET expected = ?`, `[{"scene":"SCENE-ONE","before":"BEFORE-ONE","after":"AFTER-ONE"}]`)
			f.setVerdict("not_met")
			if got := f.fixTargets(t); len(got) != 1 || got[0].Dest != j5FixDestReflect || got[0].TaskID != nil { // AC-45
				t.Fatalf("fix targets = %+v, want one reflect target", got)
			}
			res := f.verify(t)
			if len(res.Items) != 1 || res.Items[0].Outcome != "not_met" {
				t.Fatalf("items = %+v, want one not_met", res.Items)
			}
			ids := f.j5RunTaskIDs(t)
			if len(ids) != 1 || ids[0].Valid { // AC-46
				t.Fatalf("J5 run task_ids = %v, want one NULL", ids)
			}
			sec := f.sectionsOf(t)
			if sec[labelFixPassedSection] != fixPassed || sec[labelFixPendingSection] != fixPending { // AC-82・AC-83
				t.Errorf("passed = %q, pending = %q", sec[labelFixPassedSection], sec[labelFixPendingSection])
			}
			if !strings.Contains(sec[labelFixDiffSection], "+RESOLVED-LINE") { // AC-84・AC-85
				t.Errorf("diff section = %q", sec[labelFixDiffSection])
			}
			if !strings.Contains(sec["達成条件"], "SCENE-ONE") || !strings.Contains(sec["達成条件"], "AFTER-ONE") { // AC-89
				t.Errorf("criteria section = %q, want the scenes of 完了したら見えるもの", sec["達成条件"])
			}
			if _, ok := sec[labelTaskSection]; ok {
				t.Errorf("a reflect-destination check must not carry the task section")
			}
			if len(f.git.added) != 1 || f.git.added[0] != fixPending { // AC-90
				t.Fatalf("worktrees added for %v, want [%s]", f.git.added, fixPending)
			}
		})
	}
}

// 反映の操作が終わっている（反映済み・閉じた）完了の課題の実行は、反映先宛ての対象にならない。
func TestJ5Fix_ReflectTargetNeedsAnOpenOperation(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"executed", "closed", "rejected"} {
		f := newFixReflectFixture(t)
		f.sql(t, `UPDATE operation SET state = ?`, state)
		if got := f.fixTargets(t); len(got) != 0 {
			t.Errorf("operation %s: fix targets = %+v, want none", state, got)
		}
	}
}

// AC-108: 確かめの結果が 3 つのどれでも、J5 の run が 1 件残り、宛先に合った書き込みの口へ、結果が 1 件届く。J5 の出力の形は変えない
// （満たした・満たしていない・判断できないを、それぞれ読む）。
func TestJ5Fix_EveryVerdictLeavesOneRunAndReachesTheDestinationWriter(t *testing.T) {
	t.Parallel()
	for _, dest := range []j5FixDest{j5FixDestIntegration, j5FixDestReflect} {
		for _, verdict := range []string{"met", "not_met", "uncertain"} {
			dest, verdict := dest, verdict
			t.Run(string(dest)+"/"+verdict, func(t *testing.T) {
				t.Parallel()
				f := newFixIntegrationFixture(t)
				if dest == j5FixDestReflect {
					f = newFixReflectFixture(t)
				}
				f.setVerdict(verdict)
				f.verify(t)
				if n := f.j5RunCount(t); n != 1 {
					t.Fatalf("J5 runs = %d, want 1", n)
				}
				if len(f.log.results) != 1 || f.log.dests[0] != dest || f.log.results[0].Scope.Target.Dest != dest {
					t.Fatalf("writer calls = %v, want one for %s", f.log.dests, dest)
				}
				r := f.log.results[0]
				if string(r.Verdict) != verdict || r.RunID == "" || r.Scope.PassedRef != fixPassed || r.Scope.PendingRef != fixPending {
					t.Fatalf("result = %+v", r)
				}
				if verdict == "not_met" && r.Feedback != "FEEDBACK-TEXT" {
					t.Errorf("feedback = %q", r.Feedback)
				}
				if verdict == "uncertain" && r.Question != "QUESTION-TEXT" {
					t.Errorf("question = %q", r.Question)
				}
				if got := f.pendingRef(t); got != fixPending {
					t.Errorf("fix_pending_ref = %q, the J5 stage must leave the columns to the writers", got)
				}
			})
		}
	}
}

// AC-33: 入力を組み立てるときに読み直した変わり方が、コンフリクトの解消ありでない（比較の口が無い・取り込みだけ・関係のない変更あり・判定できない）→ 実行の記録は作られない。
func TestJ5Fix_NoConflictFixMeansNoRun(t *testing.T) {
	t.Parallel()
	cases := map[string]func(f *fixFixture, in *J5AutoInput){
		"no compare source": func(_ *fixFixture, in *J5AutoInput) { in.Compare = nil },
		"merge-in only": func(f *fixFixture, _ *J5AutoInput) {
			f.cmp.set("m1", fixPending, UpstreamComparison{Status: UpstreamCompareAhead, TotalCommits: 1, Commits: []UpstreamCommit{commit(fixPassed, "fix", "p0")},
				Files: []UpstreamFile{{Filename: "other.go", Patch: "@@"}}})
		},
		"a commit that is not an import is added": func(f *fixFixture, _ *J5AutoInput) {
			f.cmp.set(fixPassed, fixPending, ahead(commit("x1", "feat: more", fixPassed), commit(fixPending, "Merge", "x1", "m1")))
			f.cmp.set(integBranch, fixPending, ahead(commit("x1", "feat: more", fixPassed), commit(fixPassed, "fix", "p0"), commit(fixPending, "Merge", "x1", "m1")))
		},
		"comparison fails": func(f *fixFixture, _ *J5AutoInput) {
			f.cmp.errs[[2]string{fixPassed, fixPending}] = context.DeadlineExceeded
		},
	}
	for name, change := range cases {
		change := change
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixIntegrationFixture(t)
			in := f.input(t, nil)
			change(f, &in)
			res, err := f.s.VerifyAutoJ5(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if n := f.j5RunCount(t); n != 0 || len(res.Items) != 0 {
				t.Fatalf("J5 runs = %d, items = %+v, want none", n, res.Items)
			}
			if len(f.git.added) != 0 || len(f.log.results) != 0 {
				t.Fatalf("worktrees = %v, writer calls = %d, want none", f.git.added, len(f.log.results))
			}
			if got := f.pendingRef(t); got != fixPending {
				t.Fatalf("fix_pending_ref = %q, want unchanged", got)
			}
		})
	}
}

// AC-97: 統合先宛てで、同じタスクに終わっていない実行（run）があるとき、確かめの実行の記録は作られず、確かめ待ちの中身は変わらない。
// AC-98: 反映先宛てで、同じ課題に、タスクに結び付かない終わっていない実行（run）があるときも同じ。
func TestJ5Fix_ActiveRunBlocksTheCheck(t *testing.T) {
	t.Parallel()
	insertActive := func(f *fixFixture, t *testing.T, taskID *int64) {
		t.Helper()
		cid, _ := parseChallengeID(f.cid)
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := insertRun(context.Background(), tx, cid, insertRunInput{Kind: runKindJudgment, Judgment: JudgmentJ5, ChallengeVersion: 1, TaskID: taskID,
				SessionID: "55555555-5555-4555-8555-555555555555", PID: 1, Host: "h", HeartbeatAt: f.s.currentTime(), StartedAt: f.s.currentTime(), MaxBudgetUSD: 1, BudgetBucket: budgetBucketJudgment})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("integration: the same task", func(t *testing.T) {
		t.Parallel()
		f := newFixIntegrationFixture(t)
		task := f.count(t, `SELECT id FROM task`)
		insertActive(f, t, &task)
		f.verify(t)
		if n := f.j5RunCount(t); n != 1 || len(f.log.results) != 0 { // 仕込んだ 1 件だけ
			t.Fatalf("J5 runs = %d, writer calls = %d, want only the planted run", n, len(f.log.results))
		}
		if got := f.pendingRef(t); got != fixPending {
			t.Fatalf("fix_pending_ref = %q", got)
		}
	})
	t.Run("reflect: a run without a task", func(t *testing.T) {
		t.Parallel()
		f := newFixReflectFixture(t)
		insertActive(f, t, nil)
		f.verify(t)
		if n := f.j5RunCount(t); n != 1 || len(f.log.results) != 0 {
			t.Fatalf("J5 runs = %d, writer calls = %d, want only the planted run", n, len(f.log.results))
		}
		if got := f.pendingRef(t); got != fixPending {
			t.Fatalf("fix_pending_ref = %q", got)
		}
	})
}

// AC-96: 同じ課題に、検証中のタスクと、確かめ待ちの中身を持つ別のタスクがあるとき、J5 の段は両方を処理する（見直し前の対象を先に）。
func TestJ5Fix_BothTheVerifyingTaskAndThePendingTaskAreProcessed(t *testing.T) {
	t.Parallel()
	f := newFixIntegrationFixture(t)
	f.addSecondVerifyingTask(t, "") // 2 個目のタスク（中身の識別 beef000、検証した中身は無く、確かめ待ちの中身も無い）
	f.setVerdict("met")
	f.verify(t)
	ids := f.j5RunTaskIDs(t)
	if len(ids) != 2 {
		t.Fatalf("J5 runs = %v, want 2 (the task verification and the conflict-fix check)", ids)
	}
	first, second := f.count(t, `SELECT id FROM task WHERE key <> 'second' ORDER BY id LIMIT 1`), f.count(t, `SELECT id FROM task WHERE key = 'second'`)
	if ids[0].Int64 != second || ids[1].Int64 != first {
		t.Fatalf("J5 run task_ids = %v, want the verifying task %d first and the pending task %d after", ids, second, first)
	}
	if got := f.text(t, `SELECT verified_ref FROM contract_run WHERE external_run_id = 'ext-2'`); got != "beef000" {
		t.Errorf("the other task's verified_ref = %q, want the task verification to have run", got)
	}
	if len(f.log.results) != 1 {
		t.Fatalf("writer calls = %d, want 1 (only the conflict-fix check)", len(f.log.results))
	}
}

// 結果を書く直前に、契約の実行の中身の識別が、確かめを始めたときの確かめ待ちの中身と違っていれば、結果を捨てる（書き込みの口へ届けず、列も変えない）。
// 課題が保留中になっていたときも捨てる。
func TestJ5Fix_ResultIsDiscardedWhenTheTargetMovedMeanwhile(t *testing.T) {
	t.Parallel()
	sqlMove := func(q string) func(*testing.T, *fixFixture) {
		return func(t *testing.T, f *fixFixture) { f.sql(t, q) }
	}
	cases := map[string]func(*testing.T, *fixFixture){
		"content moved on":    sqlMove(`UPDATE contract_run SET content_ref = 'newer01'`),
		"pending ref changed": sqlMove(`UPDATE contract_run SET fix_pending_ref = 'newer01'`),
		"challenge held":      func(t *testing.T, f *fixFixture) { f.holdByTheRealPath(t) }, // 保留への遷移は本物の経路で行う
		"run ended":           sqlMove(`UPDATE contract_run SET ended_at = '2026-09-25T00:00:00.000Z'`),
	}
	for name, move := range cases {
		move := move
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixIntegrationFixture(t)
			f.setVerdict("met")
			f.j5.j5.onInvoke = func(JudgmentLaunchInput) { move(t, f) } // J5 の呼び出しの間に動く
			res := f.verify(t)
			if n := f.j5RunCount(t); n != 1 {
				t.Fatalf("J5 runs = %d, want 1", n)
			}
			if len(f.log.results) != 0 {
				t.Fatalf("writer calls = %d, want the result to be discarded", len(f.log.results))
			}
			if len(res.Items) != 1 || res.Items[0].Status != nil {
				t.Fatalf("items = %+v, want one item without a written status", res.Items)
			}
		})
	}
}

// J5 の出力が検査に落ちたとき（判定が不正）、run の結果は invalid_output になり、書き込みの口へは何も届かない。
func TestJ5Fix_InvalidOutputIsNotDispatched(t *testing.T) {
	t.Parallel()
	f := newFixIntegrationFixture(t)
	f.j5.j5.result = j5Out("not_met", nil, nil) // 満たしていないなのに指摘が無い
	res := f.verify(t)
	if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput || len(f.log.results) != 0 {
		t.Fatalf("items = %+v, writer calls = %d, want invalid_output and no writer call", res.Items, len(f.log.results))
	}
}

// J5 の呼び出しが連続の失敗の上限に達したら、判断できないと同じに扱う（J5 を呼ばず、判断できないとして、書き込みの口へ届ける）。確かめに回数の上限は足さない
// （満たしていないを何度返しても、起動は続く）。
func TestJ5Fix_FailureLimitIsTreatedAsUncertain(t *testing.T) {
	t.Parallel()
	f := newFixIntegrationFixture(t)
	f.j5.j5.result = JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	for i := 0; i < judgmentFailureLimit(f.j5.agent); i++ {
		f.verify(t)
	}
	if n := f.j5RunCount(t); n != int64(judgmentFailureLimit(f.j5.agent)) || len(f.log.results) != 0 {
		t.Fatalf("J5 runs = %d, writer calls = %d, want the failed runs only", n, len(f.log.results))
	}
	f.verify(t) // 上限に達した後は、J5 を呼ばない
	if n := f.j5RunCount(t); n != int64(judgmentFailureLimit(f.j5.agent)) {
		t.Fatalf("J5 runs = %d, want no more invocation at the limit", n)
	}
	if len(f.log.results) != 1 || f.log.results[0].Verdict != J5VerdictUncertain || !f.log.results[0].LimitReached {
		t.Fatalf("writer calls = %+v, want one uncertain result for the limit", f.log.results)
	}
}

func TestJ5Fix_NotMetDoesNotCapTheNumberOfChecks(t *testing.T) {
	t.Parallel()
	f := newFixIntegrationFixture(t)
	f.setVerdict("not_met")
	for i := 0; i < judgmentFailureLimit(f.j5.agent)+2; i++ {
		f.verify(t)
	}
	if n, want := f.j5RunCount(t), int64(judgmentFailureLimit(f.j5.agent)+2); n != want {
		t.Fatalf("J5 runs = %d, want %d（確かめに回数の上限は無い）", n, want)
	}
}

// ID を指定した操作でも、着手中の課題の確かめと、完了の課題の確かめが処理される。対象が無ければ、握りつぶさずに ErrInvalidTransition を返す。
func TestJ5Fix_ChallengeIDPathProcessesTargetsAndReportsNone(t *testing.T) {
	t.Parallel()
	for name, mk := range map[string]func(*testing.T) *fixFixture{"integration": newFixIntegrationFixture, "reflect": newFixReflectFixture} {
		mk := mk
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := mk(t)
			f.setVerdict("met")
			id := f.cid
			res, err := f.s.VerifyAutoJ5(context.Background(), f.input(t, &id))
			if err != nil || len(res.Items) != 1 || f.j5RunCount(t) != 1 {
				t.Fatalf("items = %+v, err = %v, J5 runs = %d, want one check", res, err, f.j5RunCount(t))
			}
			f.sql(t, `UPDATE contract_run SET fix_pending_ref = NULL`)
			_, err = f.s.VerifyAutoJ5(context.Background(), f.input(t, &id))
			// 統合先宛ては、確かめ待ちが無くなればタスクの検証の対象に戻る。反映先宛ては対象が無く、握りつぶさずにエラーを返す。
			if wantErr := name == "reflect"; (err != nil) != wantErr {
				t.Fatalf("err = %v, want error: %v", err, wantErr)
			}
		})
	}
}

// 1 本のタスクの実行に対応する終わっていない反映の操作が 2 つ（取り込んだ成果物が 2 つ）あるとき、反映先宛ての対象にしない（3 列はどの操作の中身かを
// 持たない）。J5 は呼ばず、確かめ待ちの中身は変わらない。1 つのときは対象になる。
func TestJ5Fix_ReflectTargetNeedsExactlyOneCorrespondingOperation(t *testing.T) {
	t.Parallel()
	f := newFixReflectFixture(t)
	if got := f.fixTargets(t); len(got) != 1 {
		t.Fatalf("one operation: fix targets = %+v, want one", got)
	}
	cid, _ := parseChallengeID(f.cid)
	f.sql(t, `INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at) VALUES (?, 'release', 's2', 'https://github.com/o/r/pull/2', 'pending', 1, '2026-09-25T00:00:00.000Z')`, cid)
	f.sql(t, `INSERT INTO run_artifact (run_id, kind, ref, state, base) SELECT start_run_id, 'pr', 'https://github.com/o/r/pull/2', 'open', 'main' FROM contract_run`)
	if got := f.fixTargets(t); len(got) != 0 {
		t.Fatalf("two operations: fix targets = %+v, want none", got)
	}
	res := f.verify(t)
	if n := f.j5RunCount(t); n != 0 || len(res.Items) != 0 {
		t.Fatalf("J5 runs = %d, items = %+v, want none", n, res.Items)
	}
	if got := f.pendingRef(t); got != fixPending {
		t.Fatalf("fix_pending_ref = %q, want unchanged", got)
	}
}

// 確かめ待ちの中身を取り出せなかったとき、J5 を呼ばず、実行の記録も作らず、列も変えず、始めなかったもの（upstream_fetch_failed。詳細に取り出せなかった
// 理由）として返す（ワークスペースを作業ディレクトリにして続けない）。
func TestJ5Fix_NoCheckWhenThePendingContentCannotBeCheckedOut(t *testing.T) {
	t.Parallel()
	for name, mk := range map[string]func(*testing.T) *fixFixture{"integration": newFixIntegrationFixture, "reflect": newFixReflectFixture} {
		mk := mk
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := mk(t)
			in := f.input(t, nil)
			git := &noHeadGit{}
			in.HeadGit = git
			res, err := f.s.VerifyAutoJ5(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if n := f.j5RunCount(t); n != 0 || len(res.Items) != 0 || len(f.log.results) != 0 || git.added != 0 {
				t.Fatalf("J5 runs = %d, items = %+v, writer calls = %d, worktrees = %d, want none", n, res.Items, len(f.log.results), git.added)
			}
			if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamFetchFailed || !strings.Contains(res.NotStarted[0].Detail, fixPending) {
				t.Fatalf("not started = %+v, want one upstream_fetch_failed naming %s", res.NotStarted, fixPending)
			}
			if got := f.pendingRef(t); got != fixPending {
				t.Fatalf("fix_pending_ref = %q, want unchanged", got)
			}
		})
	}
}
