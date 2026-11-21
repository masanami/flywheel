package core

import (
	"context"
	"database/sql"
	"testing"
)

// 反映先宛ての衝突の解消の確かめ（J5）の結果の書き込み（writeReflectFix。AC-47〜AC-49・AC-60〜AC-63・AC-70・AC-71・AC-77・AC-78・AC-101）を、
// 実ストア・本番の書き込みの口で固定する。

func newReflectWriterFixture(t *testing.T) *fixFixture {
	t.Helper()
	f := newFixReflectFixture(t)
	f.s.fixWriters = f.s.defaultFixWriters()
	return f
}

func (f *fixFixture) refsOf(t *testing.T) contractFixRefs {
	t.Helper()
	var r contractFixRefs
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		r, err = readContractFixRefs(context.Background(), tx, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *fixFixture) challengeStatusOf(t *testing.T) string {
	t.Helper()
	var s string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT status FROM challenge`).Scan(&s) }); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fixFixture) opStateOf(t *testing.T) string {
	t.Helper()
	var s string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT state FROM operation`).Scan(&s) }); err != nil {
		t.Fatal(err)
	}
	return s
}

// AC-47〜AC-49・AC-101: 「満たした」→ fix_verified_ref は確かめ待ちだった中身になり、fix_pending_ref は NULL、課題は done のまま。
// 作業ログ conflict_fix_passed の entity は反映の操作。
func TestReflectFixWriter_MetRecordsVerifiedAndLogsOnTheOperation(t *testing.T) {
	t.Parallel()
	f := newReflectWriterFixture(t)
	f.setVerdict("met")
	f.verify(t)
	if got := f.refsOf(t); got.Verified != fixPending || got.Pending != "" || got.Rejected != "" {
		t.Fatalf("fix refs = %+v", got)
	}
	if f.challengeStatusOf(t) != "done" || f.opStateOf(t) != "approved" {
		t.Fatalf("challenge = %s operation = %s", f.challengeStatusOf(t), f.opStateOf(t))
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed' AND entity = 'operation'`); n != 1 { // AC-101
		t.Fatalf("conflict_fix_passed on the operation = %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed'`); n != 1 {
		t.Fatalf("conflict_fix_passed = %d", n)
	}
}

// AC-60〜AC-63・AC-70: 「満たしていない」「判断できない」→ fix_rejected_ref は確かめ待ちだった中身になり、fix_pending_ref は NULL。
// 反映の承認・反映の操作・課題の状態は変えず、conflict_fix_passed は残さない。
func TestReflectFixWriter_NotMetAndUncertainRecordRejectedOnly(t *testing.T) {
	t.Parallel()
	for _, verdict := range []string{"not_met", "uncertain"} {
		verdict := verdict
		t.Run(verdict, func(t *testing.T) {
			t.Parallel()
			f := newReflectWriterFixture(t)
			f.setVerdict(verdict)
			f.verify(t)
			if got := f.refsOf(t); got.Rejected != fixPending || got.Pending != "" || got.Verified != "" {
				t.Fatalf("fix refs = %+v", got)
			}
			if f.challengeStatusOf(t) != "done" || f.opStateOf(t) != "approved" {
				t.Fatalf("challenge = %s operation = %s", f.challengeStatusOf(t), f.opStateOf(t))
			}
			if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed'`); n != 0 {
				t.Fatalf("conflict_fix_passed = %d", n)
			}
		})
	}
}

// AC-71: J5 の呼び出しが連続の失敗の上限まで失敗したら、fix_rejected_ref は確かめ待ちだった中身になる。
func TestReflectFixWriter_FailureLimitRecordsRejected(t *testing.T) {
	t.Parallel()
	f := newReflectWriterFixture(t)
	f.j5.j5.result = JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	for i := 0; i <= judgmentFailureLimit(f.j5.agent); i++ {
		f.verify(t)
	}
	if got := f.refsOf(t); got.Rejected != fixPending || got.Pending != "" {
		t.Fatalf("fix refs = %+v", got)
	}
}

// AC-77・AC-78: 結果を書く直前に、中身の識別が確かめ待ちの中身と違う・課題が done でないなら、fix_verified_ref は書かれない。
func TestReflectFixWriter_DiscardedWhenTheContentMovedOrTheChallengeLeftDone(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"content moved on": `UPDATE contract_run SET content_ref = 'newer01'`,
		"not done":         `UPDATE challenge SET status = 'verifying'`,
	}
	for name, q := range cases {
		q := q
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newReflectWriterFixture(t)
			f.setVerdict("met")
			f.j5.j5.onInvoke = func(JudgmentLaunchInput) { f.sql(t, q) }
			f.verify(t)
			if got := f.refsOf(t); got.Verified != "" {
				t.Fatalf("fix refs = %+v, want no fix_verified_ref", got)
			}
		})
	}
}
