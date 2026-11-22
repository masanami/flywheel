package core

// このファイルは、タスクの検証（統合先を使う着手中の課題の、検証中のタスク 1 個を対象にする J5。区切り S3b）を、実ストア・偽の
// 判断の呼び出し・偽の契約のコマンドで固定する。受入基準ごとに、基準の文が述べる条件・結果・値を確かめる。

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

// taskVerifyFixture は、統合先を使う課題 C-1 が着手中で、タスクが検証中（成果物は取り込み済み。中身の識別は c0ffee1）の足場。
func newTaskVerifyFixture(t *testing.T) *reviewFlowFixture {
	t.Helper()
	f := newIntegrationFixture(t)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 1 {
		t.Fatalf("verifying tasks = %d, want 1", n)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress", got)
	}
	return f
}

func (f *reviewFlowFixture) verifyTasks(t *testing.T, id *string) (*JudgmentAutoResult, error) {
	t.Helper()
	return f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), id))
}

func (f *reviewFlowFixture) setVerdict(verdict string) {
	switch verdict {
	case "met":
		f.j5.j5.result = j5Out("met", nil, nil)
	case "not_met":
		f.j5.j5.result = j5Out("not_met", "FEEDBACK-TEXT", nil)
	default:
		f.j5.j5.result = j5Out("uncertain", nil, "QUESTION-TEXT")
	}
}

