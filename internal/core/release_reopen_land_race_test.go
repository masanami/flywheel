package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// reopenWhileLanding は、完了から戻す操作（公開の入口）を呼び、その読み（status）と書き込みの間に、反映の段を 1 回動かす。
// 反映の段の結果と、戻す操作の結果を返す。
func (f *landFixture) reopenWhileLanding(t *testing.T) (ReleaseExecItem, *ReopenForReleaseResult, error) {
	t.Helper()
	cmp := f.contentChangedCompare()
	changed := f.fake.pr.HeadSHA
	var item ReleaseExecItem
	f.s.reopenReadDone = func() {
		// 反映の段は、承認した中身のままの成果物を見て「入れてよい」を返す（戻す側の評価は、もう済んでいる）。
		f.fake.pr.HeadSHA = f.head
		f.queueReview()
		item = f.release(t)
		f.fake.pr.HeadSHA = changed
	}
	f.queueReview()
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(cmp))
	return item, res, err
}

func (f *landFixture) wantNotReopened(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errReopenBlockedByRun) || !errors.Is(err, ErrConflict) {
		t.Fatalf("ReopenChallengeForRelease err = %v, want errReopenBlockedByRun (ErrConflict)", err)
	}
	if got := f.status(t, f.cid); got != StatusDone {
		t.Errorf("challenge = %s, want done", got)
	}
	var reopens, ops int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM challenge_reopen`).Scan(&reopens); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT COUNT(*) FROM operation`).Scan(&ops)
	})
	if reopens != 0 || ops != 1 {
		t.Errorf("reopens = %d, operations = %d, want nothing written", reopens, ops)
	}
}

// 完了から戻す前の読みの後、戻す書き込みの前に、別の反映の段がタスクの実行へ「入れてよい」を返して試行まで閉じていたら、
// 戻す操作は何も書かず競合で終わる（承認を置き換えた後に、委譲先が入れ続けない）。委譲先の応答が running の場合。
func TestReopen_ALandSentBetweenTheReadAndTheWriteBlocksTheReopen(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	item, _, err := f.reopenWhileLanding(t)
	if item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
	f.wantNotReopened(t, err)
}

// 同じ並びで、「入れてよい」の応答が待ちの状態だった（記録の状態が running にならない）ときも、止まる
// （止める条件ごとの確かめは TestTaskRunUnchangedInTx_Cases）。
func TestReopen_ALandAnsweredWithAWaitingStateBetweenTheReadAndTheWriteBlocksTheReopen(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.client.resumeOut = reviewWaitOutFor(reviewPRURL)
	item, _, err := f.reopenWhileLanding(t)
	if item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
	f.wantNotReopened(t, err)
}

// 読みの前に「入れてよい」が届いたか分からないまま（land_ref が在り、land_delivered が偽）で、読みと書き込みの間にもう一度、
// 届いたか分からない「入れてよい」が返されたとき、land_ref・land_delivered・記録の状態は読みの時点と同じ値に戻るが、
// 試行が増えているので止まる。
func TestReopen_AnUnconfirmedLandRepeatedBetweenTheReadAndTheWriteBlocksTheReopen(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.client.resumeOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	f.queueReview()
	if item := f.release(t); item.Result != ExecutionResultLandUnconfirmed {
		t.Fatalf("item = %+v, want land_unconfirmed", item)
	}
	before, beforeDelivered := f.landRow(t)
	item, _, err := f.reopenWhileLanding(t)
	if item.Result != ExecutionResultLandUnconfirmed {
		t.Fatalf("item = %+v, want land_unconfirmed", item)
	}
	if ref, delivered := f.landRow(t); ref != before || delivered != beforeDelivered {
		t.Fatalf("land_ref=%q delivered=%v, want the same values as before (%q, %v)", ref, delivered, before, beforeDelivered)
	}
	f.wantNotReopened(t, err)
}

