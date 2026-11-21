package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 区切り S3c（版 30）の受入基準（ストア）: 列と口。docs/features/core-redesign.md の「ストア」。

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

func TestContractFixRefs_ReadWriteClear(t *testing.T) {
	t.Parallel()
	s, _ := filledStore(t) // contract_run id=1 は 3 列とも値を持つ
	ctx := context.Background()
	if got := fixRefsOf(t, s, 1); got != (contractFixRefs{Pending: "pending01", Verified: "verified02", Rejected: "rejected03"}) {
		t.Fatalf("fixture refs = %+v", got)
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := clearContractFixRefs(ctx, tx, 1, contractFixPending); err != nil {
			return err
		}
		if err := writeContractFixRef(ctx, tx, 1, contractFixVerified, "v2"); err != nil {
			return err
		}
		return writeContractFixRef(ctx, tx, 1, contractFixRejected, "r2")
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixRefsOf(t, s, 1); got != (contractFixRefs{Verified: "v2", Rejected: "r2"}) {
		t.Errorf("after one clear and two writes = %+v, want only the pending column cleared", got)
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error { return clearContractFixRefs(ctx, tx, 1) }); err != nil {
		t.Fatal(err)
	}
	if got := fixRefsOf(t, s, 1); got != (contractFixRefs{}) {
		t.Errorf("after clearing all = %+v, want empty", got)
	}
	// 読む口（行の構造体）も同じ値を返す。
	if err := s.db.Write(ctx, func(tx *sql.Tx) error { return writeContractFixRef(ctx, tx, 1, contractFixPending, "p3") }); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		row, err := scanContractRun(tx.QueryRow(contractRunSelect + ` WHERE id = 1`))
		if err != nil {
			return err
		}
		if row.FixPendingRef != "p3" || row.FixVerifiedRef != "" || row.FixRejectedRef != "" {
			t.Errorf("contractRunRow fix refs = %q %q %q", row.FixPendingRef, row.FixVerifiedRef, row.FixRejectedRef)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestContractFixRefs_RejectsBadInput(t *testing.T) {
	t.Parallel()
	s, _ := filledStore(t)
	ctx := context.Background()
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := writeContractFixRef(ctx, tx, 1, contractFixColumn("land_ref"), "x"); !errors.Is(err, ErrValidation) {
			t.Errorf("a column outside the three: err = %v, want ErrValidation", err)
		}
		if err := writeContractFixRef(ctx, tx, 1, contractFixPending, ""); !errors.Is(err, ErrValidation) {
			t.Errorf("an empty ref: err = %v, want ErrValidation", err)
		}
		if err := clearContractFixRefs(ctx, tx, 1, contractFixColumn("verified_ref")); !errors.Is(err, ErrValidation) {
			t.Errorf("clearing a column outside the three: err = %v, want ErrValidation", err)
		}
		if err := writeContractFixRef(ctx, tx, 999, contractFixPending, "x"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("a missing row: err = %v, want sql.ErrNoRows", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fixRefsOf(t, s, 1).Pending; got != "pending01" {
		t.Errorf("a rejected write changed the row: pending = %q", got)
	}
}
