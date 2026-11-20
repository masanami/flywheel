package core

import (
	"context"
	"testing"
)

// 項目 6（起動の読み）: 統合先を使う課題のタスクの契約の実行が、起動の応答のうちに succeeded を返しても（land_ref 無し）、終わりの記録は
// 保留と同じ書き込みにする。保留の書き込みが失敗しても、保留の無い終わった記録だけが残らない（開いたまま次の状態の確認が読み直す）。
func TestStuckPaths_StartReturningSucceededDoesNotEndTheRunWithoutAHold(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 1)
	f.newMulti(t, "fast", "P1", true, multiTask{key: "a"})
	if err := f.delegateFixture.exec(`CREATE TRIGGER fail_hold BEFORE INSERT ON hold BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	f.client.startOut = contractOut(ContractStateSucceeded, nil, nil)
	_, _ = f.s.RunDelegation(context.Background(), f.runInput(t, nil))
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run`); n != 1 {
		t.Fatalf("contract runs = %d, want 1 (the start did happen)", n)
	}
	if f.count(t, `SELECT COUNT(*) FROM hold`) != 0 {
		t.Fatal("a hold remains although its write failed")
	}
	if got := f.queryString(t, `SELECT ended_at FROM contract_run`); got.Valid && got.String != "" {
		t.Fatalf("the contract run is ended (%s) without a hold (the next poll would pick up nothing)", got.String)
	}
}
