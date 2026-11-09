package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 本番反映でマージできなくなった完了の課題を着手中へ戻す書き込み（T18。#485）。

// insertCompletionApprovalForTest は課題に、有効な完了の承認（kind=completion・approved）を 1 件足し、その ID を返す。
func insertCompletionApprovalForTest(t *testing.T, s *Store, cid int64) int64 {
	t.Helper()
	var id int64
	writeForTest(t, s, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, decided_at)
			VALUES (?, NULL, 'completion', 'approved', 1, 'alice', 'cli', 'tty_confirm', ?)`, cid, execTS(1))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id
}

type reopenSnapshot struct {
	chStatus  string
	chVersion int
	ops       []reopenOp
	approvals []reopenApproval
	reopens   []reopenRow
}
type reopenOp struct {
	id         int64
	state      string
	version    int
	ref        string
	summary    string
	closed     sql.NullString
	closedAt   sql.NullString
	supersedes sql.NullInt64
}
type reopenApproval struct {
	id         int64
	kind       string
	supAt      sql.NullString
	supReason  sql.NullString
	supBy      sql.NullInt64
	mergeInOld sql.NullString
}
type reopenRow struct {
	id, challengeID, opID, newOpID int64
	reason                         string
	approvalID                     sql.NullInt64
}

func snapshotReopen(t *testing.T, s *Store, cid int64) reopenSnapshot {
	t.Helper()
	var snap reopenSnapshot
	readForTest(t, s, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT status, version FROM challenge WHERE id = ?`, cid).Scan(&snap.chStatus, &snap.chVersion); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT id, state, version, COALESCE(ref, ''), summary, closed_reason, closed_at, supersedes_operation_id FROM operation WHERE challenge_id = ? ORDER BY id`, cid)
		if err != nil {
			return err
		}
		for rows.Next() {
			var o reopenOp
			if err := rows.Scan(&o.id, &o.state, &o.version, &o.ref, &o.summary, &o.closed, &o.closedAt, &o.supersedes); err != nil {
				_ = rows.Close()
				return err
			}
			snap.ops = append(snap.ops, o)
		}
		_ = rows.Close()
		arows, err := tx.Query(`SELECT id, kind, superseded_at, superseded_reason, superseded_by_approval_id, merge_in_verified_head FROM approval WHERE challenge_id = ? ORDER BY id`, cid)
		if err != nil {
			return err
		}
		for arows.Next() {
			var a reopenApproval
			if err := arows.Scan(&a.id, &a.kind, &a.supAt, &a.supReason, &a.supBy, &a.mergeInOld); err != nil {
				_ = arows.Close()
				return err
			}
			snap.approvals = append(snap.approvals, a)
		}
		_ = arows.Close()
		rrows, err := tx.Query(`SELECT id, challenge_id, operation_id, new_operation_id, reason, superseded_approval_id FROM challenge_reopen WHERE challenge_id = ? ORDER BY id`, cid)
		if err != nil {
			return err
		}
		for rrows.Next() {
			var r reopenRow
			if err := rrows.Scan(&r.id, &r.challengeID, &r.opID, &r.newOpID, &r.reason, &r.approvalID); err != nil {
				_ = rrows.Close()
				return err
			}
			snap.reopens = append(snap.reopens, r)
		}
		return rrows.Close()
	})
	return snap
}

func TestReopenChallengeForRelease_MovesTheDoneChallengeBackAndReplacesTheRelease(t *testing.T) {
	f := newRelFixture(t)
	approvalID := insertCompletionApprovalForTest(t, f.s, f.cid)
	before := snapshotReopen(t, f.s, f.cid)

	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{
		OperationID: formatOperationID(f.opID), Reason: ReopenReasonNotMergeable,
	})
	if err != nil {
		t.Fatalf("ReopenChallengeForRelease: %v", err)
	}
	if !res.Reopened || res.ChallengeID != formatChallengeID(f.cid) || res.ClosedOperationID != formatOperationID(f.opID) || res.NewOperationID == "" {
		t.Fatalf("result = %+v, want reopened with old and new operation IDs", res)
	}

	after := snapshotReopen(t, f.s, f.cid)
	if after.chStatus != string(StatusInProgress) || after.chVersion != before.chVersion+1 {
		t.Errorf("challenge = %s v%d, want in_progress v%d", after.chStatus, after.chVersion, before.chVersion+1)
	}
	if len(after.ops) != 2 {
		t.Fatalf("operations = %+v, want the old (closed) and a new one", after.ops)
	}
	old, nw := after.ops[0], after.ops[1]
	if old.id != f.opID || old.state != string(OperationStateClosed) || old.closed.String != string(ClosedReasonSuperseded) || !old.closedAt.Valid || old.version != before.ops[0].version+1 {
		t.Errorf("old operation = %+v, want closed/superseded with version+1", old)
	}
	if nw.state != string(OperationStatePending) || nw.ref != relRef || nw.summary != before.ops[0].summary || nw.version != 1 || nw.supersedes.Int64 != f.opID {
		t.Errorf("new operation = %+v, want a pending release with the same ref and summary that supersedes OP %d", nw, f.opID)
	}
	if formatOperationID(nw.id) != res.NewOperationID {
		t.Errorf("new operation id = %s, result says %s", formatOperationID(nw.id), res.NewOperationID)
	}
	var prior reopenApproval
	for _, a := range after.approvals {
		if a.id == approvalID {
			prior = a
		}
	}
	if !prior.supAt.Valid || prior.supReason.String != string(ReopenReasonNotMergeable) || prior.supBy.Valid {
		t.Errorf("previous completion approval = %+v, want superseded (not_mergeable) and not yet linked to a new approval", prior)
	}
	if len(after.reopens) != 1 {
		t.Fatalf("challenge_reopen = %+v, want 1", after.reopens)
	}
	if r := after.reopens[0]; r.reason != string(ReopenReasonNotMergeable) || r.opID != f.opID || r.newOpID != nw.id || r.approvalID.Int64 != approvalID {
		t.Errorf("challenge_reopen = %+v", r)
	}

	// 作業ログ: 課題・古い操作・新しい操作。
	var actions []string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT entity || ':' || action FROM activity WHERE id > (SELECT COALESCE(MAX(id), 0) - 3 FROM activity) ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				return err
			}
			actions = append(actions, a)
		}
		return rows.Err()
	})
	want := map[string]bool{"challenge:reopen_for_release": false, "operation:op_close": false, "operation:op_add": false}
	for _, a := range actions {
		if _, ok := want[a]; ok {
			want[a] = true
		}
	}
	for a, seen := range want {
		if !seen {
			t.Errorf("activity %q missing; got %v", a, actions)
		}
	}
}

func TestReopenChallengeForRelease_PendingReleaseIsReplacedToo(t *testing.T) {
	f := newRelFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET state = 'pending' WHERE id = ?`, f.opID)
		return err
	})
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: formatOperationID(f.opID), Reason: ReopenReasonContentChanged})
	if err != nil || !res.Reopened {
		t.Fatalf("res = %+v, err = %v, want reopened", res, err)
	}
	snap := snapshotReopen(t, f.s, f.cid)
	if snap.ops[0].state != string(OperationStateClosed) || snap.ops[1].state != string(OperationStatePending) || snap.chStatus != string(StatusInProgress) {
		t.Errorf("snapshot = %+v", snap)
	}
	if len(snap.approvals) > 0 && snap.approvals[0].supAt.Valid && snap.approvals[0].kind == string(ApprovalKindRelease) {
		t.Errorf("release approval must not be marked superseded: %+v", snap.approvals[0])
	}
}

