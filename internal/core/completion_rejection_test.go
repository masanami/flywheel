package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// このファイルは、完了確認での差し戻しの理由（approval の kind=completion・decision=rejected の
// reason）を、次の J3・委譲の再開・J5 へ渡すための収集と整形（#457）を検証する。

// rejectCompletionForTest は id を完了確認待ちにして、at の時刻に reason で差し戻す（T15。着手中へ戻る）。
func (f *delegateFixture) rejectCompletionForTest(t *testing.T, id, reason string, at time.Time, att func(t *testing.T, id string) Attestation) {
	t.Helper()
	ctx := context.Background()
	f.setStatus(t, id, string(StatusAwaitingCompletionApproval))
	f.s.now = func() time.Time { return at }
	t.Cleanup(func() { f.s.now = nil })
	prev, err := f.s.PrepareRejection(ctx, id, reason)
	if err != nil {
		t.Fatalf("PrepareRejection: %v", err)
	}
	if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{ChallengeID: id, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionRejected, Reason: &reason}, att(t, id)); err != nil {
		t.Fatalf("ExecuteApproval(reject): %v", err)
	}
	if got := f.detail(t, id).Status; got != StatusInProgress {
		t.Fatalf("status after the rejection = %s, want in_progress", got)
	}
}

func ttyAtt(t *testing.T, id string) Attestation { return verifiedAttestationForTest(t, id) }

func relayedAtt(t *testing.T, id string) Attestation {
	return relayedAttestation(t, id, "sess-1", "QUOTE-NOT-THE-REASON")
}

func (f *delegateFixture) completionRejections(t *testing.T, id string) []CompletionRejection {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var out []CompletionRejection
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = loadCompletionRejections(context.Background(), tx, cid)
		return err
	})
	if err != nil {
		t.Fatalf("loadCompletionRejections: %v", err)
	}
	return out
}

func rejDay(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 9, 30, 0, 0, time.UTC) }

func TestLoadCompletionRejections_NoRejectionReturnsNothing(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	if got := f.completionRejections(t, id); len(got) != 0 {
		t.Fatalf("rejections = %+v, want none", got)
	}
}

func TestLoadCompletionRejections_OneRejectionCarriesReasonAndDate(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.rejectCompletionForTest(t, id, "REASON-A", rejDay(2026, 11, 4), ttyAtt)
	got := f.completionRejections(t, id)
	if len(got) != 1 || got[0].Reason != "REASON-A" || got[0].DecidedAt.Format("2006-01-02") != "2026-11-04" {
		t.Fatalf("rejections = %+v, want one REASON-A on 2026-11-04", got)
	}
}

func TestLoadCompletionRejections_TwoOrMoreAreOldestFirstWithTheirDates(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.rejectCompletionForTest(t, id, "REASON-FIRST", rejDay(2026, 11, 4), ttyAtt)
	f.rejectCompletionForTest(t, id, "REASON-SECOND", rejDay(2026, 11, 6), ttyAtt)
	f.rejectCompletionForTest(t, id, "REASON-THIRD", rejDay(2026, 11, 7), ttyAtt)
	got := f.completionRejections(t, id)
	want := []struct{ reason, date string }{{"REASON-FIRST", "2026-11-04"}, {"REASON-SECOND", "2026-11-06"}, {"REASON-THIRD", "2026-11-07"}}
	if len(got) != len(want) {
		t.Fatalf("rejections = %+v, want %d", got, len(want))
	}
	for i, w := range want {
		if got[i].Reason != w.reason || got[i].DecidedAt.Format("2006-01-02") != w.date {
			t.Errorf("rejections[%d] = %+v, want %s on %s", i, got[i], w.reason, w.date)
		}
	}
}

