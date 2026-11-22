package core

import (
	"context"
	"database/sql"
	"testing"
)

// AC-105 の土台: 「作業している契約の実行があるか」の判定（contractRunWorkingTx）は、人の作り直しの回答・スーパーバイザーの手の判定・手の実行の断りが
// 共有する 1 つの関数で、いまの人の作り直しの回答と同じ条件で決まる（開いていて最後に読んだ状態が running のタスクの実行・届いたか分からない
// 「入れてよい」・保留の間の成功が未処理の実行が作業中。レビュー待ち・終わりを読んだ実行・上げる依頼は作業中でない）。
func TestContractRunWorkingTx_TruthTable(t *testing.T) {
	t.Parallel()
	s, _ := filledStore(t) // 課題 1 に、種類 reflect・状態 running の契約の実行が 1 件ある
	cases := []struct {
		name string
		set  string
		want bool
	}{
		{"a reflect run is not task work", `UPDATE contract_run SET kind = 'reflect', state = 'running', ended_at = NULL, held_success = 0, land_ref = NULL, land_delivered = 0`, false},
		{"an open running task run is working", `UPDATE contract_run SET kind = 'task', state = 'running', ended_at = NULL, held_success = 0, land_ref = NULL, land_delivered = 0`, true},
		{"a review-waiting task run is not working", `UPDATE contract_run SET kind = 'task', state = 'waiting', ended_at = NULL, held_success = 0, land_ref = NULL, land_delivered = 0`, false},
		{"a task run that ended is not working", `UPDATE contract_run SET kind = 'task', state = 'running', ended_at = '2026-10-29T00:00:00.000Z', held_success = 0, land_ref = NULL, land_delivered = 0`, false},
		{"a success held during a hold is working", `UPDATE contract_run SET kind = 'task', state = 'succeeded', ended_at = '2026-10-29T00:00:00.000Z', held_success = 1, land_ref = NULL, land_delivered = 0`, true},
		{"an undelivered land is working", `UPDATE contract_run SET kind = 'task', state = 'waiting', ended_at = NULL, held_success = 0, land_ref = 'abc', land_delivered = 0`, true},
		{"a delivered land awaiting review is not working", `UPDATE contract_run SET kind = 'task', state = 'waiting', ended_at = NULL, held_success = 0, land_ref = 'abc', land_delivered = 1`, false},
	}
	for _, c := range cases {
		if err := s.db.Write(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec(c.set); return err }); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var got bool
		if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var err error
			got, err = contractRunWorkingTx(context.Background(), tx, 1)
			return err
		}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: working = %v, want %v", c.name, got, c.want)
		}
	}
}