func TestReopenChallengeForRelease_DoesNothingUnlessTheChallengeIsDone(t *testing.T) {
	for _, st := range []Status{StatusInProgress, StatusAwaitingCompletionApproval, StatusAwaitingHuman, StatusWithdrawn} {
		t.Run(string(st), func(t *testing.T) {
			f := newRelFixture(t)
			setChallengeStatus(t, f.s, f.cid, st)
			before := snapshotReopen(t, f.s, f.cid)
			res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: formatOperationID(f.opID), Reason: ReopenReasonNotMergeable})
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if res.Reopened {
				t.Errorf("Reopened = true for a %s challenge", st)
			}
			after := snapshotReopen(t, f.s, f.cid)
			if after.chStatus != before.chStatus || after.chVersion != before.chVersion || len(after.ops) != len(before.ops) || after.ops[0].state != before.ops[0].state || len(after.reopens) != 0 {
				t.Errorf("state changed: before %+v after %+v", before, after)
			}
		})
	}
}

func TestReopenChallengeForRelease_DoesNothingForOperationsThatAreNotOpenReleases(t *testing.T) {
	for _, state := range []OperationState{OperationStateRejected, OperationStateExecuted, OperationStateClosed} {
		t.Run(string(state), func(t *testing.T) {
			f := newRelFixture(t)
			writeForTest(t, f.s, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE operation SET state = ? WHERE id = ?`, string(state), f.opID)
				return err
			})
			res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: formatOperationID(f.opID), Reason: ReopenReasonNotMergeable})
			if err != nil || res.Reopened {
				t.Fatalf("res = %+v err = %v, want no-op", res, err)
			}
			if snap := snapshotReopen(t, f.s, f.cid); snap.chStatus != string(StatusDone) || len(snap.ops) != 1 {
				t.Errorf("snapshot = %+v, want the challenge still done with one operation", snap)
			}
		})
	}
	t.Run("not a release", func(t *testing.T) {
		f := newRelFixture(t)
		op, err := f.s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: formatChallengeID(f.cid), Kind: "other", Summary: "x"})
		if err == nil {
			// done の課題には op add できない。
			t.Fatalf("CreateOperation on a done challenge succeeded: %+v", op)
		}
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE operation SET kind = 'other' WHERE id = ?`, f.opID)
			return err
		})
		res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: formatOperationID(f.opID), Reason: ReopenReasonNotMergeable})
		if err != nil || res.Reopened {
			t.Fatalf("res = %+v err = %v, want no-op for a non-release", res, err)
		}
	})
}