func (f *reviewFlowFixture) j5RunTaskIDs(t *testing.T) []sql.NullInt64 {
	t.Helper()
	var out []sql.NullInt64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT task_id FROM run WHERE judgment = 'J5' ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var v sql.NullInt64
			if err := rows.Scan(&v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *reviewFlowFixture) verifiedRefs(t *testing.T) []string {
	t.Helper()
	var out []string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT COALESCE(verified_ref, '<null>') FROM contract_run ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// addSecondVerifyingTask は 2 個目のタスク（検証中）と、その契約の実行（中身の識別 beef000・成果物つきの取り込みの読み）を足す。
func (f *reviewFlowFixture) addSecondVerifyingTask(t *testing.T, sourceKey string) {
	t.Helper()
	cidInt, _ := parseChallengeID(f.cid)
	f.sql(t, `INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, source_key, created_at, updated_at)
		SELECT challenge_id, plan_version, 'second', 'SECOND-TITLE', 'SECOND-BODY', 'SECOND-DONE', repo, operation, 'verifying', ?, created_at, updated_at FROM task ORDER BY id LIMIT 1`, nullableTextColumn(sourceKey))
	task2 := f.count(t, `SELECT id FROM task WHERE key = 'second'`)
	f.sql(t, `INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd,
			started_at, observed_at, task_id, requested_kind, requested_decider, content_ref)
		SELECT challenge_id, connector_id, 'ext-2', start_run_id, 'waiting', 'old', 0, max_budget_usd, started_at, observed_at, ?, 'review', 'any', 'beef000' FROM contract_run ORDER BY id LIMIT 1`, task2)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		now := time.Now()
		r, err := insertRun(context.Background(), tx, cidInt, insertRunInput{Kind: runKindContract, ChallengeVersion: 1, TaskID: &task2,
			SessionID: "44444444-4444-4444-8444-444444444444", PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketImpl})
		if err != nil {
			return err
		}
		n, _ := parseRunID(r.ID)
		if _, err := tx.Exec(`UPDATE run SET result = 'succeeded', ended_at = ? WHERE id = ?`, formatTimestamp(now), n); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO run_artifact (run_id, kind, ref) VALUES (?, 'commit', 'beef000'), (?, 'branch', 'feat/integration-1')`, n, n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-97・AC-98: 統合先を使う着手中の課題の検証中のタスク（検証した中身が無い）は対象になり、J5 の run の task_id はそのタスクである。
func TestJ5TaskTarget_VerifyingTaskWithoutVerifiedRefIsTargetedAndRunRecordsTheTask(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.setVerdict("met")
	res, err := f.verifyTasks(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != f.cid {
		t.Fatalf("items = %+v, want one item for %s", res.Items, f.cid)
	}
	task1 := f.count(t, `SELECT id FROM task`)
	ids := f.j5RunTaskIDs(t)
	if len(ids) != 1 || !ids[0].Valid || ids[0].Int64 != task1 {
		t.Fatalf("J5 run task_ids = %v, want [%d]", ids, task1)
	}
}

// AC-100〜AC-103・AC-143: 満たした → verified_ref に中身の識別、タスクは verifying・課題は in_progress のまま、承認待ちは増えず、
// 課題の実物の記録は書かれない。
func TestJ5TaskMet_RecordsVerifiedRefAndChangesNothingElse(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.setVerdict("met")
	before := f.count(t, `SELECT version FROM challenge`)
	ops := f.openOperations(t)
	approvals := f.count(t, `SELECT COUNT(*) FROM approval`)
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.verifiedRefs(t); !reflect.DeepEqual(got, []string{"c0ffee1"}) { // AC-100
		t.Fatalf("verified_ref = %v, want [c0ffee1]", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 1 { // AC-101
		t.Fatalf("verifying tasks = %d, want 1", n)
	}
	if got := f.status(t, f.cid); got != StatusInProgress { // AC-102
		t.Fatalf("status = %s, want in_progress", got)
	}
	if got := f.count(t, `SELECT version FROM challenge`); got != before {
		t.Fatalf("challenge version %d → %d, want unchanged", before, got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM approval`); n != approvals { // AC-103（承認の求めを増やさない）
		t.Fatalf("approvals %d → %d, want unchanged", approvals, n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 0 {
		t.Fatalf("holds = %d, want 0", n)
	}
	if got := f.openOperations(t); got != ops {
		t.Fatalf("operations %d → %d, want unchanged（反映の操作を作らない）", ops, got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM challenge WHERE actual IS NOT NULL`); n != 0 { // AC-143
		t.Fatalf("challenge.actual was written")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'task' AND action = '`+ActivityActionTaskVerify+`'`); n != 1 {
		t.Fatalf("task_verify activities = %d, want 1（作業ログにタスクの検証の結果を残す）", n)
	}
}

// AC-99・AC-126: 検証した中身が中身の識別と同じタスクは対象にならない。verified_ref が NULL に戻ったタスクは次の J5 の対象になる。
func TestJ5TaskTarget_SameVerifiedRefIsNotTargetedAndNullAgainIs(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.setVerdict("met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	res, err := f.verifyTasks(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || len(f.j5.j5Runs(t)) != 1 { // AC-99
		t.Fatalf("items = %v, J5 runs = %d, want no second verification", res.Items, len(f.j5.j5Runs(t)))
	}
	f.sql(t, `UPDATE contract_run SET verified_ref = NULL`)
	if res, err = f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || len(f.j5.j5Runs(t)) != 2 { // AC-126
		t.Fatalf("items = %v, J5 runs = %d, want the task verified again", res.Items, len(f.j5.j5Runs(t)))
	}
	// 中身の識別が変われば（検証した中身と違えば）、また対象になる。
	f.sql(t, `UPDATE contract_run SET content_ref = 'newref1'`)
	if res, err = f.verifyTasks(t, nil); err != nil || len(res.Items) != 1 {
		t.Fatalf("items = %v err = %v, want a changed content verified again", res, err)
	}
	if got := f.verifiedRefs(t); !reflect.DeepEqual(got, []string{"newref1"}) {
		t.Fatalf("verified_ref = %v, want [newref1]", got)
	}
}

// AC-25: 保留中の課題の verifying のタスクは J5 の対象にならない。
func TestJ5TaskTarget_HeldChallengesTaskIsNotTargeted(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.sql(t, `UPDATE challenge SET status = 'awaiting_human'`)
	f.setVerdict("met")
	res, err := f.verifyTasks(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || len(f.j5.j5Runs(t)) != 0 {
		t.Fatalf("items = %v, J5 runs = %d, want none for a held challenge", res.Items, len(f.j5.j5Runs(t)))
	}
}

// 統合先を使わない課題の着手中の検証中のタスクは、タスクの検証の対象にしない（S3a のまま）。
func TestJ5TaskTarget_NonIntegrationChallengeIsNotTargeted(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.sql(t, `UPDATE challenge SET uses_integration = 0`)
	res, err := f.verifyTasks(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || len(f.j5.j5Runs(t)) != 0 {
		t.Fatalf("items = %v, J5 runs = %d, want none", res.Items, len(f.j5.j5Runs(t)))
	}
}

// AC-140: 同じ課題の 2 個のタスクが verifying のとき、両方が対象になる（run.task_id は 1 個ずつ）。
func TestJ5TaskTarget_TwoVerifyingTasksAreBothTargeted(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.addSecondVerifyingTask(t, "")
	f.setVerdict("met")
	res, err := f.verifyTasks(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	ids := f.j5RunTaskIDs(t)
	if len(ids) != 2 || !ids[0].Valid || !ids[1].Valid || ids[0].Int64 == ids[1].Int64 {
		t.Fatalf("J5 run task_ids = %v, want two different tasks", ids)
	}
	if got := f.verifiedRefs(t); !reflect.DeepEqual(got, []string{"c0ffee1", "beef000"}) {
		t.Fatalf("verified_ref = %v", got)
	}
}

// AC-75: 成果物つきの 2 個のタスクのとき、1 個のタスクの検証の入力の成果物は、そのタスクの最後の委譲の実行のものである。
// AC-73: 子に対応するタスクの検証の入力に、その子の本文がある（取り込みのときに記録した値。ほかのタスクの入力には入らない）。
func TestJ5TaskInput_ArtifactsAndChildBodyBelongToTheTask(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.sql(t, `INSERT INTO source_child (challenge_id, external_key, source_id, url, title, body, upstream_state, created_at, updated_at)
		VALUES (1, 'o/r#9', 'src', 'https://github.com/o/r/issues/9', 'CHILD-TITLE', 'CHILD-BODY-完了条件', 'open', '2026-09-26T00:00:00.000Z', '2026-09-26T00:00:00.000Z')`)
	f.addSecondVerifyingTask(t, "o/r#9")
	f.setVerdict("met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.j5.j5Inputs) != 2 {
		t.Fatalf("J5 inputs = %d, want 2", len(f.j5.j5Inputs))
	}
	text := func(in JudgmentLaunchInput) string {
		var b strings.Builder
		for _, s := range in.Sections {
			b.WriteString("## " + s.Label + "\n" + s.Content + "\n")
		}
		return b.String()
	}
	first, second := text(f.j5.j5Inputs[0]), text(f.j5.j5Inputs[1])
	if !strings.Contains(first, "c0ffee1") || strings.Contains(first, "beef000") {
		t.Fatalf("the first task's input must hold only its own artifacts:\n%s", first)
	}
	if !strings.Contains(second, "beef000") || strings.Contains(second, "c0ffee1") {
		t.Fatalf("the second task's input must hold only its own artifacts:\n%s", second)
	}
	for _, want := range []string{"## " + labelTaskSourceSection + "\n", "CHILD-TITLE", "https://github.com/o/r/issues/9", "CHILD-BODY-完了条件", "SECOND-DONE"} {
		if !strings.Contains(second, want) {
			t.Fatalf("the second task's input lacks %q:\n%s", want, second)
		}
	}
	if strings.Contains(first, "CHILD-BODY") || strings.Contains(first, labelTaskSourceSection) {
		t.Fatalf("the first task has no source child, but its input has the section:\n%s", first)
	}
	// 上流は読み直さない（記録した値で確かめる）。
	if threads, refs := f.upstream.calls(); len(threads)+len(refs) != 0 {
		t.Fatalf("upstream reads = %v %v, want none", threads, refs)
	}
}

// AC-104〜AC-106: 満たしていない → タスクは in_progress。その後の状態の確認で、そのタスクの契約の実行へ respond（{note} に指摘）が
// 返り、同じ課題のほかのタスクの契約の実行へは resume が呼ばれない。
func TestJ5TaskNotMet_TaskBackToInProgressAndOnlyThatRunGetsRespond(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.addSecondVerifyingTask(t, "")
	f.sql(t, `UPDATE contract_run SET verified_ref = content_ref WHERE external_run_id = 'ext-2'`) // 2 個目は検証済み（対象外）
	f.setVerdict("not_met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.text(t, `SELECT status FROM task WHERE key IS NULL OR key <> 'second' ORDER BY id LIMIT 1`); got != string(TaskStatusInProgress) { // AC-104
		t.Fatalf("task status = %q, want in_progress", got)
	}
	if got := f.text(t, `SELECT status FROM task WHERE key = 'second'`); got != string(TaskStatusVerifying) {
		t.Fatalf("the other task = %q, want verifying", got)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress", got)
	}
	if got := f.verifiedRefs(t); got[0] != "<null>" {
		t.Fatalf("verified_ref = %v, want none for the not-met task", got)
	}
	f.client.echoRunning = true
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t)
	want := []string{"tool", "resume", "ext-1", "--choice", "respond", "--note", "FEEDBACK-TEXT"}
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], want) { // AC-105・AC-106
		t.Fatalf("resumes = %v, want exactly [%v]", f.client.resumes, want)
	}
	// 同じ差し戻しを二重に返さない。
	f.poll(t)
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1", len(f.client.resumes))
	}
}

// AC-107: 判断できない → 課題は awaiting_human、保留の task_id はそのタスク。verified_ref は書かれない。
func TestJ5TaskUncertain_HoldsTheChallengeWithTheTaskOnTheHold(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.setVerdict("uncertain")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	task1 := f.count(t, `SELECT id FROM task`)
	if got := f.count(t, `SELECT task_id FROM hold`); got != task1 {
		t.Fatalf("hold.task_id = %d, want %d", got, task1)
	}
	if qs := f.holdQuestions(t); len(qs) != 1 || qs[0] != "QUESTION-TEXT" {
		t.Fatalf("hold questions = %v", qs)
	}
	if got := f.verifiedRefs(t); got[0] != "<null>" {
		t.Fatalf("verified_ref = %v, want none", got)
	}
	if got := f.text(t, `SELECT status FROM task`); got != string(TaskStatusVerifying) {
		t.Fatalf("task = %q, want verifying", got)
	}
	wantLastHoldStop(t, f.s, "verify_uncertain/pending") // AC-2
}

// AC-141・AC-142: 結果を書く直前に、課題が保留になっていた・契約の実行の中身の識別が違っていたら、何も書かない（次の J5 がやり直す）。
func TestJ5TaskResult_IsDiscardedWhenTheTargetChangedDuringTheRun(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]string{
		"challenge held (AC-141)":           `UPDATE challenge SET status = 'awaiting_human'`,
		"content_ref changed (AC-142)":      `UPDATE contract_run SET content_ref = 'changed9'`,
		"task no longer verifying":          `UPDATE task SET status = 'in_progress'`,
		"challenge withdrawn from progress": `UPDATE challenge SET status = 'verifying'`,
	} {
		for _, verdict := range []string{"met", "not_met", "uncertain"} {
			t.Run(name+"/"+verdict, func(t *testing.T) {
				t.Parallel()
				f := newTaskVerifyFixture(t)
				f.setVerdict(verdict)
				f.j5.j5.onInvoke = func(_ JudgmentLaunchInput) { f.sql(t, mutate) }
				holds, taskStatus := f.count(t, `SELECT COUNT(*) FROM hold`), f.text(t, `SELECT status FROM task`)
				chStatus := f.text(t, `SELECT status FROM challenge`)
				res, err := f.verifyTasks(t, nil)
				if err != nil {
					t.Fatal(err)
				}
				if got := f.verifiedRefs(t); got[0] != "<null>" {
					t.Fatalf("verified_ref = %v, want nothing written", got)
				}
				if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != holds {
					t.Fatalf("holds %d → %d, want unchanged", holds, n)
				}
				wantTask := taskStatus
				if mutate == `UPDATE task SET status = 'in_progress'` {
					wantTask = string(TaskStatusInProgress)
				}
				if got := f.text(t, `SELECT status FROM task`); got != wantTask {
					t.Fatalf("task status = %q, want %q（結果を捨てる）", got, wantTask)
				}
				wantCh := chStatus
				switch mutate {
				case `UPDATE challenge SET status = 'awaiting_human'`:
					wantCh = string(StatusAwaitingHuman)
				case `UPDATE challenge SET status = 'verifying'`:
					wantCh = string(StatusVerifying)
				}
				if got := f.text(t, `SELECT status FROM challenge`); got != wantCh {
					t.Fatalf("challenge status = %q, want %q", got, wantCh)
				}
				if len(res.Items) != 1 || res.Items[0].Status != nil {
					t.Fatalf("items = %+v, want one item without a written status", res.Items)
				}
			})
		}
	}
}

// 結果を捨てた後は、次の J5 がやり直す（verified_ref が無いまま、中身の識別が同じなら再び対象になる）。
func TestJ5TaskResult_DiscardedResultIsRedoneByTheNextJ5(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.setVerdict("met")
	f.j5.j5.onInvoke = func(_ JudgmentLaunchInput) { f.sql(t, `UPDATE contract_run SET content_ref = 'changed9'`) }
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	f.j5.j5.onInvoke = nil
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.verifiedRefs(t); !reflect.DeepEqual(got, []string{"changed9"}) {
		t.Fatalf("verified_ref = %v, want [changed9]", got)
	}
}

// ID を指定した操作: 着手中の統合先の課題は検証中のタスクを検証する。対象が無ければ ErrInvalidTransition。
func TestJ5TaskTarget_ExplicitChallengeIDVerifiesItsTasks(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.setVerdict("met")
	res, err := f.verifyTasks(t, &f.cid)
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("items = %v err = %v, want one", res, err)
	}
	if _, err := f.verifyTasks(t, &f.cid); err == nil || !strings.Contains(err.Error(), ErrInvalidTransition.Error()) {
		t.Fatalf("err = %v, want ErrInvalidTransition when nothing is left to verify", err)
	}
}

// AC-226: 統合先を使わない課題の J5 は S3a のまま（満たした → タスクは done、課題は awaiting_completion_approval）。
func TestJ5Task_AC226_NonIntegrationChallengeKeepsS3aBehavior(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	f.j5.result = j5Out("met", nil, nil)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval", got)
	}
	if got := taskStatusOf(t, f.s, "C-1", "t1"); got != TaskStatusDone {
		t.Fatalf("task = %s, want done", got)
	}
}

