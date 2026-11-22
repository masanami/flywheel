package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、区切り S4b-2 の横断の検査（AC-162〜AC-165）。スーパーバイザーの手は、保留を閉じる・上げる・見送るだけで、
// 計画の承認・完了の承認・不可逆操作の承認の記録を作らず、課題・タスクの予算の枠も変えない。手は 7 つ全部を、実ストアで
// applySupervisorHand を通して実行する（既存の手ごとの試験と同じ作り）。

// guardSnapshot は、承認と不可逆操作と予算の枠を持つ表（承認・不可逆操作・計画の版ごとの枠・タスクの枠）の、全行の写し。
func guardSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	dump := coretest.DumpTables(t, s.Workspace(), "approval", "operation", "task_plan")
	var b strings.Builder
	b.WriteString(dump)
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, COALESCE(budget_impl_micros, -1), COALESCE(budget_review_micros, -1) FROM task ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id, impl, review int64
			if err := rows.Scan(&id, &impl, &review); err != nil {
				return err
			}
			fmt.Fprintf(&b, "task %d impl=%d review=%d\n", id, impl, review)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// guardRow は、手 hand を実行した結果の検査に使う、保留の閉じ方（回答した者・本人確認・処理の状態）。
func guardHoldRow(t *testing.T, f *offeredFixture, holdID int64) string {
	t.Helper()
	var by, ver, state sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT answered_by, answered_verification, supervisor_state FROM hold WHERE id = ?`, holdID).Scan(&by, &ver, &state)
	}); err != nil {
		t.Fatal(err)
	}
	return by.String + "/" + ver.String + "/" + state.String
}

// 7 つの手（保留を閉じる 5 つ・見送る・上げる）のどれを実行しても、承認の記録・不可逆操作・予算の枠の表は 1 行も変わらない。
// 保留を閉じた手は、閉じた者を supervisor・本人確認なしと記録し、オーナーの回答（本人確認つき）と区別する。
func TestSupervisorGuard_NoHandMintsAnApprovalOrChangesABudget(t *testing.T) {
	t.Parallel()
	type scenario struct {
		hand   SupervisorHand
		closes bool // 保留を閉じる手（見送る・上げる手は閉じない）
		run    func(t *testing.T) (f *offeredFixture, holdID int64, outcome supervisorHandOutcome)
	}
	viaSB := func(hand SupervisorHand, kind StopKind, prepare func(t *testing.T, f *sbFixture)) func(t *testing.T) (*offeredFixture, int64, supervisorHandOutcome) {
		return func(t *testing.T) (*offeredFixture, int64, supervisorHandOutcome) {
			f := newSBFixture(t)
			tg := f.hold(t, StatusInProgress, kind, true)
			if prepare != nil {
				prepare(t, f)
			}
			before := guardSnapshot(t, f.s)
			out := f.apply(t, tg, hand, "NOTE-GUARD")
			if after := guardSnapshot(t, f.s); after != before {
				t.Errorf("%s: approvals, operations or budgets changed:\n--- before\n%s\n--- after\n%s", hand, before, after)
			}
			return f.offeredFixture, tg.HoldID, out
		}
	}
	scenarios := []scenario{
		{HandReverify, true, viaSB(HandReverify, StopKindVerifyUncertain, nil)},
		{HandSendBack, true, viaSB(HandSendBack, StopKindVerifyUncertain, func(t *testing.T, f *sbFixture) {
			f.exec2(t, `UPDATE task SET status = 'verifying' WHERE id = ?`, f.taskID)
		})},
		{HandRetry, true, viaSB(HandRetry, StopKindDelegateStopped, nil)},
		{HandReplan, true, viaSB(HandReplan, StopKindDelegateStopped, nil)},
		{HandDefer, false, viaSB(HandDefer, StopKindUpstreamUnreadable, nil)},
		{HandEscalate, false, viaSB(HandEscalate, StopKindFactsUnmet, nil)},
		{HandSplitFollowup, true, func(t *testing.T) (*offeredFixture, int64, supervisorHandOutcome) {
			f := newSplitFixture(t, [2]string{splitCondA, "after_release"}, [2]string{splitCondB, "after_human_action"})
			before := guardSnapshot(t, f.s)
			out := f.do(t, 1, 2)
			if after := guardSnapshot(t, f.s); after != before {
				t.Errorf("split_followup: approvals, operations or budgets changed:\n--- before\n%s\n--- after\n%s", before, after)
			}
			return f.offeredFixture, f.tg.HoldID, out
		}},
	}
	// answer は保留を指さない（子の問いに保留を作る前に答える）ので、この表の外。承認・予算に触れないことは supervisor_child_question_test.go の TestChildQuestion_AnswerChangesNoApprovalOrBudget が確かめる。
	if len(scenarios)+1 != len(SupervisorHandValues()) {
		t.Fatalf("scenarios = %d, hands = %d: every hand except answer must be covered", len(scenarios), len(SupervisorHandValues()))
	}
	for _, sc := range scenarios {
		sc := sc
		t.Run(string(sc.hand), func(t *testing.T) {
			t.Parallel()
			f, holdID, out := sc.run(t)
			if out.Result != ActionExecuted {
				t.Fatalf("outcome = %+v, want executed (a refused hand proves nothing)", out)
			}
			got := guardHoldRow(t, f, holdID)
			want := "supervisor/none/handled"
			switch sc.hand {
			case HandDefer:
				want = "//pending" // 見送りは、スーパーバイザー待ちのまま残す
			case HandEscalate:
				want = "//escalated" // 上げた保留は、オーナーの回答を待つ（スーパーバイザーが回答したことにしない）
			}
			if got != want {
				t.Errorf("hold (answered_by/verification/state) = %q, want %q", got, want)
			}
		})
	}
}

// 上の検査が空振りでないことの確認: 承認の記録・予算の枠が変われば、写しは変わる。
func TestSupervisorGuard_SnapshotDetectsAnApprovalAndABudgetChange(t *testing.T) {
	t.Parallel()
	f := newSBFixture(t)
	base := guardSnapshot(t, f.s)
	f.exec2(t, `INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at)
		VALUES (?, 'plan', 'approved', 1, 'x', 'cli', 'none', '2026-11-22T00:00:00.000Z')`, f.cidInt)
	withApproval := guardSnapshot(t, f.s)
	if withApproval == base {
		t.Error("the snapshot does not change when an approval is added")
	}
	f.exec2(t, `UPDATE task SET budget_impl_micros = 1 WHERE id = ?`, f.taskID)
	if guardSnapshot(t, f.s) == withApproval {
		t.Error("the snapshot does not change when a task budget changes")
	}
}
