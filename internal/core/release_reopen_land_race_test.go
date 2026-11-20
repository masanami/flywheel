package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 完了から戻す前の読みの後、戻す書き込みの前に、別の反映の段がタスクの実行へ「入れてよい」を返して試行まで閉じていたら、
// 戻す書き込みの中の確かめ（taskRunUnchangedInTx）が、何も書かせず競合で止める（承認を置き換えた後に、委譲先が入れ続けない）。
func TestReopen_ALandSentBetweenTheReadAndTheWriteBlocksTheReopen(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	ctx := context.Background()
	cs := ReopenContractSources{Contract: f.client, Agent: &AgentDeclaration{FailureLimit: 3}, Connectors: f.conn}

	f.queueReview()
	mark, err := f.s.readTaskRunBeforeReopen(ctx, 1, cs)
	if err != nil || mark == nil {
		t.Fatalf("readTaskRunBeforeReopen = %+v, %v, want a mark of the waiting task run", mark, err)
	}
	check := func() error {
		var got error
		readForTest(t, f.s, func(tx *sql.Tx) error {
			row, err := loadOperationRow(ctx, tx, 1)
			if err != nil {
				return err
			}
			got = taskRunUnchangedInTx(ctx, tx, row.ChallengeID, mark)
			return nil
		})
		return got
	}
	if err := check(); err != nil {
		t.Fatalf("unchanged run: err = %v, want nil", err)
	}

	// 読みと書き込みの間に、反映の段が「入れてよい」を返す（試行は land_sent で閉じ、終わっていない試行も実行の記録も残らない）。
	f.queueReview()
	if item := f.release(t); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
	if err := check(); !errors.Is(err, errReopenBlockedByRun) || !errors.Is(err, ErrConflict) {
		t.Fatalf("after a land was sent: err = %v, want errReopenBlockedByRun (ErrConflict)", err)
	}
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
	set(`UPDATE contract_run SET ended_at = 'x' WHERE id = ?`, id)
	if err := check(nil); err != nil {
		t.Errorf("no unfinished task run: err = %v, want nil", err)
	}
}