func TestReopenChallengeForRelease_RejectsBadInput(t *testing.T) {
	f := newRelFixture(t)
	if _, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: formatOperationID(f.opID), Reason: "merge_failed"}); !errors.Is(err, ErrValidation) {
		t.Errorf("unknown reason: err = %v, want ErrValidation", err)
	}
	if _, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: "OP-999", Reason: ReopenReasonNotMergeable}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown operation: err = %v, want ErrNotFound", err)
	}
	if _, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: "bad", Reason: ReopenReasonNotMergeable}); !errors.Is(err, ErrNotFound) {
		t.Errorf("malformed operation: err = %v, want ErrNotFound", err)
	}
	if snap := snapshotReopen(t, f.s, f.cid); snap.chStatus != string(StatusDone) {
		t.Errorf("challenge = %s, want done after rejected inputs", snap.chStatus)
	}
}

// 次の完了の承認が成立したとき、前の承認の行に新しい承認を結び、新しい release を同時に承認する。
func TestReopenChallengeForRelease_NextCompletionApprovalLinksThePreviousOneAndApprovesTheNewRelease(t *testing.T) {
	f := newRelFixture(t)
	prevApproval := insertCompletionApprovalForTest(t, f.s, f.cid)
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: formatOperationID(f.opID), Reason: ReopenReasonNotMergeable})
	if err != nil || !res.Reopened {
		t.Fatalf("reopen: %+v %v", res, err)
	}
	// 追従の後、検証を通って完了確認待ちへ進んだことにする。
	setChallengeStatus(t, f.s, f.cid, StatusAwaitingCompletionApproval)
	id := formatChallengeID(f.cid)
	prev, err := f.s.PrepareApproval(context.Background(), id)
	if err != nil {
		t.Fatalf("PrepareApproval: %v", err)
	}
	if len(prev.PendingReleases) != 1 || prev.PendingReleases[0].ID != res.NewOperationID {
		t.Fatalf("pending releases = %+v, want only the new one %s (the old one is closed)", prev.PendingReleases, res.NewOperationID)
	}
	head := "newhead"
	if _, _, err := f.s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: id, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		ReleaseHeads: []ReleaseHead{{Operation: prev.PendingReleases[0], HeadSHA: &head}},
	}, verifiedAttestationForTest(t, id)); err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
	snap := snapshotReopen(t, f.s, f.cid)
	if snap.chStatus != string(StatusDone) {
		t.Errorf("challenge = %s, want done after the new completion approval", snap.chStatus)
	}
	var newCompletion int64
	for _, a := range snap.approvals {
		if a.kind == string(ApprovalKindCompletion) && a.id != prevApproval {
			newCompletion = a.id
		}
	}
	if newCompletion == 0 {
		t.Fatalf("no new completion approval in %+v", snap.approvals)
	}
	for _, a := range snap.approvals {
		if a.id == prevApproval && a.supBy.Int64 != newCompletion {
			t.Errorf("previous approval superseded_by = %v, want %d", a.supBy, newCompletion)
		}
		if a.id == newCompletion && (a.supAt.Valid || a.supBy.Valid) {
			t.Errorf("new approval must be effective: %+v", a)
		}
	}
	if snap.ops[1].state != string(OperationStateApproved) {
		t.Errorf("new release = %s, want approved together with the completion approval", snap.ops[1].state)
	}
	// 完了は再び終端: 完了した課題には op add できない。
	if _, err := f.s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: id, Kind: "release", Summary: "x"}); !errors.Is(err, ErrTerminalState) {
		t.Errorf("CreateOperation on the re-completed challenge: err = %v, want ErrTerminalState", err)
	}
}

