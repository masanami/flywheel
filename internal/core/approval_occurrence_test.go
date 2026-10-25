package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 承認・差し戻しの発生（S3。AC-198〜AC-200・AC-215〜AC-217・AC-219〜AC-221）。

type approvalOccRow struct {
	kind, outcome string
	approvalID    sql.NullInt64
	cycleID       sql.NullInt64
	source        string
}

func readApprovalOccs(t *testing.T, s *Store) []approvalOccRow {
	t.Helper()
	var out []approvalOccRow
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT kind, outcome, approval_id, cycle_id, source FROM occurrence ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r approvalOccRow
			if err := rows.Scan(&r.kind, &r.outcome, &r.approvalID, &r.cycleID, &r.source); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func approvalKindOf(t *testing.T, s *Store, id int64) string {
	t.Helper()
	var k string
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT kind FROM approval WHERE id = ?`, id).Scan(&k)
	}); err != nil {
		t.Fatal(err)
	}
	return k
}

func approvalDecisionOf(t *testing.T, s *Store, id int64) string {
	t.Helper()
	var d string
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT decision FROM approval WHERE id = ?`, id).Scan(&d)
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

func countApprovalRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM approval`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedPlanApproval は課題に計画の承認の行を 1 件足す（区切りの基準）。
func seedPlanApproval(t *testing.T, s *Store, cid int64) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, plan_version, actor, channel, verification, decided_at)
			VALUES (?, 'plan', 'approved', 1, 1, 'alice', 'cli', 'tty_confirm', '2026-01-01T00:00:00.000Z')`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func seedOccurrence(t *testing.T, s *Store, cid int64, kind OccurrenceKind, createdAt string) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		var aid int64
		if err := tx.QueryRow(`SELECT MAX(id) FROM approval`).Scan(&aid); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO occurrence (challenge_id, kind, approval_id, outcome, source, created_at)
			VALUES (?, ?, ?, 'bad', 'core', ?)`, cid, string(kind), aid, createdAt)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

type occAttKind struct {
	name string
	make func(t *testing.T, id string) Attestation
	ui   bool
}

var occAtts = []occAttKind{
	{"tty", verifiedAttestationForTest, false},
	{"relayed", func(t *testing.T, id string) Attestation { return relayedAttestation(t, id, "sess", "quote") }, false},
	{"ui", uiAttestationForTest, true},
}

// approvalReqFor は経路ごとの要求を作る。UI の承認は完了の保留つき承認（T14。release を承認しない）だけ通る。
func approvalReqFor(prev *ApprovalPreview, c *Challenge, dec ApprovalDecision, att Attestation) ApprovalRequest {
	req := ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: dec}
	switch {
	case dec == ApprovalDecisionRejected:
		req.Reason = strPtr("no")
	case att.Channel() == ChannelUI && prev.Kind == ApprovalKindCompletion:
		req.HoldRelease = true
	default:
		req.ReleaseHeads = headsForAll(prev)
	}
	return req
}

func runApproval(t *testing.T, s *Store, c *Challenge, dec ApprovalDecision, att Attestation) (*Approval, error) {
	t.Helper()
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	req := approvalReqFor(prev, c, dec, att)
	_, a, err := s.ExecuteApproval(context.Background(), req, att)
	return a, err
}

func TestExecuteApproval_RecordsOccurrencePerRoute(t *testing.T) {
	cases := []struct {
		name     string
		status   Status
		dec      ApprovalDecision
		wantKind OccurrenceKind
		wantOut  string
		wantRef  string
	}{
		{"plan_rejected", StatusAwaitingPlanApproval, ApprovalDecisionRejected, OccurrenceKindPlanRejected, "bad", "plan"},
		{"completion_rejected", StatusAwaitingCompletionApproval, ApprovalDecisionRejected, OccurrenceKindCompletionRejected, "bad", "completion"},
		{"completion_approved", StatusAwaitingCompletionApproval, ApprovalDecisionApproved, OccurrenceKindCompletionApproved, "good", "completion"},
	}
	for _, ac := range occAtts {
		for _, tc := range cases {
			if ac.ui && tc.dec == ApprovalDecisionRejected {
				continue // UI に差し戻しの経路は無い
			}
			t.Run(ac.name+"/"+tc.name, func(t *testing.T) {
				s := newStoreForTest(t)
				fixedActor(t, "alice")
				c := createAndAdvance(t, s, tc.status)
				cid, _ := parseChallengeID(c.ID)
				insertPlanRowForTest(t, s, c.ID, 1, "x")
				seedPlanApproval(t, s, cid)
				if _, err := runApproval(t, s, c, tc.dec, ac.make(t, c.ID)); err != nil {
					t.Fatalf("ExecuteApproval() error = %v", err)
				}
				occs := readApprovalOccs(t, s)
				if len(occs) != 1 {
					t.Fatalf("occurrences = %+v, want exactly 1", occs)
				}
				o := occs[0]
				if o.kind != string(tc.wantKind) || o.outcome != tc.wantOut || o.source != "core" {
					t.Errorf("occurrence = %+v, want kind=%s outcome=%s", o, tc.wantKind, tc.wantOut)
				}
				if !o.approvalID.Valid || approvalKindOf(t, s, o.approvalID.Int64) != tc.wantRef {
					t.Errorf("ref approval kind = %v, want %s", o.approvalID, tc.wantRef)
				}
				if dec := approvalDecisionOf(t, s, o.approvalID.Int64); dec != string(tc.dec) {
					t.Errorf("ref approval decision = %q, want %q (the row just written)", dec, tc.dec)
				}
				if o.cycleID.Valid {
					t.Errorf("cycle_id = %v, want NULL (AC-217)", o.cycleID)
				}
			})
		}
	}
}

func TestExecuteApproval_CompletionRejectionIsDecidedByKindNotRejectedFields(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected,
		Reason: strPtr("no"), RejectedFields: []string{string(RejectedFieldIssues)},
	}, verifiedAttestationForTest(t, c.ID))
	if err != nil {
		t.Fatalf("ExecuteApproval() error = %v", err)
	}
	occs := readApprovalOccs(t, s)
	if len(occs) != 1 || occs[0].kind != string(OccurrenceKindPlanRejected) {
		t.Fatalf("occurrences = %+v, want one plan_rejected", occs)
	}
}

func TestExecuteApproval_PlanApprovalMakesNoOccurrence(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	if _, err := runApproval(t, s, c, ApprovalDecisionApproved, verifiedAttestationForTest(t, c.ID)); err != nil {
		t.Fatal(err)
	}
	if occs := readApprovalOccs(t, s); len(occs) != 0 {
		t.Fatalf("occurrences = %+v, want none", occs)
	}
}

// AC-219・AC-220・AC-221
func TestExecuteApproval_CompletionApprovedOutcomeRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, s *Store, cid int64)
		want string
	}{
		{"no plan approval at all and no bad events", func(*testing.T, *Store, int64) {}, "good"},
		{"clean since the latest plan approval", func(t *testing.T, s *Store, cid int64) { seedPlanApproval(t, s, cid) }, "good"},
		{"completion_rejected after the plan approval", func(t *testing.T, s *Store, cid int64) {
			seedPlanApproval(t, s, cid)
			seedRejection(t, s, cid)
		}, "bad"},
		{"completion_rejected before the latest plan approval", func(t *testing.T, s *Store, cid int64) {
			seedPlanApproval(t, s, cid)
			seedRejection(t, s, cid)
			seedPlanApproval(t, s, cid)
		}, "good"},
		{"verification_not_met after the plan approval", func(t *testing.T, s *Store, cid int64) {
			seedPlanApproval(t, s, cid)
			seedOccurrence(t, s, cid, OccurrenceKindVerificationNotMet, "2999-01-01T00:00:00.000Z")
		}, "bad"},
		{"verification_not_met before the latest plan approval", func(t *testing.T, s *Store, cid int64) {
			seedPlanApproval(t, s, cid)
			seedOccurrence(t, s, cid, OccurrenceKindVerificationNotMet, "2000-01-01T00:00:00.000Z")
		}, "good"},
		{"plan_rejected then re-approved plan stays good", func(t *testing.T, s *Store, cid int64) {
			seedPlanApproval(t, s, cid)
			seedOccurrence(t, s, cid, OccurrenceKindPlanRejected, "2999-01-01T00:00:00.000Z")
			seedPlanApproval(t, s, cid)
		}, "good"},
		{"another challenge's bad events do not count", func(t *testing.T, s *Store, cid int64) {
			seedPlanApproval(t, s, cid)
			other := createAndAdvance(t, s, StatusAwaitingCompletionApproval)
			ocid, _ := parseChallengeID(other.ID)
			seedRejection(t, s, ocid)
			seedOccurrence(t, s, ocid, OccurrenceKindVerificationNotMet, "2999-01-01T00:00:00.000Z")
		}, "good"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := createAndAdvance(t, s, StatusAwaitingCompletionApproval)
			cid, _ := parseChallengeID(c.ID)
			tc.seed(t, s, cid)
			before := len(readApprovalOccs(t, s))
			if _, err := runApproval(t, s, c, ApprovalDecisionApproved, verifiedAttestationForTest(t, c.ID)); err != nil {
				t.Fatal(err)
			}
			occs := readApprovalOccs(t, s)
			if len(occs) != before+1 {
				t.Fatalf("occurrences = %+v, want %d rows", occs, before+1)
			}
			last := occs[len(occs)-1]
			if last.kind != string(OccurrenceKindCompletionApproved) || last.outcome != tc.want {
				t.Fatalf("occurrences = %+v, want a new completion_approved with %s", occs, tc.want)
			}
			// 既存の bad の行は書き換えない。
			for _, o := range occs[:before] {
				if o.outcome != "bad" {
					t.Errorf("earlier row changed: %+v", o)
				}
			}
		})
	}
}

// seedRejection は完了の差し戻しの承認の行と completion_rejected の発生（bad）を足す。
func seedRejection(t *testing.T, s *Store, cid int64) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
			VALUES (?, 'completion', 'rejected', 1, 'alice', 'cli', 'tty_confirm', 'no', '2026-01-02T00:00:00.000Z')`, cid)
		if err != nil {
			return err
		}
		aid, _ := res.LastInsertId()
		_, err = tx.Exec(`INSERT INTO occurrence (challenge_id, kind, approval_id, outcome, source, created_at)
			VALUES (?, 'completion_rejected', ?, 'bad', 'core', '2026-01-02T00:00:00.000Z')`, cid, aid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-215
func TestExecuteApproval_ConflictLeavesNoOccurrence(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingCompletionApproval)
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version + 7, Decision: ApprovalDecisionApproved,
		ReleaseHeads: headsForAll(prev),
	}, verifiedAttestationForTest(t, c.ID))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if n := len(readApprovalOccs(t, s)); n != 0 {
		t.Fatalf("occurrences = %d, want 0", n)
	}
}

