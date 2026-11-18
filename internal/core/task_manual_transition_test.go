package core

// このファイルは、手で課題を進める口（`flywheel submit`＝SubmitChallenge、`flywheel verify --result`＝VerifyChallenge）が、
// タスクが 1 個の課題で、委譲の結果・完了の検証の判断点（J5）と同じようにタスクの状態を進めることと、契約の形の委譲の
// コマンド 1 回ごとの実行がタスクに結び付くことを、実ストアで検証する
// （区切り S2a。docs/features/core-redesign.md の受入基準 AC-192〜AC-201）。

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
)

// inProgressTaskFixture は、タスク t1 が 1 個（pending）ある着手中の課題 C-1 の delegateFixture。
func inProgressTaskFixture(t *testing.T) (*delegateFixture, string) {
	t.Helper()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusPending {
		t.Fatalf("task t1 = %s, want pending", got)
	}
	return f, id
}

func submitByHand(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.SubmitChallenge(context.Background(), ChannelCLI, id); err != nil {
		t.Fatalf("SubmitChallenge(%s): %v", id, err)
	}
}

func verifyByHand(t *testing.T, s *Store, id, result string, question *string) {
	t.Helper()
	if _, err := s.VerifyChallenge(context.Background(), ChannelCLI, id, VerifyInput{Result: result, Question: question}); err != nil {
		t.Fatalf("VerifyChallenge(%s, %s): %v", id, result, err)
	}
}

// AC-192: タスクが 1 個（pending）の着手中の課題を手で検証中へ進めると、タスクの status が verifying になる。
func TestManualTask_AC192_SubmitMovesThePendingTaskToVerifying(t *testing.T) {
	t.Parallel()
	f, id := inProgressTaskFixture(t)
	submitByHand(t, f.s, id)
	if got := f.detail(t, id).Status; got != StatusVerifying {
		t.Fatalf("challenge status = %s, want verifying", got)
	}
	if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusVerifying {
		t.Errorf("task status = %s, want verifying", got)
	}
}

// AC-193: タスクが 1 個（in_progress）の着手中の課題を手で検証中へ進めると、タスクの status が verifying になる。
func TestManualTask_AC193_SubmitMovesTheInProgressTaskToVerifying(t *testing.T) {
	t.Parallel()
	f, id := inProgressTaskFixture(t)
	cid, _ := parseChallengeID(id)
	setTaskStatusRaw(t, f.s, cid, "t1", TaskStatusInProgress)
	submitByHand(t, f.s, id)
	if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusVerifying {
		t.Errorf("task status = %s, want verifying", got)
	}
	// 状態の変化は、課題の遷移と同じ操作の作業ログ（経路 cli。原因の run は無い）に 1 件だけ残る。
	rows := coretestRows(t, f.s, `SELECT channel, run_id FROM activity WHERE entity = 'task' AND action = 'task_status' AND json_extract(after, '$.status') = 'verifying'`)
	if len(rows) != 1 || rows[0][0] != string(ChannelCLI) || rows[0][1] != nil {
		t.Errorf("verifying task_status activities (channel, run_id) = %v, want exactly one by cli without a run", rows)
	}
}

// AC-194: 起動できるタスクが 2 個の着手中の課題を手で検証中へ進めても、どのタスクの status も変わらない。
func TestManualTask_AC194_SubmitLeavesTwoTasksUntouched(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P1", twoTaskPlanSpec())
	cid, _ := parseChallengeID(id)
	before := taskStatusMap(t, f.s, cid)
	if len(before) != 2 {
		t.Fatalf("tasks = %v, want two", before)
	}
	submitByHand(t, f.s, id)
	if got := f.detail(t, id).Status; got != StatusVerifying {
		t.Fatalf("challenge status = %s, want verifying", got)
	}
	assertTaskStatuses(t, f.s, cid, before)
}

