package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 仕上げ 3 項目 1: 終わりを記録済みで開けたまま残した契約の実行（記録した状態は終わり・ended_at は NULL）の扱い。

func (f *igLandFixture) activeCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		n, err = activeDelegationCount(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

var failedStatusRead = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "exit 1"}

// (a) 記録した状態が終わりの行は、同時に走らせる数に数えない。
func TestFinish3_OpenRunWithEndRecordedIsNotCountedAsActive(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.heldWithTaskRun(t, "in_progress")
	f.succeed(t)
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) != "" {
		t.Fatal("precondition: the run must stay open while held")
	}
	if got := f.activeCount(t); got != 0 {
		t.Fatalf("active delegations = %d, want 0 (the recorded state is an end)", got)
	}
}

// (b) 記録した状態が終わりの行は、委譲先の状態の確認を呼ばない。確認が失敗しても、記録から保留が立つ。
func TestFinish3_OpenSucceededRunIsHandledFromTheRecordWithoutCallingStatus(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.heldWithTaskRun(t, "in_progress")
	f.succeed(t)
	f.answer(t, "続けてください")
	before := len(f.client.statuses)
	f.read(t, failedStatusRead)
	if got := len(f.client.statuses); got != before {
		t.Fatalf("status was called %d times for a row whose recorded state is an end", got-before)
	}
	if got := f.lastHoldReason(t); got != string(holdReasonMergedUnverified) || f.count(t, `SELECT COUNT(*) FROM hold`) != 2 {
		t.Fatalf("reason = %q, holds = %d, want merged_unverified raised from the record", got, f.count(t, `SELECT COUNT(*) FROM hold`))
	}
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) == "" {
		t.Fatal("the run is still open after the hold")
	}
}

// (b) 上げる依頼の終わりも同じ（回答の後、状態の確認の失敗に関わらず保留が立つ）は reflect_up_test 側の試験が、記録から読むことで通る。
// (c) 課題の取り下げは、記録した状態が終わりの行に中止を呼ばない（中止がエラーを返しても通る）。
func TestFinish3_WithdrawDoesNotCancelARunWhoseRecordedStateIsAnEnd(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.heldWithTaskRun(t, "in_progress")
	f.succeed(t)
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "exit 1"}
	n, err := f.s.CancelOpenContractRuns(context.Background(), ContractCancelInput{ChallengeID: f.cid, AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client})
	if err != nil || n != 1 {
		t.Fatalf("CancelOpenContractRuns = %d, %v, want 1, nil", n, err)
	}
	if len(f.client.cancels) != 0 {
		t.Fatalf("cancel was called %d times", len(f.client.cancels))
	}
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) == "" {
		t.Fatal("the run is still open")
	}
}

// 項目 2: 「入れてよい」を返し済みの実行が failed・cancelled で終わったときも、統合先へ入ったかを確かめる。
func TestFinish3_StoppedAfterLandChecksWhetherTheContentLanded(t *testing.T) {
	t.Parallel()
	for _, state := range []ContractState{ContractStateFailed, ContractStateCancelled} {
		state := state
		stop := func(f *igLandFixture) {
			f.read(t, contractOut(state, nil, func(st *ContractStatus) { st.Artifacts = landedArts }))
		}
		t.Run(string(state)+"/same content completes the task", func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			f.landed(t)
			stop(f)
			if f.taskStatus(t) != "done" || f.count(t, `SELECT COUNT(*) FROM hold`) != 0 || f.status(t, f.cid) != StatusInProgress {
				t.Fatalf("task = %s, holds = %d, status = %s", f.taskStatus(t), f.count(t, `SELECT COUNT(*) FROM hold`), f.status(t, f.cid))
			}
			if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) == "" {
				t.Fatal("the run is still open")
			}
		})
		t.Run(string(state)+"/other content holds merged_unverified", func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			f.landed(t)
			f.checks.prs["o/r#7"] = UpstreamPullRequestChecks{URL: landPRURL, State: "merged", Base: integBranch, HeadSHA: "other"}
			stop(f)
			if got := f.lastHoldReason(t); got != string(holdReasonMergedUnverified) {
				t.Fatalf("reason = %q", got)
			}
			if f.taskStatus(t) == "done" || f.taskStatus(t) == "in_progress" {
				t.Fatalf("task = %s, want it left for the answer", f.taskStatus(t))
			}
			// 問いは、失敗・中止で終わったことを言う（成功の問いを流用しない）。
			if q := f.text(t, `SELECT question FROM hold ORDER BY id DESC LIMIT 1`); !strings.Contains(q, contractStateWord(state)) || strings.Contains(q, "（成功）") {
				t.Fatalf("question = %q", q)
			}
		})
		t.Run(string(state)+"/not landed keeps the current failure handling in one write", func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			f.landed(t)
			f.setPR(7, landPRURL, "open", integBranch)
			stop(f)
			if f.count(t, `SELECT COUNT(*) FROM hold`) != 1 || f.lastHoldReason(t) == string(holdReasonMergedUnverified) {
				t.Fatalf("holds = %d, reason = %q, want the current stop hold", f.count(t, `SELECT COUNT(*) FROM hold`), f.lastHoldReason(t))
			}
			if f.taskStatus(t) != "in_progress" || f.ref(t, "land_ref") != "" {
				t.Fatalf("task = %s, land_ref = %q, want the task reopened and the land content cleared", f.taskStatus(t), f.ref(t, "land_ref"))
			}
			if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) == "" {
				t.Fatal("the run is still open")
			}
			// 保留と着手中への戻しは同じ書き込み: 保留の書き込みを失敗させると、タスクも通す中身も変わらない。
		})
		t.Run(string(state)+"/unreadable writes nothing and retries from the record", func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			f.landed(t)
			f.checks.err = errors.New("boom")
			stop(f)
			if f.taskStatus(t) != "verifying" || f.ref(t, "land_ref") == "" || f.count(t, `SELECT COUNT(*) FROM hold`) != 0 ||
				f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) != "" {
				t.Fatalf("task = %s, land_ref = %q: something was decided although upstream was unreadable", f.taskStatus(t), f.ref(t, "land_ref"))
			}
			if f.activeCount(t) != 0 {
				t.Fatal("the open run with an end recorded is counted")
			}
			f.checks.err = nil
			f.read(t, failedStatusRead) // 次の確認は記録から進む（委譲先の失敗に関わらない）
			if f.taskStatus(t) != "done" {
				t.Fatalf("task = %s, want done after the retry", f.taskStatus(t))
			}
		})
	}
}