// openRunFor は、タスク taskID（nil なら記録の無い実行）の、終わっていない契約のコマンドの実行を 1 本足す。
func (f *reviewFlowFixture) openRunFor(t *testing.T, taskID *int64) {
	t.Helper()
	cidInt, _ := parseChallengeID(f.cid)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		now := time.Now()
		_, err := insertRun(context.Background(), tx, cidInt, insertRunInput{Kind: runKindContract, ChallengeVersion: 1, TaskID: taskID,
			SessionID: "55555555-5555-4555-8555-555555555555", PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketImpl})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// 終わっていない実行（同じタスクの契約のコマンド・タスクの記録の無い実行）があるあいだ、そのタスクは対象にしない。
// 同じ課題のほかのタスクの終わっていない実行は、このタスクの対象を妨げない。
func TestJ5TaskTarget_UnfinishedRunKeepsTheTaskOutOfTargets(t *testing.T) {
	t.Parallel()
	for name, taskScoped := range map[string]bool{"same task": true, "run without a task": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newTaskVerifyFixture(t)
			f.setVerdict("met")
			var tid *int64
			if taskScoped {
				v := f.count(t, `SELECT id FROM task`)
				tid = &v
			}
			f.openRunFor(t, tid)
			res, err := f.verifyTasks(t, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Items) != 0 || len(f.j5.j5Runs(t)) != 0 {
				t.Fatalf("items = %v, J5 runs = %d, want none while a run is unfinished", res.Items, len(f.j5.j5Runs(t)))
			}
		})
	}
	t.Run("another task's run does not block", func(t *testing.T) {
		t.Parallel()
		f := newTaskVerifyFixture(t)
		f.addSecondVerifyingTask(t, "")
		f.setVerdict("met")
		other := f.count(t, `SELECT id FROM task WHERE key = 'second'`)
		f.openRunFor(t, &other)
		res, err := f.verifyTasks(t, nil)
		if err != nil || len(res.Items) != 1 {
			t.Fatalf("items = %v err = %v, want only the first task verified", res, err)
		}
		if ids := f.j5RunTaskIDs(t); len(ids) != 1 || ids[0].Int64 == other {
			t.Fatalf("J5 run task_ids = %v, want the task without an unfinished run", ids)
		}
	})
}