// 記録した状態が running の実行・読みの時点に無かった実行・land_ref と land_delivered のどちらかが違う実行は、どれも止める。
// 終わっていないタスクの実行が無ければ通す。
func TestTaskRunUnchangedInTx_Cases(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	ctx := context.Background()
	var cid, id int64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT challenge_id, id FROM contract_run WHERE ended_at IS NULL`).Scan(&cid, &id)
	})
	set := func(q string, args ...any) {
		t.Helper()
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(q, args...)
			return err
		})
	}
	check := func(mark *taskRunMark) error {
		var got error
		readForTest(t, f.s, func(tx *sql.Tx) error {
			got = taskRunUnchangedInTx(ctx, tx, cid, mark)
			return nil
		})
		return got
	}
	same := &taskRunMark{id: id}
	if err := check(same); err != nil {
		t.Errorf("same: err = %v, want nil", err)
	}
	if err := check(nil); !errors.Is(err, errReopenBlockedByRun) {
		t.Errorf("a run the read did not see: err = %v, want blocked", err)
	}
	if err := check(&taskRunMark{id: id + 1}); !errors.Is(err, errReopenBlockedByRun) {
		t.Errorf("another run: err = %v, want blocked", err)
	}
	set(`UPDATE contract_run SET land_ref = 'h' WHERE id = ?`, id)
	if err := check(same); !errors.Is(err, errReopenBlockedByRun) {
		t.Errorf("land_ref written: err = %v, want blocked", err)
	}
	set(`UPDATE contract_run SET land_delivered = 1 WHERE id = ?`, id)
	if err := check(&taskRunMark{id: id, landRef: "h"}); !errors.Is(err, errReopenBlockedByRun) {
		t.Errorf("land_delivered turned true: err = %v, want blocked", err)
	}
	if err := check(&taskRunMark{id: id, landRef: "h", landDelivered: true}); err != nil {
		t.Errorf("same land state: err = %v, want nil", err)
	}
	set(`UPDATE contract_run SET state = 'running' WHERE id = ?`, id)
	if err := check(&taskRunMark{id: id, landRef: "h", landDelivered: true}); !errors.Is(err, errReopenBlockedByRun) {
		t.Errorf("recorded as running: err = %v, want blocked", err)
	}
	set(`UPDATE contract_run SET state = 'waiting' WHERE id = ?`, id)
	var lastAttempt int64
	readForTest(t, f.s, func(tx *sql.Tx) (err error) {
		lastAttempt, err = lastReleaseAttemptID(ctx, tx, cid)
		return err
	})
	withAttempt := &taskRunMark{id: id, landRef: "h", landDelivered: true, lastAttempt: lastAttempt}
	if err := check(withAttempt); err != nil {
		t.Errorf("no newer attempt: err = %v, want nil", err)
	}
	set(`INSERT INTO operation_execution (operation_id, started_at, ended_at, result, approved_head, pid, host) VALUES (1, ?, ?, 'land_unconfirmed', 'h', 1, 'testhost')`, execTS(1), execTS(2))
	if err := check(withAttempt); !errors.Is(err, errReopenBlockedByRun) {
		t.Errorf("a newer attempt of the release: err = %v, want blocked", err)
	}
	set(`UPDATE contract_run SET ended_at = 'x' WHERE id = ?`, id)
	if err := check(nil); err != nil {
		t.Errorf("no unfinished task run: err = %v, want nil", err)
	}
}

// 戻す前の読みの時点で、その課題の反映の操作に終わっていない試行（反映の段が「入れてよい」を返す途中かもしれない）があれば、
// 委譲先を読まずに、何も書かず競合で終わる（その試行が読みの後に閉じても、最後の試行の ID は変わらないので、読みの側で止める）。
func TestReopen_AnOpenAttemptAtTheReadBlocksTheReopenWithoutReadingTheDelegate(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	cmp := f.contentChangedCompare()
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO operation_execution (operation_id, started_at, approved_head, pid, host) VALUES (1, ?, ?, 1, 'testhost')`, execTS(1), f.head)
		return err
	})
	statuses := len(f.client.statuses)
	called := false
	f.s.reopenReadDone = func() { called = true }
	_, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(cmp))
	f.wantNotReopened(t, err)
	if len(f.client.statuses) != statuses || called {
		t.Errorf("statuses +%d, reached the write = %v, want the reopen to stop at the read", len(f.client.statuses)-statuses, called)
	}
}
