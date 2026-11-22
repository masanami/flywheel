package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// docs/features/core-redesign-s4d.md S4d-1「欄から受ける承認・回答」（AC-107・109・112〜118・130・132〜139・142）。
// 実ストアで確かめる。会話・発言の投入は PostChatMessage などの core の API を使う。

const chatOwnerText = "反映してください。予算も上げて"

type chatFix struct {
	s    *Store
	turn *ChatTurn
}

// newChatFix は会話を 1 つ開き、オーナーの発言 1 つと、進行中の返事を作る（印の発言への返事が進行中の状態）。
func newChatFix(t *testing.T, s *Store) *chatFix {
	t.Helper()
	turn, err := s.PostChatMessage(context.Background(), chatOwnerText, nil)
	if err != nil {
		t.Fatalf("PostChatMessage: %v", err)
	}
	return &chatFix{s: s, turn: turn}
}

// mark はこの会話・この発言の印から経路 chat の Attestation を作る。
func (f *chatFix) mark(t *testing.T, id string) Attestation {
	t.Helper()
	return f.markOf(t, f.turn.EngineSession, strconv.FormatInt(f.turn.OwnerMessageID, 10), id)
}

func (f *chatFix) markOf(t *testing.T, session, message, id string) Attestation {
	t.Helper()
	att, err := Verify(ChannelChat, NewChatMarkVerifier(session, message), "s", id)
	if err != nil {
		t.Fatalf("Verify(chat, relayed) error = %v", err)
	}
	return att
}

func writeChatDecl(t *testing.T, s *Store, content string) {
	t.Helper()
	dir := filepath.Join(s.workspace, ".flywheel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func challengeVersion(t *testing.T, s *Store, id string) (version int, status Status) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var st string
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT version, status FROM challenge WHERE id = ?`, cid).Scan(&version, &st)
	}); err != nil {
		t.Fatal(err)
	}
	return version, Status(st)
}

func approveCompletion(t *testing.T, s *Store, id string, hold bool, att Attestation) (*Challenge, *Approval, error) {
	t.Helper()
	prev, err := s.PrepareApproval(context.Background(), id)
	if err != nil {
		t.Fatalf("PrepareApproval: %v", err)
	}
	req := ApprovalRequest{ChallengeID: id, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved, HoldRelease: hold}
	if !hold {
		req.ReleaseHeads = headsForAll(prev)
	}
	return s.ExecuteApproval(context.Background(), req, att)
}

func opState(t *testing.T, s *Store, opID string) string {
	t.Helper()
	oid, _ := parseOperationID(opID)
	st, _ := opStateReason(t, s, oid)
	return st
}

// AC-107・AC-109・AC-130: 既定の宣言で、反映の承認を含む完了の承認が成立し、記録の発言は印の発言の body、
// 会話の識別は印の会話の識別になる。
func TestChatApproval_CompletionWithReleaseRecordsTheOwnerMessageAndSession(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, releases, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)
	f := newChatFix(t, s)

	c2, approval, err := approveCompletion(t, s, c.ID, false, f.mark(t, c.ID))
	if err != nil {
		t.Fatalf("ExecuteApproval error = %v", err)
	}
	if c2.Status != StatusDone {
		t.Errorf("status = %q, want done", c2.Status)
	}
	for _, r := range releases {
		if got := opState(t, s, r.ID); got != "approved" {
			t.Errorf("release %s state = %q, want approved", r.ID, got)
		}
	}
	if approval.Channel != "chat" || approval.Verification != "relayed" {
		t.Errorf("approval channel/verification = %q/%q, want chat/relayed", approval.Channel, approval.Verification)
	}
	if approval.RelayQuote == nil || *approval.RelayQuote != chatOwnerText || approval.RelaySession == nil || *approval.RelaySession != f.turn.EngineSession {
		t.Errorf("returned relay = %v/%v, want %q/%q", approval.RelaySession, approval.RelayQuote, f.turn.EngineSession, chatOwnerText)
	}
	// 完了の承認と、release ごとの承認のすべての記録に、同じ文面・会話の識別が入る。
	rows := 0
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rs, err := tx.Query(`SELECT channel, verification, relay_session, relay_quote FROM approval`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var ch, v, sess, quote sql.NullString
			if err := rs.Scan(&ch, &v, &sess, &quote); err != nil {
				return err
			}
			rows++
			if ch.String != "chat" || v.String != "relayed" || sess.String != f.turn.EngineSession || quote.String != chatOwnerText {
				t.Errorf("approval row = %v/%v/%v/%v", ch, v, sess, quote)
			}
		}
		return rs.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if rows != 1+len(releases) {
		t.Errorf("approval rows = %d, want %d", rows, 1+len(releases))
	}
	after := lastAfter(t, s, c.ID)
	if after["relay_session"] != f.turn.EngineSession || after["relay_quote"] != chatOwnerText {
		t.Errorf("activity after = %v, want relay_session/relay_quote from the store", after)
	}
}

// rejectedAndUnchanged は err が断りで、課題の状態と版が変わっていない（AC-113）ことを確かめる。
func rejectedAndUnchanged(t *testing.T, s *Store, id string, err error, wantIs error, v0 int, st0 Status) {
	t.Helper()
	if !errors.Is(err, ErrVerificationRejected) || (wantIs != nil && !errors.Is(err, wantIs)) {
		t.Fatalf("error = %v, want ErrVerificationRejected (and %v)", err, wantIs)
	}
	if v, st := challengeVersion(t, s, id); v != v0 || st != st0 {
		t.Errorf("challenge = v%d/%s, want unchanged v%d/%s", v, st, v0, st0)
	}
	var n int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT COUNT(*) FROM approval`).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("approval rows = %d, want 0", n)
	}
}

