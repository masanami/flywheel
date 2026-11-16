package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// J2 の判定 parent_direct（委譲の計画が立たず親が直接行うのが適切な課題）のテスト。

func TestValidateJ2Output_ParentDirectNeedsReasonAndCriteria(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	if !strings.Contains(string(j2OutputSchema()), `"parent_direct"`) {
		t.Errorf("schema does not list parent_direct: %s", j2OutputSchema())
	}
}

const j2ParentDirectOutput = `{"verdict":"parent_direct","question":"UNIQUE-PD-REASON","done_criteria":"UNIQUE-PD-CRITERIA"}`

// j2ParentDirectAppended は、元の達成条件 "ORIGINAL-CRITERIA" の後ろへ親の達成条件を書き足した全文。
const j2ParentDirectAppended = "ORIGINAL-CRITERIA\n\n" + ParentDirectCriteriaHeading + "\n\nUNIQUE-PD-CRITERIA"

// newClassifiedWithCriteria は達成条件 "ORIGINAL-CRITERIA" を持つ分類済の課題を作る。
func (f *j2Fixture) newClassifiedWithCriteria(t *testing.T, title string) *Challenge {
	t.Helper()
	ch, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: title, Description: "DESC-" + title, DoneCriteria: "ORIGINAL-CRITERIA"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	if _, err := f.s.ClassifyChallenge(context.Background(), ChannelCLI, ch.ID, ClassifyInput{Priority: "P1"}); err != nil {
		t.Fatalf("ClassifyChallenge: %v", err)
	}
	return ch
}