// 項目 2: 保留と、検証中のタスクの着手中への戻し・通す中身の無効化は、同じ書き込みで行う（保留の書き込みが失敗したら、どれも変わらない）。
func TestFinish3_StoppedNotLandedIsOneWrite(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.setPR(7, landPRURL, "open", integBranch)
	f.sql(t, `CREATE TRIGGER fail_hold BEFORE INSERT ON hold BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateCancelled, nil, func(st *ContractStatus) { st.Artifacts = landedArts })}
	_, _ = f.s.PollContractRuns(context.Background(), ContractPollInput{
		AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client, CycleID: f.cycle(t, 300), Reconcile: f.branches, Checks: f.checks, Compare: f.compare,
	})
	if f.taskStatus(t) != "verifying" || f.ref(t, "land_ref") == "" || f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) != "" {
		t.Fatalf("task = %s, land_ref = %q: a partial write remains", f.taskStatus(t), f.ref(t, "land_ref"))
	}
	f.sql(t, `DROP TRIGGER fail_hold`)
	f.read(t, failedStatusRead)
	if f.count(t, `SELECT COUNT(*) FROM hold`) != 1 || f.taskStatus(t) != "in_progress" || f.ref(t, "land_ref") != "" {
		t.Fatalf("holds = %d, task = %s, land_ref = %q", f.count(t, `SELECT COUNT(*) FROM hold`), f.taskStatus(t), f.ref(t, "land_ref"))
	}
}

// (b) 保留中に終わりを読んで開けたまま残した上げる依頼は、回答の後、委譲先の状態の確認が失敗しても、記録から reflect_failed の保留が立つ。
func TestFinish3_UpRequestEndedWhileHeldIsHandledFromTheRecordWhenStatusFails(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.startUp(t)
	f.sqlExec(t, `UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, mustChallengeInt(t, id))
	f.readWith(t, contractOut(ContractStateFailed, nil, nil))
	f.sqlExec(t, `UPDATE challenge SET status = 'in_progress' WHERE id = ?`, mustChallengeInt(t, id))
	f.readWith(t, failedStatusRead)
	if got := f.lastReason(t, id); got != string(holdReasonReflectFailed) {
		t.Fatalf("reason = %q, want reflect_failed", got)
	}
	if _, _, _, ended := f.reflectRow(t); !ended {
		t.Fatal("the up request run is still open")
	}
}

