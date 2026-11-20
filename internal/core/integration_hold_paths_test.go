package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// 項目 4: 同じ PR の反映の操作が、置き換え（superseded）と課題の差し戻し（challenge_rejected）の両方で閉じた後、上げる依頼のレビュー待ちを
// 読んだら、承認できる反映の操作を作り直す。作れなかったときは、反映の操作の無い検証中を作らない。
func TestStuckPaths_UpRequestRecreatesTheReleaseClosedBySupersededAndChallengeRejected(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	cid, _ := parseChallengeID(id)
	f.setUpPR("main")
	for _, reason := range []string{"superseded", "challenge_rejected"} {
		f.sqlExec(t, `INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at, closed_reason, closed_at)
			VALUES (?, 'release', 's', ?, 'closed', 2, '2026-01-01T00:00:00.000Z', ?, '2026-01-01T00:00:01.000Z')`, cid, upPRURL, reason)
	}
	f.startUp(t)
	f.readWith(t, upReviewOut([]string{"respond", "land"}, upArtsOK...))
	if got := f.statusOf(t, id); got == StatusVerifying {
		var open int
		readForTest(t, f.s, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COUNT(*) FROM operation WHERE kind = 'release' AND state IN ('pending', 'approved')`).Scan(&open)
		})
		if open != 1 {
			t.Fatalf("status = verifying but open release operations = %d, want 1", open)
		}
		return
	}
	t.Fatalf("status = %s, want verifying with a recreated release", f.statusOf(t, id))
}

// 項目 5: 値で返す事実が揃わない保留への回答の届け先は、タスクの単位で絞る。別のタスクの実行が間に入っても、回答は届く。
func TestStuckPaths_FactsUnmetAnswerIsDeliveredPerTaskNotPerChallenge(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	unmet := intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"self_review": "incomplete", "gate": "pass"})
	f.readOnce(t, unmet)
	f.readOnce(t, unmet)
	f.answerLatestHold(t, f.cid, "OWNER-ANSWER-TEXT")
	other := addTaskRowForTest(t, f.s, f.intID(t), 1, "t2", "main-repo", "impl")
	tid, _ := parseTaskID(other.ID)
	// 回答より後に、別のタスクの実行が始まって終わった。
	f.sql(t, `INSERT INTO run (cycle_id, kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, task_id)
		SELECT cycle_id, 'contract', challenge_id, challenge_version, 'other-session', pid, host, heartbeat_at, '2999-01-01T00:00:00.000Z', '2999-01-01T00:00:01.000Z', 'succeeded', max_budget_usd, budget_bucket, ?
		FROM run WHERE kind = 'contract' ORDER BY id DESC LIMIT 1`, tid)
	// 別のタスクの facts_unmet の保留も、回答より後に回答されている（別のタスクの回答が、このタスクへ混ざらない）。
	f.sql(t, `INSERT INTO hold (challenge_id, question, reason, from_status, raised_at, answer, answered_at, answered_by, task_id)
		VALUES (?, 'q2', 'facts_unmet', 'in_progress', '2998-01-01T00:00:00.000Z', 'OTHER-TASK-ANSWER', '2998-01-01T00:00:01.000Z', 'owner', ?)`, f.intID(t), tid)
	f.readOnce(t, unmet)
	note := f.lastNote(t)
	if !strings.Contains(note, "OWNER-ANSWER-TEXT") {
		t.Fatalf("note = %q, want the owner's answer (another task's run must not hide it)", note)
	}
	if strings.Contains(note, "OTHER-TASK-ANSWER") {
		t.Fatalf("note = %q, must not carry another task's answer", note)
	}
}

// 項目 7: 統合先を使う課題のリポジトリを 1 つに限る確かめは、起動でも済みのタスクを含める（上げる依頼と同じ基準）。
// 済みのタスクと別のリポジトリのタスクを足した計画は、起動せず（費用を使う前に止め）、理由 integration_unavailable にする。
func TestStuckPaths_ADoneTaskAndAnotherRepositoryTaskAreNotStarted(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "repos2", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	cid, _ := parseChallengeID(id)
	if err := f.delegateFixture.exec(`UPDATE task SET repo = 'other-repo' WHERE challenge_id = ? AND key = 'b'`, cid); err != nil {
		t.Fatal(err)
	}
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusDone)
	res := f.runDelegationStage(t, nil)
	got := notStartedByReason(res, NotStartedIntegrationUnavailable)
	if len(got) != 1 || got[0].ChallengeID != id {
		t.Fatalf("not_started = %+v, want one integration_unavailable for %s", res.NotStarted, id)
	}
	if len(f.client.starts) != 0 {
		t.Errorf("starts = %v, want none", f.client.starts)
	}
}

// 項目 8: 保留を作り損ねた停止の掃引は、タスクの単位で見る。ほかのタスクの後の保留が、このタスクの保留の作り損ねを隠さない。
func TestStuckPaths_UnheldStopSweepIsPerTask(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	other := addTaskRowForTest(t, f.s, f.intID(t), 1, "t2", "main-repo", "impl")
	otherID, _ := parseTaskID(other.ID)
	// タスク 1 の実行が失敗で終わったのに、保留を作れていない。その後に、タスク 2 の保留が立って回答され、課題は着手中へ戻った。
	f.sql(t, `UPDATE contract_run SET state = 'failed', ended_at = '2026-11-20T00:00:00.000Z', verified_ref = NULL, land_ref = NULL`)
	f.sql(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, task_id)
		VALUES (?, 'q', 'in_progress', '2026-11-20T01:00:00.000Z', 'a', '2026-11-20T02:00:00.000Z', 'owner', ?)`, f.intID(t), otherID)
	n, err := f.s.HoldUnrecordedContractStops(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("held = %d, err = %v, want the failed task's stop to be held", n, err)
	}
}

// 項目 9: 走っている上げる依頼は、保留中の課題でも、状態・費用を読み続ける（レビュー待ちの受け取りはしない）。
func TestStuckPaths_RunningUpRequestIsReadWhileTheChallengeIsHeld(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.setUpPR("main")
	f.startUp(t)
	f.sqlExec(t, `UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, mustChallengeInt(t, id))
	cost := 1.5
	f.readWith(t, contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.CostUSD = &cost }))
	var micros int64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT cost_usd FROM contract_run WHERE kind = 'reflect'`).Scan(&micros)
	})
	if micros != usdToMicros(cost) {
		t.Fatalf("cost = %d, want %d (the held challenge's request must still be read)", micros, usdToMicros(cost))
	}
	// 保留中にレビュー待ちを読んでも、受け取らない（検証中へ進めず、反映の操作も作らない）。
	f.readWith(t, upReviewOut([]string{"respond", "land"}, upArtsOK...))
	if got := f.statusOf(t, id); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	if rels := f.releases(t); len(rels) != 0 {
		t.Fatalf("releases = %+v, want none", rels)
	}
}

