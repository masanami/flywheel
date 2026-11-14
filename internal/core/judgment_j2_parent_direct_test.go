package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// J2 の判定 parent_direct（委譲の計画が立たず親が直接行うのが適切な課題）のテスト。

func TestValidateJ2Output_ParentDirectNeedsReasonAndCriteria(t *testing.T) {
	vc := j2TestContext(t, false)
	ok := []byte(`{"verdict":"parent_direct","question":"  周を回して観察する課題で委譲に向かない ","done_criteria":"親が周を回し結果を Issue に残した"}`)
	v, valid := validateJ2Output(ok, vc)
	if !valid {
		t.Fatal("want ok=true for parent_direct with reason and criteria")
	}
	if v.Verdict != J2VerdictParentDirect || v.Question == nil || *v.Question != "周を回して観察する課題で委譲に向かない" || v.ParentDirectCriteria != "親が周を回し結果を Issue に残した" {
		t.Errorf("validated = %+v", v)
	}
	for name, out := range map[string]string{
		"no reason":          `{"verdict":"parent_direct","done_criteria":"x"}`,
		"blank reason":       `{"verdict":"parent_direct","question":"  ","done_criteria":"x"}`,
		"no criteria":        `{"verdict":"parent_direct","question":"r"}`,
		"blank criteria":     `{"verdict":"parent_direct","question":"r","done_criteria":" \n"}`,
		"null criteria":      `{"verdict":"parent_direct","question":"r","done_criteria":null}`,
		"criteria wrong typ": `{"verdict":"parent_direct","question":"r","done_criteria":1}`,
	} {
		if _, valid := validateJ2Output([]byte(out), vc); valid {
			t.Errorf("%s: want ok=false", name)
		}
	}
}

func TestJ2OutputSchema_AcceptsParentDirectVerdict(t *testing.T) {
	if !strings.Contains(string(j2OutputSchema()), `"parent_direct"`) {
		t.Errorf("schema does not list parent_direct: %s", j2OutputSchema())
	}
}

const j2ParentDirectOutput = `{"verdict":"parent_direct","question":"UNIQUE-PD-REASON","done_criteria":"UNIQUE-PD-CRITERIA"}`

func TestPlanAutoJ2_ParentDirect_RaisesParentDirectHoldAndReplacesDoneCriteria(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "direct", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	res, err := f.run(t, succeededInvoker([]byte(j2ParentDirectOutput)), idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	item := res.Items[0]
	if item.Outcome != "parent_direct" || item.Status == nil || *item.Status != string(StatusAwaitingHuman) {
		t.Errorf("item = %+v", item)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 {
		t.Fatalf("status %s holds %+v", d.Status, d.Holds)
	}
	h := d.Holds[0]
	if h.Kind != HoldKindParentDirect || h.Question != "UNIQUE-PD-REASON" || h.FromStatus != StatusClassified {
		t.Errorf("hold = %+v", h)
	}
	if d.DoneCriteria != "UNIQUE-PD-CRITERIA" {
		t.Errorf("done_criteria = %q", d.DoneCriteria)
	}
	if len(d.Plans) != 0 {
		t.Errorf("plans = %v, want none", d.Plans)
	}
	runInt, _ := parseRunID(item.RunID)
	var holdRunID sql.NullInt64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT run_id FROM hold WHERE challenge_id = ?`, mustChallengeInternalID(t, ch.ID)).Scan(&holdRunID)
	}); err != nil {
		t.Fatalf("read hold: %v", err)
	}
	if !holdRunID.Valid || holdRunID.Int64 != runInt {
		t.Errorf("hold.run_id = %v, want %d", holdRunID, runInt)
	}
	// 読んだ記録は付けない（uncertain と同じ）。
	if sb := d.SourceBinding; sb.ReadCommentsCount != 0 || sb.ReadUpstreamUpdatedAt != "" {
		t.Errorf("read values = (%d, %q), want unchanged", sb.ReadCommentsCount, sb.ReadUpstreamUpdatedAt)
	}
}

// 手で立てた parent_direct の保留と同じ見え方: オーナー待ち（needs_human・未回答の問いの一覧）に出ず、
// 親のエージェントが動く番に出る。
func TestPlanAutoJ2_ParentDirect_IsVisibleLikeAManualParentDirectHold(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "direct", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	if _, err := f.run(t, succeededInvoker([]byte(j2ParentDirectOutput)), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	ov, err := f.s.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.ParentAgentChallenges) != 1 || ov.ParentAgentChallenges[0].ID != ch.ID {
		t.Errorf("ParentAgentChallenges = %v, want [%s]", ov.ParentAgentChallenges, ch.ID)
	}
	for _, c := range ov.NeedsHumanChallenges {
		if c.ID == ch.ID {
			t.Errorf("NeedsHumanChallenges contains %s", ch.ID)
		}
	}
	hs, err := f.s.ListUnansweredHolds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hs {
		if h.ChallengeID == ch.ID {
			t.Errorf("ListUnansweredHolds contains %s", ch.ID)
		}
	}
}

// 戻す道: 既存の回答の操作で保留を解くと課題が分類済（計画待ち）に戻り、J2 が計画し直せる。
func TestPlanAutoJ2_ParentDirect_AnsweringTheHoldReturnsToClassifiedAndJ2ReplansNextCycle(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "direct", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	if _, err := f.run(t, succeededInvoker([]byte(j2ParentDirectOutput)), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	prev, err := f.s.PrepareAnswer(context.Background(), ch.ID, "委譲できる形に直した")
	if err != nil {
		t.Fatal(err)
	}
	c2, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: ch.ID, ExpectedVersion: prev.Version, Answer: "委譲できる形に直した"},
		verifiedAttestationForTest(t, ch.ID))
	if err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
	if c2.Status != StatusClassified {
		t.Fatalf("status = %s, want classified (awaiting plan)", c2.Status)
	}
	res, err := f.run(t, succeededInvoker(j2OutputFor(true, nil)), idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2 (second): %v", err)
	}
	if res.Items[0].Outcome != "plan" {
		t.Errorf("second item = %+v, want a plan", res.Items[0])
	}
}

func TestPlanAutoJ2_ParentDirect_InvalidOutputChangesNothing(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "direct", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	before := f.detail(t, ch.ID)
	if _, err := f.run(t, succeededInvoker([]byte(`{"verdict":"parent_direct","question":"r"}`)), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	after := f.detail(t, ch.ID)
	if after.Status != StatusClassified || len(after.Holds) != 0 || after.DoneCriteria != before.DoneCriteria {
		t.Errorf("challenge changed: status %s holds %v done %q", after.Status, after.Holds, after.DoneCriteria)
	}
	if got := f.runResults(t, ch.ID); len(got) != 1 || got[0] != RunResultInvalidOutput {
		t.Errorf("run results = %v, want [invalid_output]", got)
	}
}