// AC-216: 発生の挿入の失敗で承認の行・遷移・作業ログが残らない。
func TestExecuteApproval_OccurrenceInsertFailureRollsEverythingBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status Status
		dec    ApprovalDecision
		att    occAttKind
	}{
		{"plan_rejected/tty", StatusAwaitingPlanApproval, ApprovalDecisionRejected, occAtts[0]},
		{"plan_rejected/relayed", StatusAwaitingPlanApproval, ApprovalDecisionRejected, occAtts[1]},
		{"completion_rejected/tty", StatusAwaitingCompletionApproval, ApprovalDecisionRejected, occAtts[0]},
		{"completion_rejected/relayed", StatusAwaitingCompletionApproval, ApprovalDecisionRejected, occAtts[1]},
		{"completion_approved/tty", StatusAwaitingCompletionApproval, ApprovalDecisionApproved, occAtts[0]},
		{"completion_approved/relayed", StatusAwaitingCompletionApproval, ApprovalDecisionApproved, occAtts[1]},
		{"completion_approved/ui", StatusAwaitingCompletionApproval, ApprovalDecisionApproved, occAtts[2]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := createAndAdvance(t, s, tc.status)
			insertPlanRowForTest(t, s, c.ID, 1, "x")
			prev, err := s.PrepareApproval(context.Background(), c.ID)
			if err != nil {
				t.Fatal(err)
			}
			actBefore := countActivityRows(t, s)
			s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return errors.New("boom: injected occurrence failure") }
			att := tc.att.make(t, c.ID)
			req := approvalReqFor(prev, c, tc.dec, att)
			if _, _, err := s.ExecuteApproval(context.Background(), req, att); err == nil || !strings.Contains(err.Error(), "injected occurrence failure") {
				t.Fatalf("ExecuteApproval() error = %v, want the injected failure", err)
			}
			s.insertOccurrence = nil
			after, err := s.GetChallenge(context.Background(), c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Version != prev.Version || after.Status != tc.status || len(after.Approvals) != 0 {
				t.Fatalf("not rolled back: %+v", after)
			}
			if n := countApprovalRows(t, s); n != 0 {
				t.Errorf("approval rows = %d, want 0", n)
			}
			if n := countActivityRows(t, s); n != actBefore {
				t.Errorf("activity rows = %d, want %d", n, actBefore)
			}
		})
	}
}