// 印の検査（AC-112〜116）。断るときは課題の状態を変えない（AC-113）。
func TestChatApproval_MarkChecksRejectAndChangeNothing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		arrange func(t *testing.T, f *chatFix) Attestation
	}{
		{"AC-112 会話が閉じている", func(t *testing.T, f *chatFix) Attestation {
			if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE chat_session SET closed_at = '2026-11-22T00:00:00Z'`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return f.mark(t, "")
		}},
		{"AC-114 最後のオーナーの発言でない", func(t *testing.T, f *chatFix) Attestation {
			if err := f.s.FinishChatReply(context.Background(), f.turn.ReplyMessageID, ChatStateDone, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.PostChatMessage(context.Background(), "次の発言", nil); err != nil {
				t.Fatal(err)
			}
			return f.mark(t, "") // 返事は進行中だが、印は前の発言
		}},
		{"AC-115 返事が進行中でない", func(t *testing.T, f *chatFix) Attestation {
			if err := f.s.FinishChatReply(context.Background(), f.turn.ReplyMessageID, ChatStateDone, 0); err != nil {
				t.Fatal(err)
			}
			return f.mark(t, "")
		}},
		{"AC-116 発言の識別がストアに無い", func(t *testing.T, f *chatFix) Attestation {
			return f.markOf(t, f.turn.EngineSession, strconv.FormatInt(f.turn.OwnerMessageID+100, 10), "")
		}},
		{"発言の識別が数でない", func(t *testing.T, f *chatFix) Attestation {
			return f.markOf(t, f.turn.EngineSession, "abc", "")
		}},
		{"会話の識別が発言の会話と違う", func(t *testing.T, f *chatFix) Attestation {
			return f.markOf(t, "00000000-0000-4000-8000-000000000000", strconv.FormatInt(f.turn.OwnerMessageID, 10), "")
		}},
		{"印の発言がオーナーの発言でない（返事の行）", func(t *testing.T, f *chatFix) Attestation {
			return f.markOf(t, f.turn.EngineSession, strconv.FormatInt(f.turn.ReplyMessageID, 10), "")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c, _, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)
			f := newChatFix(t, s)
			att := tt.arrange(t, f)
			// 印の 2 値は空でないので Verify は通り、core の処理が断る。対象の ID は Verify の後に差し替えられないので作り直す。
			att.target = c.ID
			v0, st0 := challengeVersion(t, s, c.ID)
			_, _, err := approveCompletion(t, s, c.ID, false, att)
			rejectedAndUnchanged(t, s, c.ID, err, ErrChatMarkInvalid, v0, st0)
		})
	}
}

// AC-117: FLYWHEEL_CHAT_SESSION だけがあり FLYWHEEL_CHAT_MESSAGE が無い（その逆も）印は、Verify が断る。
func TestChatApproval_HalfMarkIsRejectedByVerify(t *testing.T) {
	t.Parallel()
	for _, m := range [][2]string{{"sess", ""}, {"", "1"}, {"", ""}, {"sess", "  "}} {
		_, err := Verify(ChannelChat, NewChatMarkVerifier(m[0], m[1]), "s", "C-1")
		if !errors.Is(err, ErrRelayEvidenceMissing) || !errors.Is(err, ErrVerificationRejected) {
			t.Errorf("Verify(%q, %q) error = %v, want ErrRelayEvidenceMissing", m[0], m[1], err)
		}
	}
}

// 印の Verifier は経路 chat だけに使え、chat の経路は印の Verifier だけを受ける（手がかりを偽れない）。
func TestChatApproval_MarkVerifierAndChannelArePairedOnly(t *testing.T) {
	t.Parallel()
	if _, err := Verify(ChannelCLI, NewChatMarkVerifier("s", "1"), "s", "C-1"); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("mark verifier on cli: error = %v, want ErrVerificationRejected", err)
	}
	fv := &fakeVerifier{method: VerificationRelayed, actor: "alice", evidence: RelayEvidence{Session: "s", Quote: "偽の発言"}}
	if _, err := Verify(ChannelChat, fv, "s", "C-1"); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("fake verifier on chat: error = %v, want ErrVerificationRejected", err)
	}
	if fv.confirmCalls != 0 {
		t.Error("Confirm was called for a rejected pair")
	}
}

// AC-118: 1 つの発言への返事が進行中の間に、承認のコマンドと予算の増額のコマンドを続けて実行すると、どちらも成立する。
func TestChatApproval_ApproveThenRaiseBudgetUnderOneMessage(t *testing.T) {
	t.Parallel()
	d := newDelegateFixture(t)
	s := d.s
	id := d.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	d.mustRun(t, nil)
	d.refreshInProgress(t, id)
	pc := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, pc.ID, 1, "x")
	f := newChatFix(t, s)

	pprev, err := s.PrepareApproval(context.Background(), pc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: pc.ID, ExpectedVersion: pprev.Version, Decision: ApprovalDecisionApproved}, f.mark(t, pc.ID)); err != nil {
		t.Fatalf("approve error = %v", err)
	}
	bprev, err := s.PrepareBudget(context.Background(), id, d.agent, 80, nil)
	if err != nil {
		t.Fatal(err)
	}
	ap, err := s.ExecuteBudget(context.Background(), BudgetRequest{ChallengeID: id, ExpectedVersion: bprev.Version, PlanVersion: bprev.PlanVersion, ImplUSD: 80}, f.mark(t, id))
	if err != nil {
		t.Fatalf("budget error = %v", err)
	}
	if ap.Channel != "chat" || ap.RelayQuote == nil || *ap.RelayQuote != chatOwnerText || ap.RelaySession == nil || *ap.RelaySession != f.turn.EngineSession {
		t.Errorf("budget approval = %+v, want chat with the owner message", ap)
	}
	if _, st := challengeVersion(t, s, pc.ID); st != StatusInProgress {
		t.Errorf("plan-approved challenge status = %q, want in_progress", st)
	}
}

// AC-132・AC-133・AC-137: chat_approvable_operations が空の一覧のとき、反映の承認を含む完了の承認は断られ、
// 反映を保留にした完了の承認は成立する。断りの文言はターミナルで行う旨。
func TestChatApproval_EmptyListRejectsCompletionWithReleaseButAcceptsHold(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	writeChatDecl(t, s, `{"chat_approvable_operations": []}`)
	c, releases, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)
	f := newChatFix(t, s)

	v0, st0 := challengeVersion(t, s, c.ID)
	_, _, err := approveCompletion(t, s, c.ID, false, f.mark(t, c.ID))
	rejectedAndUnchanged(t, s, c.ID, err, ErrChatNotApprovable, v0, st0)
	if !strings.Contains(err.Error(), "対話の欄からは承認できません。ターミナルで行ってください") {
		t.Errorf("error text = %q, want the terminal guidance", err.Error())
	}
	for _, r := range releases {
		if got := opState(t, s, r.ID); got != "pending" {
			t.Errorf("release %s state = %q, want pending", r.ID, got)
		}
	}

	c2, _, err := approveCompletion(t, s, c.ID, true, f.mark(t, c.ID))
	if err != nil {
		t.Fatalf("hold approval error = %v", err)
	}
	if c2.Status != StatusDone {
		t.Errorf("status = %q, want done", c2.Status)
	}
	for _, r := range releases {
		if got := opState(t, s, r.ID); got != "pending" {
			t.Errorf("release %s state = %q, want pending (held)", r.ID, got)
		}
	}
}

func approveOp(t *testing.T, s *Store, opID string, att Attestation) error {
	t.Helper()
	prev, err := s.PrepareOperationApproval(context.Background(), opID)
	if err != nil {
		t.Fatalf("PrepareOperationApproval: %v", err)
	}
	_, _, err = s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: opID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved, HeadSHA: "head1",
	}, att)
	return err
}

// AC-134〜136・AC-138・AC-139: 既定の宣言では、反映以外の種類の承認は断られ、差し戻しは種類によらず成立する。
// 宣言に足した種類の承認は成立する。
func TestChatApproval_OperationKindsFollowTheDeclaration(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"external_send", "delete", "other"} {
		t.Run("default rejects "+kind, func(t *testing.T) {
			t.Parallel()
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := createAndAdvance(t, s, StatusUnclassified)
			op := createPendingOperation(t, s, c.ID, kind)
			f := newChatFix(t, s)
			err := approveOp(t, s, op.ID, f.mark(t, op.ID))
			if !errors.Is(err, ErrChatNotApprovable) || !errors.Is(err, ErrVerificationRejected) {
				t.Fatalf("error = %v, want ErrChatNotApprovable", err)
			}
			if got := opState(t, s, op.ID); got != "pending" {
				t.Errorf("state = %q, want pending", got)
			}
		})
	}

	t.Run("AC-138 declared kind is accepted", func(t *testing.T) {
		t.Parallel()
		s := newStoreForTest(t)
		fixedActor(t, "alice")
		writeChatDecl(t, s, `{"chat_approvable_operations": ["release", "external_send"]}`)
		c := createAndAdvance(t, s, StatusUnclassified)
		op := createPendingOperation(t, s, c.ID, "external_send")
		f := newChatFix(t, s)
		if err := approveOp(t, s, op.ID, f.mark(t, op.ID)); err != nil {
			t.Fatalf("error = %v", err)
		}
		if got := opState(t, s, op.ID); got != "approved" {
			t.Errorf("state = %q, want approved", got)
		}
		if c := s.approvalRelayCols(t, "operation_id IS NOT NULL"); c.channel.String != "chat" || c.quote.String != chatOwnerText || c.session.String != f.turn.EngineSession {
			t.Errorf("approval cols = %+v", c)
		}
	})

	t.Run("default accepts release", func(t *testing.T) {
		t.Parallel()
		s := newStoreForTest(t)
		fixedActor(t, "alice")
		c := createAndAdvance(t, s, StatusUnclassified)
		op := createPendingOperation(t, s, c.ID, "release")
		f := newChatFix(t, s)
		if err := approveOp(t, s, op.ID, f.mark(t, op.ID)); err != nil {
			t.Fatalf("error = %v", err)
		}
	})

	for _, kind := range []string{"external_send", "delete", "other", "release"} {
		t.Run("AC-139 reject "+kind+" is accepted under the default", func(t *testing.T) {
			t.Parallel()
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := createAndAdvance(t, s, StatusUnclassified)
			op := createPendingOperation(t, s, c.ID, kind)
			f := newChatFix(t, s)
			prev, err := s.PrepareOperationApproval(context.Background(), op.ID)
			if err != nil {
				t.Fatal(err)
			}
			reason := "見送る"
			if _, _, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
				OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionRejected, Reason: &reason,
			}, f.mark(t, op.ID)); err != nil {
				t.Fatalf("reject error = %v", err)
			}
			if got := opState(t, s, op.ID); got != "rejected" {
				t.Errorf("state = %q, want rejected", got)
			}
		})
	}
}

// 宣言が読めないときは、経路 chat の絞りが要る承認を断る（既定へ倒して通さない）。
func TestChatApproval_InvalidDeclarationFailsClosed(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	writeChatDecl(t, s, `{"chat_approvable_operations": ["bogus"]}`)
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")
	f := newChatFix(t, s)
	if err := approveOp(t, s, op.ID, f.mark(t, op.ID)); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
	if got := opState(t, s, op.ID); got != "pending" {
		t.Errorf("state = %q, want pending", got)
	}
}

// 印の検査は承認以外の本人確認つきの操作（回答・取り下げ・フォローアップ）にも同じにかかる。
func TestChatApproval_MarkCheckAppliesToWithdrawAndFollowups(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	f := newChatFix(t, s)
	if err := s.FinishChatReply(context.Background(), f.turn.ReplyMessageID, ChatStateDone, 0); err != nil {
		t.Fatal(err)
	}
	prev, err := s.PrepareWithdraw(context.Background(), c.ID, "やめる")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Reason: "やめる"}, f.mark(t, c.ID))
	if !errors.Is(err, ErrChatMarkInvalid) {
		t.Fatalf("withdraw error = %v, want ErrChatMarkInvalid", err)
	}
	if _, st := challengeVersion(t, s, c.ID); st != StatusUnclassified {
		t.Errorf("status = %q, want unchanged", st)
	}
	if _, err := s.DropFollowups(context.Background(), []string{"F-1"}, "r", f.mark(t, "F-1")); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("drop followups error = %v, want ErrVerificationRejected", err)
	}
}

// AC-142: 画面のボタンの承認（経路 ui）で、差し戻しは断られるまま。
func TestChatApproval_UIRejectIsStillRefused(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, _, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	reason := "直して"
	v0, st0 := challengeVersion(t, s, c.ID)
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: &reason}, uiAttestationForTest(t, c.ID))
	rejectedAndUnchanged(t, s, c.ID, err, nil, v0, st0)
}

// 経路 chat の計画の問いへの答え（分類済へ戻る答えも記録だけの答えも）は、plan_answer に印の発言の body と
// 会話の識別を残す。
func TestChatApproval_PlanAnswerRecordsTheOwnerMessage(t *testing.T) {
	t.Parallel()
	for name, items := range map[string][]PlanAnswerItem{"returns": {choiceItem(1, 1)}, "record only": {choiceItem(1, 2)}} {
		f := newPAFixture(t, 2, true)
		cf := newChatFix(t, f.s)
		att := func(t *testing.T, id string) Attestation { return cf.mark(t, id) }
		if _, err := f.answer(t, att, items...); err != nil {
			t.Fatalf("%s: answer: %v", name, err)
		}
		got := f.answers(t)
		if len(got) != 1 || got[0].RelaySession != cf.turn.EngineSession || got[0].RelayQuote != chatOwnerText || got[0].Channel != ChannelChat {
			t.Errorf("%s: row = %+v, want chat with the owner message", name, got)
		}
	}
}
