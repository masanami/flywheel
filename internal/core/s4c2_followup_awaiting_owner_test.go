package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// オーナーの決定 2026-11-23: 足す依頼がオーナー待ち（問いが回答待ち）の間は、フォローアップを捨てる・済みにするを断る。

func awaitingOwnerFollowup(t *testing.T) (*paFixture, string) {
	t.Helper()
	f, fid := followupAddFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_addition SET state = ?`, string(TaskAdditionAwaitingOwner))
		return err
	})
	return f, fid
}

func TestFollowupAwaitingOwner_DropAndDoneAreRefusedAndWriteNothing(t *testing.T) {
	t.Parallel()
	f, fid := awaitingOwnerFollowup(t)
	ctx := context.Background()
	acts := f.count(t, `SELECT COUNT(*) FROM activity`)

	_, err := f.s.DropFollowups(ctx, []string{fid}, "不要", fuAtt(t, fid))
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "先に問いへ回答") {
		t.Fatalf("drop err = %v, want ErrValidation with the Japanese guidance", err)
	}
	_, err = f.s.CompleteFollowups(ctx, ChannelCLI, []string{fid}, "確かめた")
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "先に問いへ回答") {
		t.Fatalf("done err = %v, want ErrValidation with the Japanese guidance", err)
	}
	if _, err := f.s.PrepareFollowupDrop(ctx, []string{fid}, "不要"); !errors.Is(err, ErrFollowupAwaitingOwner) {
		t.Errorf("prepare err = %v, want ErrFollowupAwaitingOwner", err)
	}
	if s4c2Followup(t, f.s, fid).Status != FollowupStatusOpen {
		t.Error("followup status changed")
	}
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionAwaitingOwner {
		t.Errorf("additions = %+v, want awaiting_owner unchanged", adds)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM activity`); got != acts {
		t.Errorf("activities = %d, want %d (nothing written)", got, acts)
	}
}

func TestFollowupAwaitingOwner_OneBlockedIdRefusesTheWholeBatch(t *testing.T) {
	t.Parallel()
	f, fid := awaitingOwnerFollowup(t)
	other := s4c2OpenFollowup(t, f.s, f.id)
	_, err := f.s.CompleteFollowups(context.Background(), ChannelCLI, []string{other, fid}, "確かめた")
	if !errors.Is(err, ErrFollowupAwaitingOwner) {
		t.Fatalf("err = %v, want ErrFollowupAwaitingOwner", err)
	}
	if s4c2Followup(t, f.s, other).Status != FollowupStatusOpen {
		t.Error("the other followup was closed; the whole batch must be refused")
	}
}

func TestFollowupPendingAddition_DoneStillClosesAndDeclinesTheAddition(t *testing.T) {
	t.Parallel()
	f, fid := followupAddFixture(t)
	if _, err := f.s.CompleteFollowups(context.Background(), ChannelCLI, []string{fid}, "確かめた"); err != nil {
		t.Fatal(err)
	}
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionDeclined {
		t.Errorf("additions = %+v, want declined", adds)
	}
}

func TestFollowupAwaitingOwner_AnsweringSkipThenAllowsDrop(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	fid := s4c2OpenFollowup(t, f.s, f.id)
	n, _ := parseFollowupID(fid)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_addition SET followup_id = ?`, n)
		return err
	})
	if _, err := f.s.CompleteFollowups(context.Background(), ChannelCLI, []string{fid}, "確かめた"); !errors.Is(err, ErrFollowupAwaitingOwner) {
		t.Fatalf("err = %v, want refused while awaiting the owner", err)
	}
	if _, err := f.answerHold(t, taskAddOptionSkip); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CompleteFollowups(context.Background(), ChannelCLI, []string{fid}, "確かめた"); err != nil {
		t.Fatalf("after skip: %v", err)
	}
}

// 上げる指示で上限が上がったときは、承認の記録（種類 budget・タスクの指定なし・--limit と同じ形）を残す。
func TestPromoteTask_OverLimitWritesABudgetApproval(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	before := f.budgetApprovals(t)
	if _, res, err := destPromote(t, f.s, fid, destOver(nil)); err != nil || res.Addition == nil {
		t.Fatalf("promote: %+v %v", res, err)
	}
	if got := f.budgetApprovals(t); got != before+1 {
		t.Fatalf("budget approvals = %d, want %d", got, before+1)
	}
	var verification string
	var task sql.NullInt64
	var limitOnly int
	var version, planVersion int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT verification, task_id, limit_only, target_version FROM approval WHERE challenge_id = ? AND kind = 'budget' ORDER BY id DESC LIMIT 1`, f.cid()).
			Scan(&verification, &task, &limitOnly, &version); err != nil {
			return err
		}
		p, ok, err := loadApprovedPlan(context.Background(), tx, f.cid())
		if err != nil || !ok {
			t.Fatalf("approved plan: ok=%v err=%v", ok, err)
		}
		planVersion = p.Version
		return nil
	})
	if verification != string(VerificationTTYConfirm) || task.Valid || limitOnly != 1 || version != planVersion {
		t.Errorf("approval = verification %q task %v limit_only %d target_version %d (plan %d)", verification, task, limitOnly, version, planVersion)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = ?`, f.cid(), ActivityActionBudgetLimit); n != 1 {
		t.Errorf("budget_limit activities = %d, want 1", n)
	}
}

func TestPromoteTask_WithinLimitWritesNoBudgetApproval(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	before := f.budgetApprovals(t)
	if _, _, err := destPromote(t, f.s, fid, destSmall); err != nil {
		t.Fatal(err)
	}
	if got := f.budgetApprovals(t); got != before {
		t.Errorf("budget approvals = %d, want %d", got, before)
	}
}

func TestPromoteToChallenge_WritesNoBudgetApproval(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	before := f.budgetApprovals(t)
	if _, _, err := destPromote(t, f.s, fid, FollowupPromoteOptions{NewChallenge: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.budgetApprovals(t); got != before {
		t.Errorf("budget approvals = %d, want %d", got, before)
	}
}
