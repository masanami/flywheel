package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func relayedAttestation(t *testing.T, id, session, quote string) Attestation {
	t.Helper()
	att, err := Verify(ChannelCLI, &fakeVerifier{method: VerificationRelayed, actor: "alice", evidence: RelayEvidence{Session: session, Quote: quote}}, "s", id)
	if err != nil {
		t.Fatalf("Verify(relayed) error = %v", err)
	}
	return att
}

type relayCols struct {
	channel, verification, session, quote sql.NullString
}

func (s *Store) approvalRelayCols(t *testing.T, where string, args ...any) relayCols {
	t.Helper()
	var c relayCols
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT channel, verification, relay_session, relay_quote FROM approval WHERE `+where+` ORDER BY id DESC LIMIT 1`, args...).
			Scan(&c.channel, &c.verification, &c.session, &c.quote)
	}); err != nil {
		t.Fatalf("read approval: %v", err)
	}
	return c
}

func (s *Store) holdCols(t *testing.T) relayCols {
	t.Helper()
	var c relayCols
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT answered_channel, answered_verification, relay_session, relay_quote FROM hold ORDER BY id DESC LIMIT 1`).
			Scan(&c.channel, &c.verification, &c.session, &c.quote)
	}); err != nil {
		t.Fatalf("read hold: %v", err)
	}
	return c
}

func lastAfter(t *testing.T, s *Store, id string) map[string]any {
	t.Helper()
	acts := activitiesFor(t, s, id)
	var after map[string]any
	if err := json.Unmarshal(acts[len(acts)-1].After, &after); err != nil {
		t.Fatalf("unmarshal After: %v", err)
	}
	return after
}

func wantCols(t *testing.T, got relayCols, ch, ver string, session, quote *string) {
	t.Helper()
	if got.channel.String != ch || got.verification.String != ver {
		t.Errorf("channel/verification = %q/%q, want %q/%q", got.channel.String, got.verification.String, ch, ver)
	}
	for _, p := range []struct {
		name string
		got  sql.NullString
		want *string
	}{{"relay_session", got.session, session}, {"relay_quote", got.quote, quote}} {
		if p.want == nil {
			if p.got.Valid {
				t.Errorf("%s = %q, want NULL", p.name, p.got.String)
			}
		} else if !p.got.Valid || p.got.String != *p.want {
			t.Errorf("%s = %v, want %q", p.name, p.got, *p.want)
		}
	}
}

// AC-249: 手がかりの空な relayed は Confirm にも触れず verification_rejected。
func TestVerify_RelayedWithoutEvidenceIsRejectedWithoutConfirm(t *testing.T) {
	for _, ev := range []RelayEvidence{{}, {Session: "s"}, {Quote: "q"}, {Session: "  ", Quote: "q"}, {Session: "s", Quote: "\t"}} {
		fv := &fakeVerifier{method: VerificationRelayed, actor: "alice", evidence: ev}
		if _, err := Verify(ChannelCLI, fv, "s", "C-1"); !errors.Is(err, ErrVerificationRejected) {
			t.Errorf("evidence %+v: err = %v, want ErrVerificationRejected", ev, err)
		} else if !errors.Is(err, ErrRelayEvidenceMissing) || strings.Contains(err.Error(), "registry") {
			t.Errorf("evidence %+v: err = %q, want the empty-evidence message (not the registry one)", ev, err)
		}
		if fv.confirmCalls != 0 {
			t.Errorf("evidence %+v: Confirm called %d times, want 0", ev, fv.confirmCalls)
		}
	}
}

// (ui, relayed)・(invoker, relayed) は登録簿に無い。
func TestVerify_RelayedOnlyRegisteredForCLI(t *testing.T) {
	ev := RelayEvidence{Session: "s", Quote: "q"}
	for _, ch := range []Channel{ChannelUI, ChannelInvoker} {
		if _, err := Verify(ch, &fakeVerifier{method: VerificationRelayed, actor: "a", evidence: ev}, "s", "C-1"); !errors.Is(err, ErrVerificationRejected) {
			t.Errorf("channel %s: err = %v, want ErrVerificationRejected", ch, err)
		}
	}
	for _, act := range []attestedAction{actionApprovePlan, actionApproveCompletion, actionApproveCompletionHold, actionReject, actionAnswer, actionBudget, actionOperationApprove, actionOperationReject} {
		if !registryPermits(ChannelCLI, VerificationRelayed, act) {
			t.Errorf("(cli, relayed) does not permit %s", act)
		}
	}
}