// AC-195: 検証中のタスクが 1 個の検証中の課題に、手で「満たした」（met）を記録すると、タスクの status が done になる。
func TestManualTask_AC195_VerifyMetMovesTheTaskToDone(t *testing.T) {
	t.Parallel()
	f, id := inProgressTaskFixture(t)
	submitByHand(t, f.s, id)
	verifyByHand(t, f.s, id, "met", nil)
	if got := f.detail(t, id).Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("challenge status = %s, want awaiting_completion_approval", got)
	}
	if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusDone {
		t.Errorf("task status = %s, want done", got)
	}
}

// AC-196: 同じ課題に、手で「満たさない」（not_met）を記録すると、タスクの status が in_progress になる。
func TestManualTask_AC196_VerifyNotMetMovesTheTaskToInProgress(t *testing.T) {
	t.Parallel()
	f, id := inProgressTaskFixture(t)
	submitByHand(t, f.s, id)
	verifyByHand(t, f.s, id, "not_met", nil)
	if got := f.detail(t, id).Status; got != StatusInProgress {
		t.Fatalf("challenge status = %s, want in_progress", got)
	}
	if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusInProgress {
		t.Errorf("task status = %s, want in_progress", got)
	}
}

// AC-197: 手で「満たさない」を記録して着手中へ戻った課題に委譲の段を実行すると、そのタスクの委譲が起動する
// （起動できるタスクが無い〔no_tasks〕にならない）。
func TestManualTask_AC197_AfterManualNotMetTheTaskIsDelegatedAgain(t *testing.T) {
	t.Parallel()
	f, id := inProgressTaskFixture(t)
	submitByHand(t, f.s, id)
	verifyByHand(t, f.s, id, "not_met", nil)
	res := mustRunResult(t, f, nil)
	if len(res.NotStarted) != 0 || len(res.Items) != 1 {
		t.Fatalf("result = %+v, want one launch and no not_started", res)
	}
	runs := f.delegateRuns(t)
	if want := firstTaskID(t, f.s, id); len(runs) != 1 || runs[0].TaskID == nil || *runs[0].TaskID != want {
		t.Fatalf("delegate runs = %+v, want one run tied to the task %s", runs, want)
	}
}

// AC-198: 同じ課題に、手で「判断できない」（uncertain）を記録すると、作られた保留の task_id がそのタスクの ID である。
// AC-199: そのとき、タスクの status は verifying のままである。
func TestManualTask_AC198_AC199_VerifyUncertainTiesTheHoldAndKeepsTheTaskVerifying(t *testing.T) {
	t.Parallel()
	f, id := inProgressTaskFixture(t)
	cid, _ := parseChallengeID(id)
	submitByHand(t, f.s, id)
	q := "QUESTION"
	verifyByHand(t, f.s, id, "uncertain", &q)
	if got := f.detail(t, id).Status; got != StatusAwaitingHuman {
		t.Fatalf("challenge status = %s, want awaiting_human", got)
	}
	task := taskRows(t, f.s, cid)[0]
	want, _ := parseTaskID(task.ID)
	if got := f.queryString(t, `SELECT COALESCE(task_id, '') FROM hold WHERE challenge_id = ? ORDER BY id DESC LIMIT 1`, cid); got != strconv.FormatInt(want, 10) {
		t.Errorf("hold.task_id = %q, want %d (%s)", got, want, task.ID)
	}
	if task.Status != TaskStatusVerifying {
		t.Errorf("task status = %s, want verifying", task.Status)
	}
}

// 手で「満たした」を記録した後に完了を差し戻すと、タスクは in_progress に戻り、委譲を続けられる
// （完了の差し戻しが done のタスクを戻す規則〔AC-135〕と、手の記録の組み合わせ）。
func TestManualTask_RejectingAfterManualMetReopensTheTask(t *testing.T) {
	t.Parallel()
	f, id := inProgressTaskFixture(t)
	submitByHand(t, f.s, id)
	verifyByHand(t, f.s, id, "met", nil)
	rejectCompletionNow(t, f.s, id, "直す")
	if got := f.detail(t, id).Status; got != StatusInProgress {
		t.Fatalf("challenge status = %s, want in_progress", got)
	}
	if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusInProgress {
		t.Errorf("task status = %s, want in_progress", got)
	}
}

