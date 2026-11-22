package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// lastHoldStopOf は、いちばん新しい保留の「止まり方/スーパーバイザーの処理の状態」を返す（NULL は "<null>"。保留が無ければ "<no hold>"）。
func lastHoldStopOf(t *testing.T, s *Store) string {
	t.Helper()
	var kind, state sql.NullString
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT stop_kind, supervisor_state FROM hold ORDER BY id DESC LIMIT 1`).Scan(&kind, &state)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "<no hold>"
	}
	if err != nil {
		t.Fatal(err)
	}
	orNull := func(v sql.NullString) string {
		if !v.Valid {
			return "<null>"
		}
		return v.String
	}
	return orNull(kind) + "/" + orNull(state)
}

// wantLastHoldStop は、いちばん新しい保留の止まり方と処理の状態が want（"kind/state"）であることを確かめる。
func wantLastHoldStop(t *testing.T, s *Store, want string) {
	t.Helper()
	if got := lastHoldStopOf(t, s); got != want {
		t.Errorf("stop_kind/supervisor_state = %s, want %s", got, want)
	}
}

// 対象の止まり方の保留は、スーパーバイザー待ち（pending）。
const (
	stopPendingIntake = "intake_unverified/pending"
	stopNone          = "<null>/<null>"
)

// 宣言の supervisor が偽のとき、契約の実行の失敗の保留は、止まり方は残るがスーパーバイザー待ちにならない（契約の経路が宣言を運ぶ）。
func TestSupervisorOff_ContractFailureHoldIsNotPending(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	f.agent.Supervisor = false
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateFailed, nil, nil)}
	f.poll(t)
	wantLastHoldStop(t, f.s, "delegate_stopped/<null>")
}

// 宣言の supervisor が偽のとき、委譲の連続の失敗の上限の保留も、スーパーバイザー待ちにならない（照合・上限の経路が宣言を運ぶ）。
func TestSupervisorOff_DelegationFailureLimitHoldIsNotPending(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	f.agent.Supervisor = false
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), failOut(RunResultErrored)}
	f.runDelegation(t, id)
	f.runDelegation(t, id)
	if _, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), nil)); err != nil {
		t.Fatal(err)
	}
	wantLastHoldStop(t, f.s, "delegate_stopped/<null>")
}