// AC-251: relayed 以外の方式は、Verifier が手がかりを返しても Attestation に載せない。
func TestVerify_NonRelayedDropsEvidence(t *testing.T) {
	fv := &fakeVerifier{method: VerificationTTYConfirm, actor: "alice", evidence: RelayEvidence{Session: "s", Quote: "q"}}
	att, err := Verify(ChannelCLI, fv, "s", "C-1")
	if err != nil {
		t.Fatal(err)
	}
	if att.hasEvidence() {
		t.Error("tty_confirm attestation carries evidence")
	}
}

// AC-235/238/240: 計画の承認（relayed）。
func TestExecuteApproval_RelayedRecordsEvidenceInApprovalAndActivity(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved},
		relayedAttestation(t, c.ID, "sess-1", "「承認して」")); err != nil {
		t.Fatalf("ExecuteApproval() error = %v", err)
	}
	ss, qq := "sess-1", "「承認して」"
	wantCols(t, s.approvalRelayCols(t, "challenge_id = 1"), "cli", "relayed", &ss, &qq)
	after := lastAfter(t, s, c.ID)
	if after["relay_session"] != ss || after["relay_quote"] != qq || after["approval_kind"] != "plan" {
		t.Errorf("after = %+v, want relay_* at top level plus existing keys", after)
	}
}

func TestExecuteApproval_RelayedRejectionRecordsEvidence(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, _ := s.PrepareRejection(context.Background(), c.ID, "no")
	reason := "no"
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: &reason},
		relayedAttestation(t, c.ID, "s2", "q2")); err != nil {
		t.Fatalf("ExecuteApproval() error = %v", err)
	}
	ss, qq := "s2", "q2"
	wantCols(t, s.approvalRelayCols(t, "challenge_id = 1"), "cli", "relayed", &ss, &qq)
}

// AC-251: tty_confirm の記録の relay_* は NULL で、作業ログにも relay_* が無い。
func TestExecuteApproval_TTYConfirmLeavesRelayNullAndOutOfActivity(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, _ := s.PrepareApproval(context.Background(), c.ID)
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved},
		verifiedAttestationForTest(t, c.ID)); err != nil {
		t.Fatal(err)
	}
	wantCols(t, s.approvalRelayCols(t, "challenge_id = 1"), "cli", "tty_confirm", nil, nil)
	after := lastAfter(t, s, c.ID)
	if _, ok := after["relay_session"]; ok {
		t.Errorf("after has relay_session: %+v", after)
	}
	if _, ok := after["relay_quote"]; ok {
		t.Errorf("after has relay_quote: %+v", after)
	}
}

// AC-236/237/239/240: 回答の経路・方式と手がかり。
func TestExecuteAnswer_RecordsAnsweredChannelVerificationAndEvidence(t *testing.T) {
	answer := func(t *testing.T, att func(id string) Attestation) (*Store, string) {
		s := newStoreForTest(t)
		fixedActor(t, "alice")
		c := createAndAdvance(t, s, StatusUnclassified)
		if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "why?"}); err != nil {
			t.Fatal(err)
		}
		prev, err := s.PrepareAnswer(context.Background(), c.ID, "because")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Answer: "because"}, att(c.ID)); err != nil {
			t.Fatalf("ExecuteAnswer() error = %v", err)
		}
		return s, c.ID
	}

	t.Run("relayed", func(t *testing.T) {
		s, id := answer(t, func(id string) Attestation { return relayedAttestation(t, id, "sess", "quote") })
		ss, qq := "sess", "quote"
		wantCols(t, s.holdCols(t), "cli", "relayed", &ss, &qq)
		after := lastAfter(t, s, id)
		if after["relay_session"] != ss || after["relay_quote"] != qq || after["answer"] != "because" {
			t.Errorf("after = %+v", after)
		}
	})
	t.Run("tty_confirm", func(t *testing.T) {
		s, id := answer(t, func(id string) Attestation { return verifiedAttestationForTest(t, id) })
		wantCols(t, s.holdCols(t), "cli", "tty_confirm", nil, nil)
		if _, ok := lastAfter(t, s, id)["relay_session"]; ok {
			t.Error("after has relay_session for tty_confirm")
		}
	})
}