// AC-216 の逆向き: coretest の DB トリガで挿入を失敗させても同じ。
func TestExecuteApproval_TriggerFailureRollsBackApproval(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = s.Close()
		}
	})
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	closed = true
	coretest.FailOccurrenceInserts(t, dir)
	s, err = OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: strPtr("no"),
	}, verifiedAttestationForTest(t, c.ID))
	if err == nil || !strings.Contains(err.Error(), "occurrence insert failed") {
		t.Fatalf("err = %v, want the trigger failure", err)
	}
	if n := countApprovalRows(t, s); n != 0 {
		t.Errorf("approval rows = %d, want 0", n)
	}
	after, gerr := s.GetChallenge(context.Background(), c.ID)
	if gerr != nil || after.Version != prev.Version || after.Status != StatusAwaitingPlanApproval {
		t.Errorf("challenge changed: %+v (err %v)", after, gerr)
	}
}

// 完了の承認で release が自動承認されるときも、発生の参照は kind=completion の行で、
// 発生の挿入の失敗では release の承認も含めてすべて残らない。
func TestExecuteApproval_CompletionWithReleasesReferencesCompletionRow(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, releases, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)
	if _, err := runApproval(t, s, c, ApprovalDecisionApproved, verifiedAttestationForTest(t, c.ID)); err != nil {
		t.Fatal(err)
	}
	var nRelease int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM approval WHERE kind = 'release'`).Scan(&nRelease)
	}); err != nil {
		t.Fatal(err)
	}
	if nRelease != len(releases) {
		t.Fatalf("release approval rows = %d, want %d", nRelease, len(releases))
	}
	occs := readApprovalOccs(t, s)
	if len(occs) != 1 || !occs[0].approvalID.Valid || approvalKindOf(t, s, occs[0].approvalID.Int64) != "completion" {
		t.Fatalf("occurrences = %+v, want one referencing the completion row", occs)
	}
}

func TestExecuteApproval_OccurrenceFailureAlsoRollsBackReleaseApprovals(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, _, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)
	s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return errors.New("boom: injected occurrence failure") }
	if _, err := runApproval(t, s, c, ApprovalDecisionApproved, verifiedAttestationForTest(t, c.ID)); err == nil {
		t.Fatal("want error")
	}
	s.insertOccurrence = nil
	if n := countApprovalRows(t, s); n != 0 {
		t.Errorf("approval rows = %d, want 0 (release approvals included)", n)
	}
}

// AC-215: 発生の挿入より後の書き込み（作業ログ）を DB のトリガで失敗させると、差し戻しの承認の行・
// 遷移とともに、すでに挿入した発生の行も残らない（同じトランザクション）。
func TestExecuteApproval_LateWriteFailureLeavesNoOccurrence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status Status
	}{
		{"plan_rejected", StatusAwaitingPlanApproval},
		{"completion_rejected", StatusAwaitingCompletionApproval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := createAndAdvance(t, s, tc.status)
			insertPlanRowForTest(t, s, c.ID, 1, "x")
			prev, err := s.PrepareApproval(context.Background(), c.ID)
			if err != nil {
				t.Fatal(err)
			}
			// 発生の挿入の後に走る書き込み（作業ログ）を、本物の DB のトリガで失敗させる。
			if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`CREATE TRIGGER fail_late_activity BEFORE INSERT ON activity
					WHEN NEW.action IN ('reject', 'approve') BEGIN SELECT RAISE(ABORT, 'late write failed'); END`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			att := verifiedAttestationForTest(t, c.ID)
			_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
				ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: strPtr("no"),
			}, att)
			if err == nil || !strings.Contains(err.Error(), "late write failed") {
				t.Fatalf("err = %v, want the late write failure", err)
			}
			if n := len(readApprovalOccs(t, s)); n != 0 {
				t.Errorf("occurrences = %d, want 0", n)
			}
			if n := countApprovalRows(t, s); n != 0 {
				t.Errorf("approval rows = %d, want 0", n)
			}
			after, gerr := s.GetChallenge(context.Background(), c.ID)
			if gerr != nil || after.Version != prev.Version || after.Status != tc.status {
				t.Errorf("challenge changed: %+v (err %v)", after, gerr)
			}
		})
	}
}

// AC-215: 先に別の差し戻しが成立していて版が古い 2 回目の差し戻しは conflict で、発生は 1 行のまま増えない。
func TestExecuteApproval_StaleRejectionConflictDoesNotAddOccurrence(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: strPtr("no")}
	if _, _, err := s.ExecuteApproval(context.Background(), req, verifiedAttestationForTest(t, c.ID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ExecuteApproval(context.Background(), req, verifiedAttestationForTest(t, c.ID)); !errors.Is(err, ErrConflict) {
		t.Fatalf("2nd err = %v, want ErrConflict", err)
	}
	if n := len(readApprovalOccs(t, s)); n != 1 {
		t.Errorf("occurrences = %d, want 1", n)
	}
}