// 項目 10: 統合先の識別を課題に初めて記録したとき、作業ログに残す（経路は委譲の読み。原因の run が付く）。
func TestStuckPaths_RecordingTheIntegrationRefIsLogged(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if got := f.text(t, `SELECT integration_ref FROM challenge`); got != integBranch {
		t.Fatalf("precondition: integration_ref = %q", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND action = '`+ActivityActionIntegrationRef+`'
		AND after LIKE '%`+integBranch+`%' AND channel = 'invoker'`); n != 1 {
		t.Fatalf("activity entries = %d, want 1", n)
	}
	// 同じ識別を読み直しても、記録は増えない。
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = '`+ActivityActionIntegrationRef+`'`); n != 1 {
		t.Fatalf("activity entries after a re-read = %d, want 1", n)
	}
}

// 項目 9（保留中に終わりを読んだとき）: 契約の実行は開けたまま残し、回答で着手中へ戻った後の状態の確認が、反映の操作を閉じて reflect_failed で保留にする。
func TestStuckPaths_UpRequestEndedWhileHeldIsHandledAfterTheAnswer(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.startUp(t)
	f.sqlExec(t, `UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, mustChallengeInt(t, id))
	f.readWith(t, contractOut(ContractStateFailed, nil, nil))
	if _, _, _, ended := f.reflectRow(t); ended {
		t.Fatal("the up request run was ended while the challenge was held (the ending would be lost)")
	}
	if got := f.queryString(t, `SELECT state FROM contract_run WHERE kind = 'reflect'`); got.String != string(ContractStateFailed) {
		t.Fatalf("recorded state = %q while held, want failed (the read continues while held)", got.String)
	}
	f.sqlExec(t, `UPDATE challenge SET status = 'in_progress' WHERE id = ?`, mustChallengeInt(t, id))
	f.readWith(t, contractOut(ContractStateFailed, nil, nil))
	if got := f.lastReason(t, id); got != string(holdReasonReflectFailed) {
		t.Fatalf("reason = %q, want reflect_failed", got)
	}
	if _, _, _, ended := f.reflectRow(t); !ended {
		t.Fatal("the up request run is still open after the hold was raised")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 1 {
		t.Fatalf("holds = %d, want exactly 1 (the ending read while held must not raise one)", n)
	}
}

// 項目 4（作れなかったとき）: 承認できる反映の操作を作れない（同じ PR の反映の操作が置き換え・差し戻し以外で閉じている）ときは、
// 検証中へ進めず、黙って止めずに保留にする。
func TestStuckPaths_UpRequestWithoutAnApprovableReleaseIsHeldNotVerified(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	cid, _ := parseChallengeID(id)
	f.setUpPR("main")
	f.sqlExec(t, `INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at, closed_reason, closed_at)
		VALUES (?, 'release', 's', ?, 'closed', 2, '2026-01-01T00:00:00.000Z', 'pr_closed', '2026-01-01T00:00:01.000Z')`, cid, upPRURL)
	f.startUp(t)
	f.readWith(t, upReviewOut([]string{"respond", "land"}, upArtsOK...))
	if got := f.statusOf(t, id); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	if got := f.lastReason(t, id); got != string(holdReasonReflectFailed) {
		t.Fatalf("reason = %q", got)
	}
}
