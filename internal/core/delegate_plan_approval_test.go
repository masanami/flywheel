package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// このファイルは #143（承認が計画の版も記録する。承認の target_version は課題の版のまま、
// 委譲は計画の承認が記録した計画の版で計画を引く）を、core の承認 API（PrepareApproval・
// ExecuteApproval）を通して検証する。承認を SQL で直接入れない。

// newApprovedViaAPI は、計画を 2 回登録（版 1・2）し、版 2 に構造化した出力を付け、core の承認 API で
// 計画を承認した課題を返す。課題の版（承認の target_version）と計画の版（2）は一致しない。
func (f *delegateFixture) newApprovedViaAPI(t *testing.T, title string) string {
	t.Helper()
	ctx := context.Background()
	ch, err := f.s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: title, Description: "D", DoneCriteria: "DC"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	if _, err := f.s.ClassifyChallenge(ctx, ChannelCLI, ch.ID, ClassifyInput{Priority: "P2"}); err != nil {
		t.Fatalf("ClassifyChallenge: %v", err)
	}
	for _, body := range []string{"PLAN-V1", "PLAN-V2"} {
		if _, _, err := f.s.PlanChallenge(ctx, ChannelCLI, ch.ID, PlanInput{Body: body}); err != nil {
			t.Fatalf("PlanChallenge: %v", err)
		}
	}
	cid, _ := parseChallengeID(ch.ID)
	if err := f.s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_plan SET spec = ? WHERE challenge_id = ? AND version = 2`, planSpec(nil), cid)
		return err
	}); err != nil {
		t.Fatalf("set spec: %v", err)
	}
	prev, err := f.s.PrepareApproval(ctx, ch.ID)
	if err != nil {
		t.Fatalf("PrepareApproval: %v", err)
	}
	if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{ChallengeID: ch.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved},
		verifiedAttestationForTest(t, ch.ID)); err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
	return ch.ID
}

func TestExecuteApproval_PlanApprovalRecordsPlanVersionAndKeepsChallengeVersionAsTarget(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newApprovedViaAPI(t, "a")
	detail, err := f.s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	var plan *Approval
	for i := range detail.Approvals {
		if detail.Approvals[i].Kind == ApprovalKindPlan {
			plan = &detail.Approvals[i]
		}
	}
	if plan == nil || plan.PlanVersion == nil || *plan.PlanVersion != 2 {
		t.Fatalf("plan approval = %+v, want plan_version 2", plan)
	}
	// create=1 → classify=2 → plan=3 → plan=4 → 承認の直前の課題の版は 4（計画の版 2 とは別の値）。
	if plan.TargetVersion != 4 {
		t.Errorf("target_version = %d, want 4 (the challenge version, not the plan version)", plan.TargetVersion)
	}
}

func TestExecuteApproval_RejectionRecordsNoPlanVersion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusClassified)
	if _, _, err := s.PlanChallenge(context.Background(), ChannelCLI, c.ID, PlanInput{Body: "v1"}); err != nil {
		t.Fatal(err)
	}
	reason := "no"
	prev, err := s.PrepareRejection(context.Background(), c.ID, reason)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionRejected, Reason: &reason}, verifiedAttestationForTest(t, c.ID)); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Approvals) != 1 {
		t.Fatalf("approvals = %+v, want exactly the rejection", detail.Approvals)
	}
	for _, a := range detail.Approvals {
		if a.PlanVersion != nil {
			t.Errorf("approval %+v has plan_version, want nil for a rejection", a)
		}
	}
}

// 完了条件: CLI の approve（core の承認 API）で計画を承認した課題が、run の委譲の候補に入る。
func TestRunDelegation_ChallengeApprovedThroughTheApprovalAPIIsDelegatedWithTheApprovedPlanVersion(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newApprovedViaAPI(t, "a")

	res, err := f.run(t, nil)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != id || len(res.NotStarted) != 0 {
		t.Fatalf("items = %+v not_started = %+v, want %s delegated", res.Items, res.NotStarted, id)
	}
	if len(f.j3Inputs) != 1 || !strings.Contains(sectionsText(f.j3Inputs[0].Sections), "PLAN-V2") {
		t.Errorf("J3 was not given the approved plan version 2")
	}
}

// 完了条件: 承認済みなのに委譲できない課題は、黙って除外せず not_started に理由つきで出る。
// 計画の版を持たない既存の承認の行は委譲せず、承認し直しが必要な旨を示す。
func TestRunDelegation_ApprovalWithoutPlanVersion_NotStartedWithReasonAndNothingLaunched(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "legacy", "P1", planSpec(nil))
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE approval SET plan_version = NULL WHERE challenge_id = ?`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	res, err := f.run(t, nil)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	if len(res.Items) != 0 || len(res.NotStarted) != 1 {
		t.Fatalf("items = %+v not_started = %+v, want one not_started", res.Items, res.NotStarted)
	}
	ns := res.NotStarted[0]
	if ns.ChallengeID != id || ns.Reason != NotStartedPlanUnavailable || !strings.Contains(ns.Detail, "承認し直し") {
		t.Errorf("not_started = %+v, want %s plan_unavailable with a re-approve hint", ns, id)
	}
	if len(f.j3Inputs) != 0 {
		t.Error("J3 must not start")
	}
}

func TestRunDelegation_ApprovalPointingAtAMissingPlan_NotStartedWithReason(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "dangling", "P1", planSpec(nil))
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE approval SET plan_version = 9 WHERE challenge_id = ?`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedPlanUnavailable || !strings.Contains(res.NotStarted[0].Detail, "9") {
		t.Fatalf("not_started = %+v, want plan_unavailable naming version 9", res.NotStarted)
	}
}

func TestRunDelegation_ApprovedPlanWithoutSpec_NotStartedWithReason(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "manual", "P1", "")
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != id || res.NotStarted[0].Reason != NotStartedPlanUnavailable {
		t.Fatalf("not_started = %+v, want %s plan_unavailable", res.NotStarted, id)
	}
}

// ID を指定した run も、承認に計画の版が無い理由（承認し直しが必要）を示す。
func TestRunDelegation_ExplicitID_ApprovalWithoutPlanVersion_ErrorNamesTheReason(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "legacy", "P1", planSpec(nil))
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE approval SET plan_version = NULL WHERE challenge_id = ?`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := f.run(t, &id)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "承認し直し") {
		t.Fatalf("err = %v, want ErrValidation naming the re-approval need", err)
	}
}