func TestAppendParentDirectCriteria(t *testing.T) {
	t.Parallel()
	section := ParentDirectCriteriaHeading + "\n\nP"
	for name, tc := range map[string]struct{ original, parent, want string }{
		"keeps the original and appends": {"O", "P", "O\n\n" + section},
		"trailing newlines are trimmed":  {"O\n\n", " P\n", "O\n\n" + section},
		"empty original":                 {"", "P", section},
		"blank original":                 {" \n", "P", section},
		"already appended":               {"O\n\n" + section, "P", "O\n\n" + section},
		"already appended, trailing nl":  {"O\n\n" + section + "\n", "P", "O\n\n" + section + "\n"},
		"different parent appends again": {"O\n\n" + section, "Q", "O\n\n" + section + "\n\n" + ParentDirectCriteriaHeading + "\n\nQ"},
	} {
		if got := appendParentDirectCriteria(tc.original, tc.parent); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestPlanAutoJ2_ParentDirect_RaisesParentDirectHoldAndAppendsDoneCriteria(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassifiedWithCriteria(t, "direct")
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
	// 元の達成条件が残り、その後ろに見出しと親の達成条件が付く。
	if d.DoneCriteria != j2ParentDirectAppended {
		t.Errorf("done_criteria = %q, want %q", d.DoneCriteria, j2ParentDirectAppended)
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
	t.Parallel()
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

// 元の達成条件が空なら、見出しと親の達成条件だけになる。
func TestPlanAutoJ2_ParentDirect_EmptyOriginalCriteriaBecomesHeadingAndParentCriteria(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "direct", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	if got := f.detail(t, ch.ID).DoneCriteria; got != "" {
		t.Fatalf("fixture done_criteria = %q, want empty", got)
	}
	if _, err := f.run(t, succeededInvoker([]byte(j2ParentDirectOutput)), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	want := ParentDirectCriteriaHeading + "\n\nUNIQUE-PD-CRITERIA"
	if got := f.detail(t, ch.ID).DoneCriteria; got != want {
		t.Errorf("done_criteria = %q, want %q", got, want)
	}
}

// 戻す道: 既存の回答の操作で保留を解くと課題が分類済（計画待ち）に戻り、J2 が計画し直せる。
// 計画し直すときの J2 の入力の「課題」の区画には、元の達成条件と親の達成条件の両方が入る。
// 再び parent_direct が返っても、同じ内容を二重に書き足さない。
func TestPlanAutoJ2_ParentDirect_AnsweringTheHoldReturnsToClassifiedAndJ2ReplansNextCycle(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassifiedWithCriteria(t, "direct")
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
	var launched JudgmentLaunchInput
	inv := succeededInvoker(j2OutputFor(true, nil))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2 (second): %v", err)
	}
	if res.Items[0].Outcome != "plan" {
		t.Errorf("second item = %+v, want a plan", res.Items[0])
	}
	var challengeSection string
	for _, sec := range launched.Sections {
		if sec.Label == labelChallenge {
			challengeSection = sec.Content
		}
	}
	if !strings.Contains(challengeSection, j2ParentDirectAppended) {
		t.Errorf("J2 input %q section lacks both criteria %q:\n%s", labelChallenge, j2ParentDirectAppended, challengeSection)
	}
}

// 保留を解いた後に J2 がまた parent_direct を返しても、同じ見出しと本文を二重に書き足さない。
func TestPlanAutoJ2_ParentDirect_SameCriteriaIsNotAppendedTwice(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassifiedWithCriteria(t, "direct")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	if _, err := f.run(t, succeededInvoker([]byte(j2ParentDirectOutput)), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	prev, err := f.s.PrepareAnswer(context.Background(), ch.ID, "まだ委譲できない")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: ch.ID, ExpectedVersion: prev.Version, Answer: "まだ委譲できない"},
		verifiedAttestationForTest(t, ch.ID)); err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
	if _, err := f.run(t, succeededInvoker([]byte(j2ParentDirectOutput)), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2 (second): %v", err)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", d.Status)
	}
	if d.DoneCriteria != j2ParentDirectAppended {
		t.Errorf("done_criteria = %q, want %q (appended once)", d.DoneCriteria, j2ParentDirectAppended)
	}
}

func TestPlanAutoJ2_ParentDirect_InvalidOutputChangesNothing(t *testing.T) {
	t.Parallel()
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

// 達成条件の書き足しは、保留を立てた作業ログ（hold）の before・after に書き足す前と後の全文で載る。
func TestPlanAutoJ2_ParentDirect_DoneCriteriaAppendIsInTheHoldActivity(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassifiedWithCriteria(t, "direct")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	prevCriteria := f.detail(t, ch.ID).DoneCriteria
	if prevCriteria != "ORIGINAL-CRITERIA" {
		t.Fatalf("fixture done_criteria = %q", prevCriteria)
	}
	if _, err := f.run(t, succeededInvoker([]byte(j2ParentDirectOutput)), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	acts, err := f.s.ListActivities(context.Background(), &ch.ID)
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	var hold *Activity
	for i := range acts {
		if acts[i].Action == string(OpHold) {
			hold = &acts[i]
		}
	}
	if hold == nil {
		t.Fatalf("no hold activity in %+v", acts)
	}
	var before, after map[string]any
	if err := json.Unmarshal(hold.Before, &before); err != nil {
		t.Fatalf("unmarshal Before: %v", err)
	}
	if err := json.Unmarshal(hold.After, &after); err != nil {
		t.Fatalf("unmarshal After: %v", err)
	}
	if before["done_criteria"] != prevCriteria || after["done_criteria"] != j2ParentDirectAppended {
		t.Errorf("hold activity done_criteria before/after = %v/%v, want %q/%q",
			before["done_criteria"], after["done_criteria"], prevCriteria, j2ParentDirectAppended)
	}
}

// 写す直前に読み直した状態が分類済でなければ（mapped=false）、保留を立てず・達成条件を変えず、
// run は成功のまま返す（状態は返さない）。
func TestPlanAutoJ2_ParentDirect_ChallengeLeftClassifiedDuringJ2_IsNotMapped(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "moved", "P1")
	f.bind(t, ch, 7, simpleThread("b", "c1"))
	prevCriteria := f.detail(t, ch.ID).DoneCriteria
	inv := succeededInvoker([]byte(j2ParentDirectOutput))
	inv.onInvoke = func(JudgmentLaunchInput) {
		// J2 の実行中に、人間が先に人間対応待ちへ進めた。
		if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, ch.ID, HoldInput{Question: "human moved it"}); err != nil {
			t.Errorf("HoldChallenge: %v", err)
		}
	}
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	item := res.Items[0]
	if item.Status != nil || item.Outcome != "parent_direct" || item.Result != RunResultSucceeded {
		t.Errorf("item = %+v, want parent_direct outcome, succeeded, and no status (not mapped)", item)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || d.Holds[0].Kind == HoldKindParentDirect || d.Holds[0].Question != "human moved it" {
		t.Errorf("status %s holds %+v, want only the human's hold", d.Status, d.Holds)
	}
	if d.DoneCriteria != prevCriteria {
		t.Errorf("done_criteria = %q, want unchanged %q", d.DoneCriteria, prevCriteria)
	}
}