// 2 回戻しても、有効な完了の承認は常に 1 件で、前の承認はその次の承認に結ばれる。
func TestReopenChallengeForRelease_SecondReopenSupersedesOnlyTheEffectiveApproval(t *testing.T) {
	f := newRelFixture(t)
	first := insertCompletionApprovalForTest(t, f.s, f.cid)
	r1, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: formatOperationID(f.opID), Reason: ReopenReasonNotMergeable})
	if err != nil || !r1.Reopened {
		t.Fatalf("reopen 1: %+v %v", r1, err)
	}
	// 2 回目の完了の承認が成立した状態を作る（最初の承認は 2 番目に結ばれ、新しい release は承認済み）。
	second := insertCompletionApprovalForTest(t, f.s, f.cid)
	newOp, _ := parseOperationID(r1.NewOperationID)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE approval SET superseded_by_approval_id = ? WHERE id = ?`, second, first); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE operation SET state = 'approved', version = 2 WHERE id = ?`, newOp); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE challenge SET status = 'done' WHERE id = ?`, f.cid)
		return err
	})
	r2, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: r1.NewOperationID, Reason: ReopenReasonContentChanged})
	if err != nil || !r2.Reopened {
		t.Fatalf("reopen 2: %+v %v", r2, err)
	}
	snap := snapshotReopen(t, f.s, f.cid)
	if len(snap.reopens) != 2 || snap.reopens[1].approvalID.Int64 != second {
		t.Fatalf("reopens = %+v, want the second to supersede approval %d", snap.reopens, second)
	}
	for _, a := range snap.approvals {
		switch a.id {
		case first:
			if a.supReason.String != string(ReopenReasonNotMergeable) {
				t.Errorf("first approval reason = %v, want unchanged not_mergeable", a.supReason)
			}
		case second:
			if a.supReason.String != string(ReopenReasonContentChanged) {
				t.Errorf("second approval reason = %v, want content_changed", a.supReason)
			}
		}
	}
	if len(snap.ops) != 3 || snap.ops[2].supersedes.Int64 != newOp {
		t.Errorf("operations = %+v, want a third that supersedes OP-%d", snap.ops, newOp)
	}
}

// 戻すとき、同じ課題のほかの承認済み・承認待ちの release も置き換える（前の完了の承認で同時に承認された release を、
// 課題が着手中の間に実行させない）。終わっていない実行の試行がある release は置き換えない。
func TestReopenChallengeForRelease_SupersedesSiblingReleasesWithoutOpenAttempts(t *testing.T) {
	f := newRelFixture(t)
	const otherRef = "https://github.com/o/r/pull/9"
	const busyRef = "https://github.com/o/r/pull/10"
	sibling := coretest.InsertApprovedRelease(t, f.s.workspace, int(f.cid), otherRef, relHead, execTS(0))
	busy := coretest.InsertApprovedRelease(t, f.s.workspace, int(f.cid), busyRef, relHead, execTS(0))
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO operation_execution (operation_id, started_at, approved_head, pid, host) VALUES (?, ?, ?, 1, 'testhost')`, busy, execTS(1), relHead)
		return err
	})

	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: formatOperationID(f.opID), Reason: ReopenReasonNotMergeable})
	if err != nil || !res.Reopened {
		t.Fatalf("res = %+v, err = %v, want reopened", res, err)
	}
	snap := snapshotReopen(t, f.s, f.cid)
	byID := map[int64]reopenOp{}
	var copies []reopenOp
	for _, o := range snap.ops {
		byID[o.id] = o
		if o.supersedes.Valid {
			copies = append(copies, o)
		}
	}
	if o := byID[sibling]; o.state != string(OperationStateClosed) || o.closed.String != string(ClosedReasonSuperseded) {
		t.Errorf("sibling release = %+v, want closed/superseded", o)
	}
	if o := byID[busy]; o.state != string(OperationStateApproved) {
		t.Errorf("release with an open attempt = %+v, want left approved", o)
	}
	var siblingCopy bool
	for _, c := range copies {
		if c.supersedes.Int64 == sibling && c.state == string(OperationStatePending) && c.ref == otherRef {
			siblingCopy = true
		}
		if c.supersedes.Int64 == busy {
			t.Errorf("release with an open attempt must not be copied: %+v", c)
		}
	}
	if !siblingCopy || len(copies) != 2 {
		t.Errorf("copies = %+v, want a pending copy of the primary and of the sibling", copies)
	}
}