// AC-238/240: 不可逆操作の承認（relayed）と、完了の承認に伴う release の一括承認。
func TestExecuteOperationApproval_RelayedRecordsEvidence(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")
	prev, _ := s.PrepareOperationApproval(context.Background(), op.ID)
	if _, _, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved,
	}, relayedAttestation(t, op.ID, "so", "qo")); err != nil {
		t.Fatalf("ExecuteOperationApproval() error = %v", err)
	}
	ss, qq := "so", "qo"
	wantCols(t, s.approvalRelayCols(t, "operation_id IS NOT NULL"), "cli", "relayed", &ss, &qq)
	for _, a := range activitiesFor(t, s, c.ID)[1:] { // create の次以降（operation 追加後の承認系）
		if a.Action != "op_approve" && a.Action != "approve" {
			continue
		}
		var after map[string]any
		_ = json.Unmarshal(a.After, &after)
		if after["relay_session"] != ss {
			t.Errorf("activity %s/%s after = %+v, want relay_session", a.Entity, a.Action, after)
		}
	}
}

func TestExecuteApproval_RelayedCompletionApprovalRecordsEvidenceOnAutoApprovedRelease(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")
	cid, _ := parseChallengeID(c.ID)
	setChallengeStatus(t, s, cid, StatusAwaitingCompletionApproval)
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved},
		relayedAttestation(t, c.ID, "sc", "qc")); err != nil {
		t.Fatalf("ExecuteApproval() error = %v", err)
	}
	ss, qq := "sc", "qc"
	wantCols(t, s.approvalRelayCols(t, "operation_id IS NOT NULL"), "cli", "relayed", &ss, &qq)
	wantCols(t, s.approvalRelayCols(t, "operation_id IS NULL"), "cli", "relayed", &ss, &qq)
	_ = op
}