// (c) 計画の作り直しの後の取り消し（上げる依頼）も、記録した状態が終わりの行には中止を呼ばない。
func TestFinish3_ObsoleteUpRequestWithEndRecordedIsNotCancelled(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.startUp(t)
	f.sqlExec(t, `UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, mustChallengeInt(t, id))
	f.readWith(t, contractOut(ContractStateFailed, nil, nil))
	f.sqlExec(t, `UPDATE task SET status = 'in_progress' WHERE id = (SELECT MIN(id) FROM task)`) // 終わっていないタスクがある（作り直した計画の新しいタスク）
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "exit 1"}
	before := len(f.client.cancels)
	f.readWith(t, failedStatusRead)
	if len(f.client.cancels) != before {
		t.Fatalf("cancel was called for a run whose recorded state is an end")
	}
	if _, _, _, ended := f.reflectRow(t); !ended {
		t.Fatal("the up request run is still open")
	}
}

// lastHoldQuestionOf は課題 cid の最後の保留の問い。
func (f *contractFixture) lastHoldQuestionOf(t *testing.T, cid int64) string {
	t.Helper()
	var q string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT question FROM hold WHERE challenge_id = ? ORDER BY id DESC LIMIT 1`, cid).Scan(&q)
	})
	return q
}

// レビューの指摘: 「入れてよい」を返し済みの実行が失敗で終わり、同じ中身が入っていてタスクを済みにしたあと、保留にし損ねた停止の掃引が、
// 済みのタスクの停止を保留にしない。
func TestFinish3_SweepDoesNotHoldAStopWhoseTaskLandedAndIsDone(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.read(t, contractOut(ContractStateFailed, nil, func(st *ContractStatus) { st.Artifacts = landedArts }))
	if f.taskStatus(t) != "done" {
		t.Fatalf("task = %s", f.taskStatus(t))
	}
	n, err := f.s.HoldUnrecordedContractStops(context.Background())
	if err != nil || n != 0 || f.count(t, `SELECT COUNT(*) FROM hold`) != 0 {
		t.Fatalf("sweep held %d (%v), holds = %d, want none", n, err, f.count(t, `SELECT COUNT(*) FROM hold`))
	}
}

// レビューの指摘: 保留と同じトランザクションで終わった記録にしたとき、終わりの時刻が保留の時刻より後にならない（後になると、回答の後に二重の保留が立つ）。
func TestFinish3_EndedAtIsNotLaterThanTheHoldRaisedInTheSameWrite(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	// 呼ぶたびに進む時計。保留の時刻と終わりの時刻を別々に取ると、終わりの時刻が必ず後になる（そろえる処理を外すと必ず落ちる）。
	var ticks int64
	base := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	f.s.now = func() time.Time { return base.Add(time.Duration(atomic.AddInt64(&ticks, 1)) * time.Second) }
	f.setPR(7, landPRURL, "open", integBranch)
	f.read(t, contractOut(ContractStateFailed, nil, func(st *ContractStatus) { st.Artifacts = landedArts }))
	if f.count(t, `SELECT COUNT(*) FROM hold`) == 0 || f.count(t, `SELECT COUNT(*) FROM contract_run WHERE ended_at IS NULL`) != 0 {
		t.Fatal("precondition: a hold was raised and the run is ended")
	}
	if f.count(t, `SELECT COUNT(*) FROM contract_run WHERE ended_at = (SELECT MAX(raised_at) FROM hold)`) != 1 {
		t.Fatalf("ended_at = %q, hold raised_at = %q, want equal", f.text(t, `SELECT ended_at FROM contract_run`), f.text(t, `SELECT MAX(raised_at) FROM hold`))
	}
}

// 項目 2: 課題が保留の間に読んだ、「入れてよい」を返し済みの実行の失敗は、行を開けたまま残す。そのあと別の保留への回答で課題が着手中へ戻っても、
// 開けたままの失敗の行の通す中身は消さない（消すと、次の状態の確認が、統合先へ入ったかを確かめられない）。
func TestFinish3_AnswerKeepsTheLandRefOfAnOpenStoppedRun(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.sql(t, `INSERT INTO hold (challenge_id, question, reason, from_status, raised_at, task_id) VALUES (?, 'q', 'checks_failed', 'in_progress', '2026-11-20T00:00:00.000Z',
		(SELECT task_id FROM contract_run))`, f.intID(t))
	f.sql(t, `UPDATE challenge SET status = 'awaiting_human', version = version + 1`)
	f.checks.err = errors.New("boom") // 統合先の読みが失敗するので、行は開けたまま残る
	f.read(t, contractOut(ContractStateFailed, nil, func(st *ContractStatus) { st.Artifacts = landedArts }))
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) != "" || f.ref(t, "land_ref") == "" {
		t.Fatalf("ended = %q, land_ref = %q, state = %s, holds = %d", f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`), f.ref(t, "land_ref"), f.ref(t, "state"), f.count(t, `SELECT COUNT(*) FROM hold`))
	}
	f.answer(t, "確かめた")
	if f.ref(t, "land_ref") == "" {
		t.Fatal("the answer cleared the land content of the open stopped run")
	}
}
