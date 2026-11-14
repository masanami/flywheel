package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// 計画の差し戻しの理由（approval の kind=plan・decision=rejected の reason）が、次の J2 の入力へ
// 日付つき・古い順で渡ることを検証する。

func (f *j2Fixture) rejectPlanForTest(t *testing.T, id, reason string, at time.Time, att func(t *testing.T, id string) Attestation) {
	t.Helper()
	ctx := context.Background()
	f.s.now = func() time.Time { return at }
	t.Cleanup(func() { f.s.now = nil })
	if _, _, err := f.s.PlanChallenge(ctx, ChannelCLI, id, PlanInput{Body: "plan for " + reason}); err != nil {
		t.Fatalf("PlanChallenge: %v", err)
	}
	prev, err := f.s.PrepareRejection(ctx, id, reason)
	if err != nil {
		t.Fatalf("PrepareRejection: %v", err)
	}
	if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{ChallengeID: id, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionRejected, Reason: &reason}, att(t, id)); err != nil {
		t.Fatalf("ExecuteApproval(reject): %v", err)
	}
}

func (f *j2Fixture) j2SectionsAfter(t *testing.T, id string) string {
	t.Helper()
	f.s.now = nil
	var launched JudgmentLaunchInput
	inv := succeededInvoker(j2OutputFor(false, nil))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	if _, err := f.run(t, inv, idPtr(id)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if len(launched.Sections) == 0 || !strings.Contains(allSectionsText(launched.Sections), "DESC-") {
		t.Fatal("J2 was not launched with the challenge section")
	}
	for _, s := range launched.Sections {
		if s.Label == PlanRejectionsSectionLabel {
			return s.Content
		}
	}
	return ""
}

func TestPlanAutoJ2_PlanRejections_NoRejectionHasNoSection(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "none", "P1")
	if got := f.j2SectionsAfter(t, ch.ID); got != "" {
		t.Fatalf("section = %q, want none", got)
	}
}

func TestPlanAutoJ2_PlanRejections_ReasonsAreDatedOldestFirst(t *testing.T) {
	f := newJ2Fixture(t)
	fixedActor(t, "alice")
	ch := f.newClassified(t, "rej", "P1")
	f.rejectPlanForTest(t, ch.ID, "REASON-FIRST", rejDay(2026, 11, 4), ttyAtt)
	if got := f.detail(t, ch.ID).Status; got != StatusClassified {
		t.Fatalf("status after plan rejection = %s, want classified", got)
	}
	f.rejectPlanForTest(t, ch.ID, "REASON-SECOND", rejDay(2026, 11, 6), relayedAtt)
	got := f.j2SectionsAfter(t, ch.ID)
	i1 := strings.Index(got, "1. 2026-11-04\nREASON-FIRST")
	i2 := strings.Index(got, "2. 2026-11-06\nREASON-SECOND")
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Fatalf("section = %q, want dated reasons oldest first", got)
	}
	if strings.Contains(got, "QUOTE-NOT-THE-REASON") {
		t.Errorf("section must not carry relay_quote: %q", got)
	}
}

func TestFormatPlanRejectionsSection_EmptyWhenNone(t *testing.T) {
	if got := formatPlanRejectionsSection(nil); got != "" {
		t.Fatalf("format(nil) = %q, want empty", got)
	}
}

func TestPlanAutoJ2_PlanRejections_IgnoresCompletionRejectionsBlankAndOtherChallenges(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "mine", "P1")
	other := f.newClassified(t, "other", "P1")
	cid, _ := parseChallengeID(ch.ID)
	oid, _ := parseChallengeID(other.ID)
	ins := func(id int64, kind, decision, reason string) {
		t.Helper()
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
				VALUES (?, ?, ?, 1, 'alice', 'cli', 'tty_confirm', ?, '2026-11-01T00:00:00.000Z')`,
				id, kind, decision, reason)
			return err
		}); err != nil {
			t.Fatalf("insert approval: %v", err)
		}
	}
	ins(cid, "completion", "rejected", "COMPLETION-REASON")
	ins(cid, "plan", "rejected", "   ")
	ins(oid, "plan", "rejected", "OTHER-CHALLENGE")
	if got := f.j2SectionsAfter(t, ch.ID); got != "" {
		t.Fatalf("section = %q, want none", got)
	}
}