// `--relayed` で記録した差し戻しでも、理由は reason の列にあり（relay_quote は会話の手がかりで、理由ではない）、収集できる。
func TestLoadCompletionRejections_RelayedRejectionUsesTheReasonNotTheQuote(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.rejectCompletionForTest(t, id, "REASON-RELAYED", rejDay(2026, 11, 4), relayedAtt)
	got := f.completionRejections(t, id)
	if len(got) != 1 || got[0].Reason != "REASON-RELAYED" {
		t.Fatalf("rejections = %+v, want the reason (not the quote)", got)
	}
}

// 計画承認での差し戻しや、完了の承認は含めない。他の課題の差し戻しも含めない。
func TestLoadCompletionRejections_OnlyCompletionRejectionsOfThisChallenge(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	other := f.newInProgress(t, "other", "P1", planSpec(nil))
	cid, _ := parseChallengeID(id)
	ocid, _ := parseChallengeID(other)
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		for _, r := range []struct {
			cid                    int64
			kind, decision, reason string
		}{
			{cid, "plan", "rejected", "PLAN-REJECTION"},
			{cid, "completion", "approved", "APPROVED-WITH-REASON"},
			{ocid, "completion", "rejected", "OTHER-CHALLENGE"},
		} {
			var reason any
			if r.reason != "" {
				reason = r.reason
			}
			if _, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
				VALUES (?, ?, ?, 1, 'alice', 'cli', 'tty_confirm', ?, '2026-11-01T00:00:00.000Z')`, r.cid, r.kind, r.decision, reason); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.rejectCompletionForTest(t, id, "REASON-REAL", rejDay(2026, 11, 4), ttyAtt)
	got := f.completionRejections(t, id)
	if len(got) != 1 || got[0].Reason != "REASON-REAL" {
		t.Fatalf("rejections = %+v, want only the completion rejection of this challenge", got)
	}
}

func TestFormatCompletionRejectionsSection_NoneIsEmpty(t *testing.T) {
	if got := formatCompletionRejectionsSection(nil); got != "" {
		t.Fatalf("section = %q, want empty (no section for a challenge without rejections)", got)
	}
}

func TestFormatCompletionRejectionsSection_PinsTheText(t *testing.T) {
	one := []CompletionRejection{{DecidedAt: rejDay(2026, 11, 4), Reason: "REASON-A\nline two"}}
	wantOne := "人間が成果物を確認して差し戻した理由。元の達成条件に足した、満たすべき条件である。古い順に並べている。" +
		"後の差し戻しが前の差し戻しの方針を置き換えているときは、後のものに従う。\n\n" +
		"1. 2026-11-04\nREASON-A\nline two"
	if got := formatCompletionRejectionsSection(one); got != wantOne {
		t.Errorf("one rejection:\n got: %q\nwant: %q", got, wantOne)
	}
	two := append(one, CompletionRejection{DecidedAt: rejDay(2026, 11, 6), Reason: "REASON-B"})
	wantTwo := wantOne + "\n\n2. 2026-11-06\nREASON-B"
	if got := formatCompletionRejectionsSection(two); got != wantTwo {
		t.Errorf("two rejections:\n got: %q\nwant: %q", got, wantTwo)
	}
}

// --- J3 の入力（#457 手順 3） ---

func TestRunDelegation_J3InputHasTheRejectionSectionWithDatesAndReasons(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.rejectCompletionForTest(t, id, "REASON-FIRST", rejDay(2026, 11, 4), ttyAtt)
	f.rejectCompletionForTest(t, id, "REASON-SECOND", rejDay(2026, 11, 6), relayedAtt)
	f.runDelegation(t, id)
	sec, ok := sectionByLabel(f.j3Inputs[0].Sections, "人間の差し戻し")
	if !ok {
		t.Fatalf("J3 input lacks the 人間の差し戻し section:\n%s", sectionsText(f.j3Inputs[0].Sections))
	}
	want := formatCompletionRejectionsSection([]CompletionRejection{
		{DecidedAt: rejDay(2026, 11, 4), Reason: "REASON-FIRST"}, {DecidedAt: rejDay(2026, 11, 6), Reason: "REASON-SECOND"}})
	if sec.Content != want {
		t.Errorf("section content:\n got: %q\nwant: %q", sec.Content, want)
	}
}

func TestRunDelegation_J3InputHasNoRejectionSectionWithoutARejection(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.runDelegation(t, id)
	if _, ok := sectionByLabel(f.j3Inputs[0].Sections, "人間の差し戻し"); ok {
		t.Fatalf("J3 input has a 人間の差し戻し section for a challenge without a rejection:\n%s", sectionsText(f.j3Inputs[0].Sections))
	}
}

// --- 委譲の起動の入力（#457 手順 3・4） ---

// 差し戻しの直後の委譲は再開にならず、J3 を通る新しいセッションになる（最新の J5 は met のため）。
// その起動の入力に、差し戻しの理由が（J3 の出力に依らず）機械的に載る。
func TestRunDelegation_NewSessionAfterARejectionCarriesTheRejectionText(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.rejectCompletionForTest(t, id, "REASON-FIRST", rejDay(2026, 11, 4), ttyAtt)
	f.rejectCompletionForTest(t, id, "REASON-SECOND", rejDay(2026, 11, 6), relayedAtt)
	f.runDelegation(t, id)
	got := f.lastLaunch(t)
	if got.IsResume || len(f.j3Inputs) != 1 {
		t.Fatalf("launch resume = %v, J3 calls = %d; want a new session through J3", got.IsResume, len(f.j3Inputs))
	}
	want := formatCompletionRejectionsSection([]CompletionRejection{
		{DecidedAt: rejDay(2026, 11, 4), Reason: "REASON-FIRST"}, {DecidedAt: rejDay(2026, 11, 6), Reason: "REASON-SECOND"}})
	if got.HumanRejections != want {
		t.Errorf("HumanRejections:\n got: %q\nwant: %q", got.HumanRejections, want)
	}
}

func TestRunDelegation_LaunchWithoutARejectionCarriesNoRejectionText(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.runDelegation(t, id)
	if got := f.lastLaunch(t); got.HumanRejections != "" {
		t.Errorf("HumanRejections = %q, want empty", got.HumanRejections)
	}
}

// 差し戻しのある課題の再開は、種類に依らず（回答・中断）同じ区画を持つ。
func TestResume_AfterARejectionAnswerAndInterruptedResumesCarryTheRejectionText(t *testing.T) {
	want := formatCompletionRejectionsSection([]CompletionRejection{
		{DecidedAt: rejDay(2026, 11, 4), Reason: "REASON-FIRST"}, {DecidedAt: rejDay(2026, 11, 6), Reason: "REASON-SECOND"}})
	setup := func(t *testing.T) (*delegateFixture, string) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.rejectCompletionForTest(t, id, "REASON-FIRST", rejDay(2026, 11, 4), ttyAtt)
		f.rejectCompletionForTest(t, id, "REASON-SECOND", rejDay(2026, 11, 6), relayedAtt)
		return f, id
	}
	t.Run("answer", func(t *testing.T) {
		f, id := setup(t)
		f.deleg.queue = []JudgmentLaunchOutput{questionsOut("feat/x")}
		f.runDelegation(t, id)
		f.answerLatestHold(t, id, "ANS")
		f.runDelegation(t, id)
		if got := f.lastLaunch(t); !got.IsResume || got.ResumeKind != ResumeKindAnswer || got.HumanRejections != want {
			t.Errorf("launch = resume %v kind %q rejections %q; want an answer resume with:\n%q", got.IsResume, got.ResumeKind, got.HumanRejections, want)
		}
	})
	t.Run("interrupted", func(t *testing.T) {
		f, id := setup(t)
		f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored)}
		f.runDelegation(t, id)
		f.runDelegation(t, id)
		if got := f.lastLaunch(t); !got.IsResume || got.ResumeKind != ResumeKindInterrupted || got.HumanRejections != want {
			t.Errorf("launch = resume %v kind %q rejections %q; want an interrupted resume with:\n%q", got.IsResume, got.ResumeKind, got.HumanRejections, want)
		}
	})
}

// --- J5 の入力（#457 手順 5） ---

// delegateAndVerifyMet は C-1 を委譲して検証中にし、J5 の met で完了確認待ちにする。
func (f *j5Fixture) delegateAndVerifyMet(t *testing.T, id string) {
	t.Helper()
	f.delegateToVerifying(t, id)
	f.j5.result = j5Out("met", nil, nil)
	if _, err := f.verify(t, &id); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, id).Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("status after J5 met = %s, want awaiting_completion_approval", got)
	}
}

func (f *j5Fixture) rejectAndRedelegate(t *testing.T, id, reason string, at time.Time, att func(*testing.T, string) Attestation) {
	t.Helper()
	f.rejectCompletionForTest(t, id, reason, at, att)
	f.runDelegation(t, id) // 新しいセッション（J3 を通る）。委譲の後に検証中へ進む
	if got := f.detail(t, id).Status; got != StatusVerifying {
		t.Fatalf("status after the redelegation = %s, want verifying", got)
	}
}

func TestVerifyAutoJ5_InputHasTheRejectionSectionOldestFirstAsConditionsToMeet(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateAndVerifyMet(t, "C-1")
	f.rejectAndRedelegate(t, "C-1", "REASON-FIRST", rejDay(2026, 11, 4), ttyAtt)
	f.rejectAndRedelegate(t, "C-1", "REASON-SECOND", rejDay(2026, 11, 6), relayedAtt)
	f.j5Inputs = nil
	f.j5.result = j5Out("met", nil, nil)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.j5Inputs) != 1 {
		t.Fatalf("J5 calls = %d, want 1", len(f.j5Inputs))
	}
	sec, ok := sectionByLabel(f.j5Inputs[0].Sections, "人間の差し戻し")
	if !ok {
		t.Fatalf("J5 input lacks the 人間の差し戻し section:\n%s", sectionsText(f.j5Inputs[0].Sections))
	}
	want := formatCompletionRejectionsSection([]CompletionRejection{
		{DecidedAt: rejDay(2026, 11, 4), Reason: "REASON-FIRST"}, {DecidedAt: rejDay(2026, 11, 6), Reason: "REASON-SECOND"}})
	if sec.Content != want {
		t.Errorf("section content:\n got: %q\nwant: %q", sec.Content, want)
	}
	for _, w := range []string{"2026-11-04", "REASON-FIRST", "2026-11-06", "REASON-SECOND", "満たすべき条件"} {
		if !strings.Contains(sec.Content, w) {
			t.Errorf("section lacks %q", w)
		}
	}
	if strings.Index(sec.Content, "REASON-FIRST") > strings.Index(sec.Content, "REASON-SECOND") {
		t.Error("the reasons are not oldest first")
	}
}

func TestVerifyAutoJ5_InputHasNoRejectionSectionWithoutARejection(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := sectionByLabel(f.j5Inputs[0].Sections, "人間の差し戻し"); ok {
		t.Fatalf("J5 input has a 人間の差し戻し section for a challenge without a rejection:\n%s", sectionsText(f.j5Inputs[0].Sections))
	}
}

// --- 通し（#457 手順 5・6） ---

// 2 回差し戻した課題で、2 回分の理由が古い順に J3 の入力・J5 の入力・差し戻しの再開の入力のすべてに入る。
// その 1 点を満たさない成果物で J5 が not_met と足りない点の指摘を返すと、その指摘が既存の経路で
// 次の委譲（再開）に渡る（J5 の出力はスタブ）。
func TestRejectionReasonsReachJ3J5AndTheReworkResumeInOrder_AndANotMetFeedbackReachesTheNextDelegation(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateAndVerifyMet(t, "C-1")
	f.rejectAndRedelegate(t, "C-1", "REASON-FIRST", rejDay(2026, 11, 4), ttyAtt)
	f.verifyMet(t, "C-1")
	f.rejectAndRedelegate(t, "C-1", "REASON-SECOND", rejDay(2026, 11, 6), relayedAtt)

	both := formatCompletionRejectionsSection([]CompletionRejection{
		{DecidedAt: rejDay(2026, 11, 4), Reason: "REASON-FIRST"}, {DecidedAt: rejDay(2026, 11, 6), Reason: "REASON-SECOND"}})
	assertOldestFirst := func(where, text string) {
		t.Helper()
		for _, w := range []string{"2026-11-04", "REASON-FIRST", "2026-11-06", "REASON-SECOND"} {
			if !strings.Contains(text, w) {
				t.Errorf("%s lacks %q:\n%s", where, w, text)
			}
		}
		if strings.Index(text, "REASON-FIRST") > strings.Index(text, "REASON-SECOND") {
			t.Errorf("%s: the reasons are not oldest first", where)
		}
	}

	// J3: 2 回目の差し戻しの後の起動（3 回目の J3）の入力に 2 回分が入る。
	lastJ3 := f.j3Inputs[len(f.j3Inputs)-1]
	sec, ok := sectionByLabel(lastJ3.Sections, "人間の差し戻し")
	if !ok || sec.Content != both {
		t.Fatalf("J3 input section = %q (found %v), want %q", sec.Content, ok, both)
	}
	assertOldestFirst("J3 input", sec.Content)
	// 新しいセッションの起動の入力にも載る（J3 の出力に依らない）。
	if got := f.lastLaunch(t); got.IsResume || got.HumanRejections != both {
		t.Fatalf("new-session launch = resume %v rejections %q, want a new session with %q", got.IsResume, got.HumanRejections, both)
	}

	// J5: 満たさない点があるので not_met と足りない点の指摘を返す（スタブ）。
	f.j5Inputs = nil
	f.j5.result = j5Out("not_met", "FEEDBACK-REASON-SECOND-NOT-MET", nil)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	j5Sec, ok := sectionByLabel(f.j5Inputs[0].Sections, "人間の差し戻し")
	if !ok || j5Sec.Content != both {
		t.Fatalf("J5 input section = %q (found %v), want %q", j5Sec.Content, ok, both)
	}
	assertOldestFirst("J5 input", j5Sec.Content)
	if got := f.detail(t, "C-1").Status; got != StatusInProgress {
		t.Fatalf("status after not_met = %s, want in_progress", got)
	}

	// 次の委譲: 差し戻しの再開。J5 の指摘と 2 回分の理由の両方が渡り、J3 は呼ばれない。
	j3Calls := len(f.j3Inputs)
	f.runDelegation(t, "C-1")
	got := f.lastLaunch(t)
	if !got.IsResume || got.ResumeKind != ResumeKindRework || got.ResumeFeedback != "FEEDBACK-REASON-SECOND-NOT-MET" {
		t.Fatalf("launch = resume %v kind %q feedback %q; want a rework resume carrying the J5 feedback", got.IsResume, got.ResumeKind, got.ResumeFeedback)
	}
	if got.HumanRejections != both {
		t.Errorf("rework resume HumanRejections:\n got: %q\nwant: %q", got.HumanRejections, both)
	}
	assertOldestFirst("rework resume", got.HumanRejections)
	if len(f.j3Inputs) != j3Calls {
		t.Error("a rework resume must not call J3")
	}
}

func (f *j5Fixture) verifyMet(t *testing.T, id string) {
	t.Helper()
	f.j5.result = j5Out("met", nil, nil)
	if _, err := f.verify(t, &id); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, id).Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("status after J5 met = %s, want awaiting_completion_approval", got)
	}
}