// 検証中でないタスク（着手中）は、契約の実行があっても対象にしない。
func TestJ5TaskTarget_TaskNotVerifyingIsNotTargeted(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.sql(t, `UPDATE task SET status = 'in_progress'`)
	f.setVerdict("met")
	res, err := f.verifyTasks(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || len(f.j5.j5Runs(t)) != 0 {
		t.Fatalf("items = %v, J5 runs = %d, want none", res.Items, len(f.j5.j5Runs(t)))
	}
}

// 書く直前に、タスクの最後の契約の実行が別の行になっていたら（中身の識別が同じでも）、結果を捨てる。
func TestJ5TaskResult_IsDiscardedWhenTheLatestContractRunIsAnotherRow(t *testing.T) {
	t.Parallel()
	for _, verdict := range []string{"met", "not_met", "uncertain"} {
		t.Run(verdict, func(t *testing.T) {
			t.Parallel()
			f := newTaskVerifyFixture(t)
			f.setVerdict(verdict)
			f.j5.j5.onInvoke = func(_ JudgmentLaunchInput) {
				f.sql(t, `UPDATE contract_run SET ended_at = observed_at`) // 終わっていない実行は 1 本だけ（一意の索引）
				f.sql(t, `INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd,
					started_at, observed_at, task_id, requested_kind, requested_decider, content_ref, kind)
					SELECT challenge_id, connector_id, 'ext-new', start_run_id, 'waiting', 'new', 0, max_budget_usd, started_at, observed_at, task_id, requested_kind, requested_decider, content_ref, kind
					FROM contract_run ORDER BY id DESC LIMIT 1`)
			}
			res, err := f.verifyTasks(t, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range f.verifiedRefs(t) {
				if v != "<null>" {
					t.Fatalf("verified_ref = %v, want nothing written", f.verifiedRefs(t))
				}
			}
			if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 0 {
				t.Fatalf("holds = %d, want 0", n)
			}
			if len(res.Items) != 1 || res.Items[0].Status != nil {
				t.Fatalf("items = %+v, want one item without a written status", res.Items)
			}
		})
	}
}

// addLaterTaskRun は、最初のタスクに、いまの最後の run より後の契約の run（終わり済み）を足す。ref が空でなければ、その commit を成果物に持つ。
func (f *reviewFlowFixture) addLaterTaskRun(t *testing.T, ref string) {
	t.Helper()
	cidInt, _ := parseChallengeID(f.cid)
	task1 := f.count(t, `SELECT id FROM task ORDER BY id LIMIT 1`)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		now := time.Now()
		r, err := insertRun(context.Background(), tx, cidInt, insertRunInput{Kind: runKindContract, ChallengeVersion: 1, TaskID: &task1,
			SessionID: "55555555-5555-4555-8555-555555555555", PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketImpl})
		if err != nil {
			return err
		}
		n, _ := parseRunID(r.ID)
		if _, err := tx.Exec(`UPDATE run SET result = 'succeeded', ended_at = ? WHERE id = ?`, formatTimestamp(now), n); err != nil {
			return err
		}
		if ref != "" {
			_, err = tx.Exec(`INSERT INTO run_artifact (run_id, kind, ref) VALUES (?, 'commit', ?)`, n, ref)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func j5InputText(in JudgmentLaunchInput) string {
	var b strings.Builder
	for _, s := range in.Sections {
		b.WriteString("## " + s.Label + "\n" + s.Content + "\n")
	}
	return b.String()
}

// 同じタスクに成果物を持つ実行が 2 回あるとき、タスクの検証の入力の成果物は後の実行のもの（前の実行の成果物は入らない）。
func TestJ5TaskInput_TwoRunsWithArtifactsTheLaterRunWins(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.addLaterTaskRun(t, "dddd111")
	f.addSecondVerifyingTask(t, "")                                                                // ほかのタスクの成果物（beef000。より後の run）は入らない
	f.sql(t, `UPDATE contract_run SET verified_ref = content_ref WHERE external_run_id = 'ext-2'`) // 2 個目は検証済み（対象外）
	f.setVerdict("met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.j5.j5Inputs) != 1 {
		t.Fatalf("J5 inputs = %d, want 1", len(f.j5.j5Inputs))
	}
	text := j5InputText(f.j5.j5Inputs[0])
	if !strings.Contains(text, "dddd111") || strings.Contains(text, "c0ffee1") || strings.Contains(text, "beef000") {
		t.Fatalf("the input must hold only the later run's artifacts (dddd111):\n%s", text)
	}
}

// タスクの検証の入力の成果物は、成果物を持つ最後の実行のもの（run_artifact を持つ実行 → ID の大きい実行の順で選ぶ）。
// 成果物を持たない実行が最後にあっても、その前の、成果物のある実行の成果物が入る。
func TestJ5TaskInput_LaterRunWithoutArtifactsStillGetsTheEarlierRunsArtifacts(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.addLaterTaskRun(t, "")
	f.setVerdict("met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.j5.j5Inputs) != 1 {
		t.Fatalf("J5 inputs = %d, want 1", len(f.j5.j5Inputs))
	}
	if text := j5InputText(f.j5.j5Inputs[0]); !strings.Contains(text, "c0ffee1") {
		t.Fatalf("current behaviour: the run with artifacts is preferred over the later run without them (c0ffee1 expected):\n%s", text)
	}
}