// AC-250: 予算の増額（relayed）。
func TestExecuteBudget_RelayedRecordsEvidence(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	p, err := f.s.PrepareBudget(context.Background(), id, f.agent, 80, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ExecuteBudget(context.Background(), BudgetRequest{ChallengeID: id, ExpectedVersion: p.Version, PlanVersion: p.PlanVersion, ImplUSD: 80},
		relayedAttestation(t, id, "sb", "qb")); err != nil {
		t.Fatalf("ExecuteBudget() error = %v", err)
	}
	ss, qq := "sb", "qb"
	wantCols(t, f.s.approvalRelayCols(t, "kind = 'budget'"), "cli", "relayed", &ss, &qq)
	if after := lastAfter(t, f.s, id); after["relay_session"] != ss || after["relay_quote"] != qq {
		t.Errorf("after = %+v", after)
	}
}

// AC-249: 手がかりの無い relayed の Attestation（パッケージ内でだけ組める）を
// Execute* に直接渡しても、何も書かず verification_rejected になる。
func TestExecute_RelayedAttestationWithoutEvidenceIsRejectedAndWritesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	forged := Attestation{actor: "alice", channel: ChannelCLI, verification: VerificationRelayed, target: c.ID}
	before := len(activitiesFor(t, s, c.ID))
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved}, forged); !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}
	if got := len(activitiesFor(t, s, c.ID)); got != before {
		t.Errorf("activities = %d, want %d (nothing written)", got, before)
	}
	var n int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM approval`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("approval rows = %d, want 0", n)
	}
}

// AC-252: GetChallenge（show --json の元）が承認・保留の会話の手がかりを返す。
// relayed は両方に値、tty_confirm・未回答は nil。
func TestGetChallenge_ExposesRelayEvidenceOfApprovalAndHold(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	if _, err := s.HoldChallenge(ctx, ChannelCLI, c.ID, HoldInput{Question: "why?"}); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetChallenge(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := d.Holds[0]
	if h.AnsweredChannel != nil || h.AnsweredVerification != nil || h.RelaySession != nil || h.RelayQuote != nil {
		t.Errorf("unanswered hold = %+v, want all new fields nil", h)
	}
	prev, err := s.PrepareAnswer(ctx, c.ID, "because")
	if err != nil {
		t.Fatal(err)
	}
	_, retHold, err := s.ExecuteAnswer(ctx, AnswerRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Answer: "because"}, relayedAttestation(t, c.ID, "sess", "quote"))
	if err != nil {
		t.Fatal(err)
	}
	if retHold.AnsweredChannel == nil || *retHold.AnsweredChannel != "cli" || retHold.AnsweredVerification == nil || *retHold.AnsweredVerification != "relayed" ||
		retHold.RelaySession == nil || *retHold.RelaySession != "sess" || retHold.RelayQuote == nil || *retHold.RelayQuote != "quote" {
		t.Errorf("returned hold = %+v, want cli/relayed/sess/quote", retHold)
	}
	d, err = s.GetChallenge(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	h = d.Holds[0]
	if h.AnsweredChannel == nil || *h.AnsweredChannel != "cli" || h.AnsweredVerification == nil || *h.AnsweredVerification != "relayed" ||
		h.RelaySession == nil || *h.RelaySession != "sess" || h.RelayQuote == nil || *h.RelayQuote != "quote" {
		t.Errorf("answered hold = %+v, want cli/relayed/sess/quote", h)
	}

	// 承認: relayed と tty_confirm の両方。
	c2 := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c2.ID, 1, "x")
	p2, err := s.PrepareApproval(ctx, c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, retA, err := s.ExecuteApproval(ctx, ApprovalRequest{ChallengeID: c2.ID, ExpectedVersion: p2.Version, Decision: ApprovalDecisionApproved}, relayedAttestation(t, c2.ID, "s9", "q9"))
	if err != nil {
		t.Fatal(err)
	}
	if retA.RelaySession == nil || *retA.RelaySession != "s9" || retA.RelayQuote == nil || *retA.RelayQuote != "q9" {
		t.Errorf("returned approval = %+v, want relay s9/q9", retA)
	}
	d2, err := s.GetChallenge(ctx, c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := d2.Approvals[0]
	if a.RelaySession == nil || *a.RelaySession != "s9" || a.RelayQuote == nil || *a.RelayQuote != "q9" || a.Verification != "relayed" {
		t.Errorf("relayed approval = %+v", a)
	}
	c3 := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c3.ID, 1, "x")
	p3, err := s.PrepareApproval(ctx, c3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ExecuteApproval(ctx, ApprovalRequest{ChallengeID: c3.ID, ExpectedVersion: p3.Version, Decision: ApprovalDecisionApproved}, verifiedAttestationForTest(t, c3.ID)); err != nil {
		t.Fatal(err)
	}
	d3, err := s.GetChallenge(ctx, c3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a := d3.Approvals[0]; a.RelaySession != nil || a.RelayQuote != nil {
		t.Errorf("tty approval = %+v, want nil relay fields", a)
	}
}

// AC-252: 端末の確認（tty_confirm）で回答した保留は、経路・方式が入り手がかりは nil。
// 返す Hold と GetChallenge（show --json の元）の両方で確かめる。
func TestExecuteAnswer_TTYConfirmReturnsChannelVerificationAndNilEvidence(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	if _, err := s.HoldChallenge(ctx, ChannelCLI, c.ID, HoldInput{Question: "why?"}); err != nil {
		t.Fatal(err)
	}
	prev, err := s.PrepareAnswer(ctx, c.ID, "because")
	if err != nil {
		t.Fatal(err)
	}
	_, ret, err := s.ExecuteAnswer(ctx, AnswerRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Answer: "because"}, verifiedAttestationForTest(t, c.ID))
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.GetChallenge(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]Hold{"returned": *ret, "show": d.Holds[0]} {
		if h.AnsweredChannel == nil || *h.AnsweredChannel != "cli" || h.AnsweredVerification == nil || *h.AnsweredVerification != "tty_confirm" {
			t.Errorf("%s hold channel/verification = %v/%v, want cli/tty_confirm", name, h.AnsweredChannel, h.AnsweredVerification)
		}
		if h.RelaySession != nil || h.RelayQuote != nil {
			t.Errorf("%s hold relay = %v/%v, want nil", name, h.RelaySession, h.RelayQuote)
		}
	}
}

// wantApprovalRelay は承認の会話の手がかりを確かめる（session・quote が "" なら nil を期待）。
func wantApprovalRelay(t *testing.T, name string, a Approval, verification, session, quote string) {
	t.Helper()
	if a.Verification != verification {
		t.Errorf("%s verification = %q, want %q", name, a.Verification, verification)
	}
	if session == "" {
		if a.RelaySession != nil || a.RelayQuote != nil {
			t.Errorf("%s relay = %v/%v, want nil", name, a.RelaySession, a.RelayQuote)
		}
		return
	}
	if a.RelaySession == nil || *a.RelaySession != session || a.RelayQuote == nil || *a.RelayQuote != quote {
		t.Errorf("%s relay = %v/%v, want %q/%q", name, a.RelaySession, a.RelayQuote, session, quote)
	}
}

// AC-252: 不可逆操作（release）の承認が返す Approval と GetChallenge の承認に手がかりが載る。
func TestExecuteOperationApproval_ReturnsRelayEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, verification, session, quote string
	}{{"relayed", "relayed", "so", "qo"}, {"tty_confirm", "tty_confirm", "", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := createAndAdvance(t, s, StatusUnclassified)
			op := createPendingOperation(t, s, c.ID, "release")
			prev, err := s.PrepareOperationApproval(ctx, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			att := verifiedAttestationForTest(t, op.ID)
			if tc.session != "" {
				att = relayedAttestation(t, op.ID, tc.session, tc.quote)
			}
			_, ret, err := s.ExecuteOperationApproval(ctx, OperationApprovalRequest{
				OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved,
			}, att)
			if err != nil {
				t.Fatalf("ExecuteOperationApproval() error = %v", err)
			}
			wantApprovalRelay(t, "returned", *ret, tc.verification, tc.session, tc.quote)
			d, err := s.GetChallenge(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Approvals) != 1 {
				t.Fatalf("approvals = %d, want 1", len(d.Approvals))
			}
			wantApprovalRelay(t, "show", d.Approvals[0], tc.verification, tc.session, tc.quote)
		})
	}
}

// AC-252: 予算の承認が返す Approval と GetChallenge の承認に手がかりが載る。
func TestExecuteBudget_ReturnsRelayEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, verification, session, quote string
	}{{"relayed", "relayed", "sb", "qb"}, {"tty_confirm", "tty_confirm", "", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newDelegateFixture(t)
			id := f.newInProgress(t, "t", "P1", budgetSpec(50, 30))
			p, err := f.s.PrepareBudget(ctx, id, f.agent, 80, nil)
			if err != nil {
				t.Fatal(err)
			}
			att := verifiedAttestationForTest(t, id)
			if tc.session != "" {
				att = relayedAttestation(t, id, tc.session, tc.quote)
			}
			ret, err := f.s.ExecuteBudget(ctx, BudgetRequest{ChallengeID: id, ExpectedVersion: p.Version, PlanVersion: p.PlanVersion, ImplUSD: 80}, att)
			if err != nil {
				t.Fatalf("ExecuteBudget() error = %v", err)
			}
			wantApprovalRelay(t, "returned", *ret, tc.verification, tc.session, tc.quote)
			d, err := f.s.GetChallenge(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var budget *Approval
			for i := range d.Approvals {
				if d.Approvals[i].Kind == ApprovalKindBudget {
					budget = &d.Approvals[i]
				}
			}
			if budget == nil {
				t.Fatalf("approvals = %+v, want a budget approval", d.Approvals)
			}
			wantApprovalRelay(t, "show", *budget, tc.verification, tc.session, tc.quote)
		})
	}
}
