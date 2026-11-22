package core

import (
	"context"
	"database/sql"
	"testing"
)

// AC-246: サイクルが走っておらず、スーパーバイザー待ちの保留（前のサイクルで見送ったもの）が 1 件あるとき、「いま始められるもの」の数えに 1 件入る。
// オーナーへ上げた保留・止まり方を渡さずに作った保留は入らない。
func TestCountCycleWork_SupervisorPendingHoldCountsOne_AC246(t *testing.T) {
	t.Parallel()
	f := newJ5Fixture(t, "impl")
	before, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}

	for _, in := range []HoldOptionsInput{{}, {StopKind: StopKindVerifyUncertain, SupervisorEnabled: false}} {
		if _, err := holdViaEntrance(t, f.s, StatusVerifying, HoldKindHuman, in); err != nil {
			t.Fatal(err)
		}
	}
	mid, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if mid.Stages.Supervise != 0 || mid.Total != before.Total {
		t.Fatalf("スーパーバイザー待ちでない保留が数えに入っている: %+v (before %+v)", *mid, *before)
	}

	if _, err := holdViaEntrance(t, f.s, StatusVerifying, HoldKindHuman, HoldOptionsInput{StopKind: StopKindVerifyUncertain, SupervisorEnabled: true}); err != nil {
		t.Fatal(err)
	}
	after, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if after.Stages.Supervise != 1 || after.Total != before.Total+1 {
		t.Fatalf("work = %+v, want supervise=1 and total %d (before %+v)", *after, before.Total+1, *before)
	}

	// 上げた（escalated）保留・回答済みの保留・閉じた保留は入らない。
	for _, q := range []string{
		`UPDATE hold SET supervisor_state = 'escalated' WHERE supervisor_state = 'pending'`,
		`UPDATE hold SET supervisor_state = 'pending', answer = 'a', answered_at = '2026-11-22T00:00:00.000Z' WHERE supervisor_state = 'escalated'`,
		`UPDATE hold SET answer = NULL, answered_at = NULL, closed_at = '2026-11-22T00:00:00.000Z' WHERE supervisor_state = 'pending'`,
	} {
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec(q); return err }); err != nil {
			t.Fatal(err)
		}
		w, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
		if err != nil {
			t.Fatal(err)
		}
		if w.Stages.Supervise != 0 {
			t.Fatalf("%s の後も数えに入っている: %+v", q, *w)
		}
	}
}