// AC-203・AC-204: 検証中のタスクが 2 個の課題では、手の記録はどのタスクも動かさず、保留もタスクに結び付けない
// （「ちょうど 1 個のときだけ」の規則。met・not_met・uncertain のそれぞれで確かめる）。
func TestManualTask_TwoVerifyingTasksAreLeftUntouchedAndTheHoldIsUntied(t *testing.T) {
	t.Parallel()
	for _, result := range []string{"met", "not_met", "uncertain"} {
		f := newDelegateFixture(t)
		id := f.newInProgress(t, "two", "P1", twoTaskPlanSpec())
		cid, _ := parseChallengeID(id)
		submitByHand(t, f.s, id)
		for key := range taskStatusMap(t, f.s, cid) {
			setTaskStatusRaw(t, f.s, cid, key, TaskStatusVerifying)
		}
		before := taskStatusMap(t, f.s, cid)
		if len(before) != 2 {
			t.Fatalf("tasks = %v, want two", before)
		}
		var q *string
		if result == "uncertain" {
			s := "QUESTION"
			q = &s
		}
		verifyByHand(t, f.s, id, result, q)
		assertTaskStatuses(t, f.s, cid, before)
		if got := f.queryString(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ? AND task_id IS NOT NULL`, cid); got != "0" {
			t.Errorf("%s: holds tied to a task = %s, want 0", result, got)
		}
		if result == "uncertain" {
			if got := f.queryString(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ?`, cid); got != "1" {
				t.Errorf("holds = %s, want the one made by the uncertain verdict", got)
			}
		}
	}
}

// AC-200・AC-201: 契約の形（form: cli）の委譲で、コマンド 1 回ごとの実行（run。kind = contract）の task_id がそのタスクの ID で、
// repo がタスクの対象リポジトリである。start の行と、その後の読み直し（status）の行の両方で確かめる。
func TestManualTask_AC200_AC201_ContractCommandRunsHaveTheTaskIDAndRepo(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	cid, _ := parseChallengeID(f.cid)
	task := taskRows(t, f.s, cid)[0]
	f.runDelegationStage(t, nil)
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "human", Text: "どうする"}, nil)}
	if reps := f.poll(t); len(reps) != 1 || !reps[0].Held {
		t.Fatalf("report = %+v, want held", reps)
	}
	type row struct{ task, repo sql.NullString }
	var rows []row
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rs, err := tx.Query(`SELECT 'T-' || task_id, repo FROM run WHERE kind = 'contract' ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.task, &r.repo); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return rs.Err()
	}); err != nil {
		t.Fatalf("read the contract command runs: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("contract command runs = %d, want the start and the status rows", len(rows))
	}
	for i, r := range rows {
		if !r.task.Valid || r.task.String != task.ID {
			t.Errorf("run[%d].task_id = %v, want %s", i, r.task, task.ID)
		}
		if !r.repo.Valid || r.repo.String != task.Repo {
			t.Errorf("run[%d].repo = %v, want %s", i, r.repo, task.Repo)
		}
	}
}

// coretestRows は q の結果の行を返す（列の値は、SQLite の型のまま。NULL は nil）。
func coretestRows(t *testing.T, s *Store, q string, args ...any) [][]any {
	t.Helper()
	var out [][]any
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rs, err := tx.Query(q, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		cols, err := rs.Columns()
		if err != nil {
			return err
		}
		for rs.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rs.Scan(ptrs...); err != nil {
				return err
			}
			out = append(out, vals)
		}
		return rs.Err()
	})
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return out
}
