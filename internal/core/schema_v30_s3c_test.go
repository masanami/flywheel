package core

import (
	"context"
	"database/sql"
	"testing"
)

// 区切り S3c（版 30）の受入基準（ストア）: 列と口。docs/features/core-redesign.md の「ストア」。

// contractFixRefs は試験が見る、契約の実行の 3 列。値が無い（NULL）列は空文字。
type contractFixRefs struct {
	Pending, Verified, Rejected string
}

// readContractFixRefs は契約の実行（内部整数 ID rowID）の 3 列を読む。行が無ければ sql.ErrNoRows を返す。
func readContractFixRefs(ctx context.Context, tx *sql.Tx, rowID int64) (contractFixRefs, error) {
	var r contractFixRefs
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(fix_pending_ref, ''), COALESCE(fix_verified_ref, ''), COALESCE(fix_rejected_ref, '')
		FROM contract_run WHERE id = ?`, rowID).Scan(&r.Pending, &r.Verified, &r.Rejected)
	return r, err
}

func fixRefsOf(t *testing.T, s *Store, id int64) contractFixRefs {
	t.Helper()
	var r contractFixRefs
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		r, err = readContractFixRefs(context.Background(), tx, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStoreV30_ExecutionResultAndActivityActionAreWritable(t *testing.T) {
	t.Parallel()
	s, _ := filledStore(t)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE operation_execution SET result = ? WHERE id = (SELECT MIN(id) FROM operation_execution)`, string(ExecutionResultWaitingFixCheck)); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, before, after)
			VALUES ('2026-11-01T00:00:00.000Z', 'core', 'cli', 'none', 'task', 1, ?, '{"ref":"a"}', '{"ref":"b","files":[],"run_id":1}')`, ActivityActionConflictFixPassed)
		return err
	}); err != nil {
		t.Fatalf("writing waiting_fix_check / conflict_fix_passed: %v", err)
	}
	if got, ok := ParseExecutionResult("waiting_fix_check"); !ok || got != ExecutionResultWaitingFixCheck {
		t.Errorf("ParseExecutionResult(waiting_fix_check) = %q, %v", got, ok)
	}
}

// 契約の実行の 3 列は、契約の実行の行の構造体（scanContractRun）からも読める。
func TestContractFixRefs_ReadFromTheRunRow(t *testing.T) {
	t.Parallel()
	s, _ := filledStore(t) // contract_run id=1 は 3 列とも値を持つ
	ctx := context.Background()
	if got := fixRefsOf(t, s, 1); got != (contractFixRefs{Pending: "pending01", Verified: "verified02", Rejected: "rejected03"}) {
		t.Fatalf("fixture refs = %+v", got)
	}
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		row, err := scanContractRun(tx.QueryRow(contractRunSelect + ` WHERE id = 1`))
		if err != nil {
			return err
		}
		if row.FixPendingRef != "pending01" || row.FixVerifiedRef != "verified02" || row.FixRejectedRef != "rejected03" {
			t.Errorf("contractRunRow fix refs = %q %q %q", row.FixPendingRef, row.FixVerifiedRef, row.FixRejectedRef)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
