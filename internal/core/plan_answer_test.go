package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// このファイルは docs/features/core-redesign.md の受入基準「問いへの回答と計画の承認」（AC-55〜AC-103）のうち
// core が持つものを、実ストア・偽の判断の IF で確かめる。CLI・画面の口で確かめるものは internal/cli・
// internal/server のテストが持つ。

// paFixture は計画の承認待ちの課題（問い 2 件・タスク 2 件の計画の版 1）を持つ。
// Q1 は選択肢 3 つで推奨は 2、Q2 は選択肢 2 つで推奨は 1。
type paFixture struct {
	*j2Fixture
	id string
}

// paPlanOut は計画の出力。tasks は a・b・c の先頭 nTasks 件（b は a に、c は b に依存）。
func paPlanOut(nTasks int, usesIntegration bool) []byte {
	return j2OutputFor(false, func(m map[string]any) {
		m["size"] = "M"
		m["budget_impl_usd"] = 41.5
		m["topics"] = []any{
			j2TestTopic("TOPIC-A", j2TestQuestion("QTEXT-1", 3, 2), j2TestQuestion("QTEXT-2", 2, 1)),
		}
		keys := []string{"a", "b", "c", "d"}[:nTasks]
		tasks := j2TasksOf(keys...)
		for i, tk := range tasks {
			tm := tk.(map[string]any)
			tm["repo"], tm["operation"] = "harness-repo", "brief"
			tm["title"] = "TASK-" + keys[i]
			if i > 0 {
				tm["depends_on"] = []string{keys[i-1]}
			}
		}
		m["tasks"] = tasks
		m["uses_integration"] = usesIntegration
	})
}

func newPAFixture(t *testing.T, nTasks int, usesIntegration bool) *paFixture {
	t.Helper()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "pa", "P1")
	res, err := f.run(t, succeededInvoker(paPlanOut(nTasks, usesIntegration)), idPtr(ch.ID))
	if err != nil || len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Fatalf("plan: %v %+v", err, res)
	}
	if got := f.detail(t, ch.ID).Status; got != StatusAwaitingPlanApproval {
		t.Fatalf("status = %s, want awaiting_plan_approval", got)
	}
	return &paFixture{j2Fixture: f, id: ch.ID}
}

func ip(v int64) *int64                         { return &v }
func sp(v string) *string                       { return &v }
func choiceItem(q, c int64) PlanAnswerItem      { return PlanAnswerItem{Question: q, Choice: ip(c)} }
func textItem(q int64, s string) PlanAnswerItem { return PlanAnswerItem{Question: q, Text: sp(s)} }

func (f *paFixture) answer(t *testing.T, att func(*testing.T, string) Attestation, items ...PlanAnswerItem) (*Challenge, error) {
	t.Helper()
	ctx := context.Background()
	prev, err := f.s.PreparePlanAnswer(ctx, f.id, items)
	if err != nil {
		return nil, err
	}
	c, _, err := f.s.ExecutePlanAnswer(ctx, PlanAnswerRequest{ChallengeID: f.id, ExpectedVersion: prev.Version, Answers: items}, att(t, f.id))
	return c, err
}

func (f *paFixture) approve(t *testing.T, att Attestation) error {
	t.Helper()
	return approvePlanForTest(t, f.s, f.id, att)
}

func approvePlanForTest(t *testing.T, s *Store, id string, att Attestation) error {
	t.Helper()
	prev, err := s.PrepareApproval(context.Background(), id)
	if err != nil {
		t.Fatalf("PrepareApproval: %v", err)
	}
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: id, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved}, att)
	return err
}

func (f *paFixture) answers(t *testing.T) []PlanAnswer {
	t.Helper()
	cid, _ := parseChallengeID(f.id)
	var all []PlanAnswer
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(planAnswerSelect+` WHERE challenge_id = ? ORDER BY id`, cid)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			a, err := scanPlanAnswer(rows)
			if err != nil {
				return err
			}
			all = append(all, *a)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read plan_answer: %v", err)
	}
	return all
}

func (f *paFixture) tasks(t *testing.T) []Task {
	t.Helper()
	cid, _ := parseChallengeID(f.id)
	return readTasks(t, f.s, cid)
}

func (f *paFixture) activities(t *testing.T, entity, action string) []Activity {
	t.Helper()
	all, err := f.s.ListActivities(context.Background(), &f.id)
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	var out []Activity
	for _, a := range all {
		if a.Entity == entity && a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

func (f *paFixture) usesIntegration(t *testing.T) *bool {
	t.Helper()
	cid, _ := parseChallengeID(f.id)
	var v *bool
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		v, err = challengeUsesIntegration(ctx0, tx, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

var ctx0 = context.Background()

// AC-55・AC-56・AC-57: 推奨と違う選択肢で答えると、課題は分類済になり、plan_answer が 1 件増え、作業ログに残る。
func TestAnswerPlan_NonRecommendedChoiceReturnsToClassifiedAndRecordsIt(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	before := len(f.answers(t))
	c, err := f.answer(t, ttyAtt, choiceItem(1, 1))
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if c.Status != StatusClassified || f.detail(t, f.id).Status != StatusClassified {
		t.Errorf("status = %s, want classified (AC-55)", c.Status)
	}
	got := f.answers(t)
	if len(got) != before+1 {
		t.Fatalf("plan_answer rows = %d, want %d (AC-56)", len(got), before+1)
	}
	a := got[len(got)-1]
	if a.QuestionNumber != 1 || a.Choice == nil || *a.Choice != 1 || a.Recommended || a.Text != nil {
		t.Errorf("row = %+v, want question 1, choice 1, recommended=false (AC-56)", a)
	}
	logs := f.activities(t, "challenge", "answer_plan")
	if len(logs) != 1 {
		t.Fatalf("answer_plan activities = %d, want 1 (AC-57)", len(logs))
	}
	var after map[string]any
	if err := json.Unmarshal(logs[0].After, &after); err != nil {
		t.Fatal(err)
	}
	if after["status"] != "classified" {
		t.Errorf("after.status = %v, want classified (AC-57)", after["status"])
	}
	if _, ok := after["version"]; !ok {
		t.Errorf("after has no version: %s", logs[0].After)
	}
	if answers, _ := after["answers"].([]any); len(answers) != 1 {
		t.Errorf("after.answers = %v, want 1 entry", after["answers"])
	}
}

// AC-58: 画面の経路・本人確認なしの組での answer_plan は、登録簿が受けず、何も書かれない。
func TestAnswerPlan_RegistryRejectsUIChannelAndWritesNothing(t *testing.T) {
	t.Parallel()
	if registryPermits(ChannelUI, VerificationNone, actionAnswerPlan) {
		t.Error("(ui, none) must not permit answer_plan (AC-58)")
	}
	if !registryPermits(ChannelCLI, VerificationTTYConfirm, actionAnswerPlan) || !registryPermits(ChannelCLI, VerificationRelayed, actionAnswerPlan) {
		t.Error("(cli, tty_confirm) and (cli, relayed) must permit answer_plan")
	}
	if a, ok := actionForTransition(Transition{ID: "T19"}); !ok || a != actionAnswerPlan {
		t.Errorf("T19 -> %v, %v; want answer_plan", a, ok)
	}
	f := newPAFixture(t, 2, true)
	before := len(f.answers(t))
	for _, items := range [][]PlanAnswerItem{{choiceItem(1, 1)}, {choiceItem(1, 2)}} { // 分類済へ戻る答えも記録だけの答えも
		_, err := f.answer(t, uiAttestationForTest, items...)
		if !errors.Is(err, ErrVerificationRejected) {
			t.Errorf("ui answer error = %v, want ErrVerificationRejected", err)
		}
	}
	if len(f.answers(t)) != before || f.detail(t, f.id).Status != StatusAwaitingPlanApproval {
		t.Error("a rejected attestation must write nothing")
	}
}

// AC-59〜AC-62: 推奨と同じ選択肢だけの答えは、状態を変えず、recommended=1 の行・課題の版・作業ログだけが増える。
func TestAnswerPlan_RecommendedChoiceOnlyRecordsAndBumpsVersion(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	d0 := f.detail(t, f.id)
	before := len(f.answers(t))
	c, err := f.answer(t, ttyAtt, choiceItem(1, 2))
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	d := f.detail(t, f.id)
	if d.Status != StatusAwaitingPlanApproval || c.Status != StatusAwaitingPlanApproval {
		t.Errorf("status = %s, want awaiting_plan_approval (AC-59)", d.Status)
	}
	got := f.answers(t)
	if len(got) != before+1 || !got[len(got)-1].Recommended || got[len(got)-1].Choice == nil || *got[len(got)-1].Choice != 2 {
		t.Errorf("rows = %+v, want one more row with recommended=true, choice 2 (AC-60)", got)
	}
	if d.Version != d0.Version+1 || c.Version != d0.Version+1 {
		t.Errorf("version = %d, want %d (AC-61)", d.Version, d0.Version+1)
	}
	logs := f.activities(t, "challenge", "answer_plan")
	if len(logs) != 1 {
		t.Fatalf("answer_plan activities = %d, want 1 (AC-62)", len(logs))
	}
	var after map[string]any
	_ = json.Unmarshal(logs[0].After, &after)
	if _, hasStatus := after["status"]; hasStatus || after["version"] == nil {
		t.Errorf("after = %s, want version and no status", logs[0].After)
	}
}

// AC-63・AC-64: 文の答えは分類済へ戻し、text にその文・choice は NULL の行になる。
func TestAnswerPlan_TextAnswerReturnsToClassifiedWithNullChoice(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	before := len(f.answers(t))
	if _, err := f.answer(t, ttyAtt, textItem(1, "TEXT-ANSWER 別の案で")); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := f.detail(t, f.id).Status; got != StatusClassified {
		t.Errorf("status = %s, want classified (AC-63)", got)
	}
	got := f.answers(t)
	if len(got) != before+1 {
		t.Fatalf("rows = %d, want %d", len(got), before+1)
	}
	a := got[len(got)-1]
	if a.Text == nil || *a.Text != "TEXT-ANSWER 別の案で" || a.Choice != nil || a.Recommended {
		t.Errorf("row = %+v, want text answer with NULL choice (AC-64)", a)
	}
}

// AC-65・AC-66: 複数の問いへの答えは、問いごとに 1 行になる（選択肢と文の混在も）。
func TestAnswerPlan_MultipleAnswersAddOneRowPerQuestion(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	if _, err := f.answer(t, ttyAtt, choiceItem(1, 1), choiceItem(2, 2)); err != nil {
		t.Fatalf("answer: %v", err)
	}
	got := f.answers(t)
	if len(got) != 2 || got[0].QuestionNumber != 1 || *got[0].Choice != 1 || got[1].QuestionNumber != 2 || *got[1].Choice != 2 {
		t.Errorf("rows = %+v, want Q1=1 and Q2=2 (AC-65)", got)
	}

	g := newPAFixture(t, 2, true)
	if _, err := g.answer(t, ttyAtt, choiceItem(1, 1), textItem(2, "TEXT-Q2")); err != nil {
		t.Fatalf("answer: %v", err)
	}
	got = g.answers(t)
	if len(got) != 2 || got[0].Choice == nil || *got[0].Choice != 1 || got[0].Text != nil ||
		got[1].Text == nil || *got[1].Text != "TEXT-Q2" || got[1].Choice != nil {
		t.Errorf("rows = %+v, want Q1 choice row and Q2 text row (AC-66)", got)
	}
}

// AC-71・AC-72・AC-73: 計画に無い問い・選択肢の範囲外は validation_failed 相当で、1 件でも落ちれば何も書かない。
func TestAnswerPlan_InvalidAnswersAreRejectedAndWriteNothing(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	cases := map[string][]PlanAnswerItem{
		"unknown question (AC-71)":            {choiceItem(3, 1)},
		"valid and unknown question (AC-72)":  {choiceItem(1, 1), choiceItem(9, 1)},
		"choice over the options (AC-73)":     {choiceItem(1, 4)},
		"valid and choice over (AC-73/AC-72)": {choiceItem(2, 1), choiceItem(1, 4)},
		"duplicate question":                  {choiceItem(1, 1), textItem(1, "x")},
		"choice zero":                         {choiceItem(1, 0)},
		"blank text":                          {textItem(1, "  ")},
		"no answers":                          nil,
	}
	for name, items := range cases {
		before, d0 := len(f.answers(t)), f.detail(t, f.id)
		if _, err := f.s.PreparePlanAnswer(ctx0, f.id, items); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: Prepare error = %v, want ErrValidation", name, err)
		}
		if _, err := f.answer(t, ttyAtt, items...); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: error = %v, want ErrValidation", name, err)
		}
		// 準備を通らない呼び出し（Prepare を省いた Execute）でも何も書かない。
		if _, _, err := f.s.ExecutePlanAnswer(ctx0, PlanAnswerRequest{ChallengeID: f.id, ExpectedVersion: d0.Version, Answers: items}, ttyAtt(t, f.id)); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: Execute error = %v, want ErrValidation", name, err)
		}
		if after := f.detail(t, f.id); len(f.answers(t)) != before || after.Version != d0.Version || after.Status != d0.Status {
			t.Errorf("%s: wrote something (rows %d→%d, version %d→%d)", name, before, len(f.answers(t)), d0.Version, after.Version)
		}
	}
}

// AC-77: 計画の承認待ちでない課題への答えは invalid_transition（ErrInvalidTransition）で、何も書かない。
func TestAnswerPlan_NotAwaitingPlanApprovalIsInvalidTransition(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "classified", "P1")
	_, err := f.s.PreparePlanAnswer(ctx0, ch.ID, []PlanAnswerItem{choiceItem(1, 1)})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Prepare error = %v, want ErrInvalidTransition", err)
	}
	_, _, err = f.s.ExecutePlanAnswer(ctx0, PlanAnswerRequest{ChallengeID: ch.ID, ExpectedVersion: ch.Version, Answers: []PlanAnswerItem{choiceItem(1, 1)}}, ttyAtt(t, ch.ID))
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Execute error = %v, want ErrInvalidTransition", err)
	}
	if got := countRowsForTest(t, f.s, "plan_answer"); got != 0 {
		t.Errorf("plan_answer rows = %d, want 0", got)
	}
}

// 版が表示の時点から進んでいれば ErrConflict で、何も書かない（記録だけの答えも）。
func TestAnswerPlan_StaleVersionIsConflict(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	d := f.detail(t, f.id)
	for _, items := range [][]PlanAnswerItem{{choiceItem(1, 1)}, {choiceItem(1, 2)}} {
		_, _, err := f.s.ExecutePlanAnswer(ctx0, PlanAnswerRequest{ChallengeID: f.id, ExpectedVersion: d.Version - 1, Answers: items}, ttyAtt(t, f.id))
		if !errors.Is(err, ErrConflict) {
			t.Errorf("error = %v, want ErrConflict", err)
		}
	}
	if len(f.answers(t)) != 0 {
		t.Error("a conflict must write nothing")
	}
}

// AC-79・AC-80: 対話で受けた答えは、plan_answer に会話の識別と発言の要旨が残る（分類済へ戻る答えも記録だけの答えも）。
func TestAnswerPlan_RelayedAnswerRecordsSessionAndQuote(t *testing.T) {
	t.Parallel()
	for name, items := range map[string][]PlanAnswerItem{"returns": {choiceItem(1, 1)}, "record only": {choiceItem(1, 2)}} {
		f := newPAFixture(t, 2, true)
		att := func(t *testing.T, id string) Attestation {
			return relayedAttestation(t, id, "SESSION-XYZ", "QUOTE-ABC")
		}
		if _, err := f.answer(t, att, items...); err != nil {
			t.Fatalf("%s: answer: %v", name, err)
		}
		got := f.answers(t)
		if len(got) != 1 || got[0].RelaySession != "SESSION-XYZ" || got[0].RelayQuote != "QUOTE-ABC" ||
			got[0].Channel != ChannelCLI || got[0].Verification != VerificationRelayed {
			t.Errorf("%s: row = %+v, want relay_session SESSION-XYZ and relay_quote QUOTE-ABC (AC-79/AC-80)", name, got)
		}
	}
	f := newPAFixture(t, 2, true)
	if _, err := f.answer(t, ttyAtt, choiceItem(1, 1)); err != nil {
		t.Fatal(err)
	}
	if a := f.answers(t)[0]; a.RelaySession != "" || a.RelayQuote != "" {
		t.Errorf("a terminal answer must carry no relay evidence: %+v", a)
	}
}

// AC-81〜AC-85: 推奨と違う答えで分類済になった課題の計画の段で、J2 の入力に区画「問いと回答」があり、問いの文と選ばれた
// 選択肢のラベルを含む。判断点が plan を返すと、計画の版が 1 つ増え、計画の承認待ちになる。
func TestAnswerPlan_NextPlanStageGetsQuestionsAndAnswersSectionAndReplans(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	if _, err := f.answer(t, ttyAtt, choiceItem(1, 1), textItem(2, "TEXT-ANSWER-Q2")); err != nil {
		t.Fatal(err)
	}
	versionsBefore := len(f.detail(t, f.id).Plans)
	var launched JudgmentLaunchInput
	inv := succeededInvoker(paPlanOut(2, true))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	res, err := f.run(t, inv, idPtr(f.id))
	if err != nil || len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Fatalf("plan stage: %v %+v", err, res)
	}
	var section string
	for _, s := range launched.Sections {
		if s.Label == "問いと回答" {
			section = s.Content
		}
	}
	if section == "" {
		t.Fatalf("J2 input has no section 問いと回答 (AC-81): %+v", launched.Sections)
	}
	if !strings.Contains(section, "QTEXT-1") {
		t.Errorf("section lacks the question text (AC-82): %q", section)
	}
	if !strings.Contains(section, "QTEXT-1-選択肢1") || !strings.Contains(section, "答え: 1) QTEXT-1-選択肢1") {
		t.Errorf("section lacks the chosen option label (AC-83): %q", section)
	}
	if !strings.Contains(section, "TEXT-ANSWER-Q2") {
		t.Errorf("section lacks the text answer: %q", section)
	}
	d := f.detail(t, f.id)
	if len(d.Plans) != versionsBefore+1 {
		t.Errorf("plan versions = %d, want %d (AC-84)", len(d.Plans), versionsBefore+1)
	}
	if d.Status != StatusAwaitingPlanApproval {
		t.Errorf("status = %s, want awaiting_plan_approval (AC-85)", d.Status)
	}
}

// 答えがまだ 1 件も無い課題の J2 の入力には、区画「問いと回答」を載せない。
func TestPlanAnswersSection_AbsentWithoutAnyRecordedAnswer(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "none", "P1")
	var launched JudgmentLaunchInput
	inv := succeededInvoker(paPlanOut(2, true))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	for _, s := range launched.Sections {
		if s.Label == "問いと回答" {
			t.Errorf("unexpected section: %q", s.Content)
		}
	}
}

// AC-86・AC-87: 答えの無い問い 2 件の計画の承認は、推奨の選択肢で recommended=1 の行を 2 件足す。
func TestApprovePlan_RecordsRecommendedAnswersForUnansweredQuestions(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got := f.answers(t)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (AC-86)", len(got))
	}
	want := map[int64]int64{1: 2, 2: 1}
	for _, a := range got {
		if !a.Recommended || a.Choice == nil || *a.Choice != want[a.QuestionNumber] || a.Text != nil {
			t.Errorf("row = %+v, want recommended choice %d (AC-86/AC-87)", a, want[a.QuestionNumber])
		}
		if a.PlanVersion != 1 || a.Actor != "alice" || a.Channel != ChannelCLI || a.Verification != VerificationTTYConfirm {
			t.Errorf("row = %+v, want plan version 1 and the approver's actor/channel/verification", a)
		}
	}
}

// 承認の記録は、承認と同じ経路の対話で受けた記録（relayed）の手がかりも写す。
func TestApprovePlan_RecommendedAnswersCarryTheApprovalsRelayEvidence(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	if err := f.approve(t, relayedAttestation(t, f.id, "SESS", "QUOTE")); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.answers(t) {
		if a.RelaySession != "SESS" || a.RelayQuote != "QUOTE" || a.Verification != VerificationRelayed {
			t.Errorf("row = %+v, want relayed evidence", a)
		}
	}
}

// AC-88: 推奨と同じ答えを先に記録してあった問いには、承認で行を足さない。
func TestApprovePlan_DoesNotAddAnswersForAlreadyAnsweredQuestions(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	if _, err := f.answer(t, ttyAtt, choiceItem(1, 2)); err != nil {
		t.Fatal(err)
	}
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got := f.answers(t)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (the earlier Q1 row plus the Q2 recommended row)", len(got))
	}
	perQ := map[int64]int{}
	for _, a := range got {
		perQ[a.QuestionNumber]++
	}
	if perQ[1] != 1 || perQ[2] != 1 {
		t.Errorf("rows per question = %v, want 1 each (AC-88)", perQ)
	}
}

// AC-89〜AC-94: タスク 3 件の計画の承認は、pending のタスクの行を 3 件（承認した版で）作り、作業ログへ create を 3 件残し、
// 前提の行を作る。作業ログ（課題の ID 指定）にはタスクの記録も含まれる。
func TestApprovePlan_CreatesPendingTasksDependenciesAndLogs(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 3, true)
	before := countRowsForTest(t, f.s, "task")
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	tasks := f.tasks(t)
	if len(tasks) != 3 || countRowsForTest(t, f.s, "task") != before+3 {
		t.Fatalf("tasks = %d, want 3 (AC-89)", len(tasks))
	}
	byKey := map[string]Task{}
	for _, tk := range tasks {
		byKey[tk.Key] = tk
		if tk.Status != TaskStatusPending {
			t.Errorf("task %s status = %s, want pending (AC-90)", tk.Key, tk.Status)
		}
		if tk.PlanVersion != 1 {
			t.Errorf("task %s plan_version = %d, want 1 (AC-91)", tk.Key, tk.PlanVersion)
		}
	}
	creates := f.activities(t, "task", "create")
	if len(creates) != 3 {
		t.Errorf("task create activities = %d, want 3 (AC-92, AC-93: included in the challenge's log)", len(creates))
	}
	for _, a := range creates {
		var after map[string]any
		_ = json.Unmarshal(a.After, &after)
		if after["status"] != "pending" {
			t.Errorf("create after = %s", a.After)
		}
		if _, hasVersion := after["version"]; hasVersion {
			t.Errorf("task activity must not carry version: %s", a.After)
		}
	}
	if len(byKey["b"].DependsOn) != 1 || byKey["b"].DependsOn[0] != byKey["a"].ID ||
		len(byKey["c"].DependsOn) != 1 || byKey["c"].DependsOn[0] != byKey["b"].ID || len(byKey["a"].DependsOn) != 0 {
		t.Errorf("dependencies = a:%v b:%v c:%v, want b→a and c→b (AC-94)", byKey["a"].DependsOn, byKey["b"].DependsOn, byKey["c"].DependsOn)
	}
}

// 別の課題のタスクの記録は、この課題の作業ログに含まれない。
func TestListActivities_TaskEntriesOfOtherChallengesAreNotIncluded(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	other := f.newClassified(t, "other", "P1")
	if _, err := f.run(t, succeededInvoker(paPlanOut(3, true)), idPtr(other.ID)); err != nil {
		t.Fatal(err)
	}
	if err := approvePlanForTest(t, f.s, other.ID, ttyAtt(t, other.ID)); err != nil {
		t.Fatal(err)
	}
	if got := len(f.activities(t, "task", "create")); got != 0 {
		t.Errorf("task activities of %s = %d, want 0 (the other challenge approved 3 tasks)", f.id, got)
	}
	all, _ := f.s.ListActivities(ctx0, &other.ID)
	n := 0
	for _, a := range all {
		if a.Entity == "task" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("task activities of the approved challenge = %d, want 3", n)
	}
}

// AC-95・AC-96: uses_integration が計画のとおり（真は 1・偽は 0）に課題へ記録される。承認の前は未決（NULL）のまま。
func TestApprovePlan_RecordsUsesIntegration(t *testing.T) {
	t.Parallel()
	for _, want := range []bool{true, false} {
		f := newPAFixture(t, 1, want) // タスクが 2 件以上の計画は統合先を使う（J2 の出力の検査）ので、偽は 1 件で作る
		if f.usesIntegration(t) != nil {
			t.Error("uses_integration must stay NULL until the approval")
		}
		if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
			t.Fatal(err)
		}
		if got := f.usesIntegration(t); got == nil || *got != want {
			t.Errorf("uses_integration = %v, want %v (AC-95/AC-96)", got, want)
		}
	}
}

// AC-97・AC-98 の core 側: 画面の経路（ui・none）の計画の承認も、同じ処理をする（server のテストが口を確かめる）。
func TestApprovePlan_UIChannelDoesTheSameBookkeeping(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 3, true)
	if err := f.approve(t, uiAttestationForTest(t, f.id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if len(f.tasks(t)) != 3 {
		t.Errorf("tasks = %d, want 3", len(f.tasks(t)))
	}
	got := f.answers(t)
	if len(got) != 2 || got[0].Channel != ChannelUI || got[0].Verification != VerificationNone || !got[0].Recommended {
		t.Errorf("answers = %+v, want 2 recommended rows via (ui, none)", got)
	}
}

// AC-99〜AC-102: 着手の後の保留への回答（--replan）で分類済へ戻った時点ではタスクの状態は変わらず、計画を作り直して
// 承認し直すと、前の版のタスクのうち pending・in_progress・verifying が cancelled になり、done は done のまま、新しい版の
// タスクは pending で作られる。
func TestReplanAfterStartCancelsOldTasksKeepsDoneAndCreatesNewOnes(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 4, true)
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(f.id)
	old := f.tasks(t)
	setStatus := func(task Task, to TaskStatus) {
		tid, _ := parseTaskID(task.ID)
		if err := taskTx(t, f.s, func(tx *sql.Tx, rec *activityRecorder) error {
			_, err := setTaskStatus(ctx0, tx, rec, tid, to)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	setStatus(old[0], TaskStatusDone)
	setStatus(old[1], TaskStatusInProgress)
	setStatus(old[2], TaskStatusVerifying) // old[3] は pending のまま
	statuses := func() map[string]TaskStatus {
		m := map[string]TaskStatus{}
		for _, tk := range readTasks(t, f.s, cid) {
			m[tk.ID] = tk.Status
		}
		return m
	}
	snapshot := statuses()

	// 着手中の保留へ、範囲が変わる印つきで回答する。
	if _, err := f.s.HoldChallenge(ctx0, ChannelCLI, f.id, HoldInput{Question: "範囲を変えたい"}); err != nil {
		t.Fatalf("hold: %v", err)
	}
	pre, err := f.s.PrepareAnswer(ctx0, f.id, "新しい範囲")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := f.s.ExecuteAnswer(ctx0, AnswerRequest{ChallengeID: f.id, ExpectedVersion: pre.Version, Answer: "新しい範囲", Replan: true}, relayedAttestation(t, f.id, "s", "q"))
	if err != nil || c.Status != StatusClassified {
		t.Fatalf("answer --replan: %v %+v", err, c)
	}
	for id, st := range statuses() {
		if snapshot[id] != st {
			t.Errorf("task %s status changed at the replan answer: %s → %s (AC-99)", id, snapshot[id], st)
		}
	}

	// 計画を作り直して承認し直す。
	if res, err := f.run(t, succeededInvoker(paPlanOut(2, true)), idPtr(f.id)); err != nil || res.Items[0].Outcome != "plan" {
		t.Fatalf("replan: %v %+v", err, res)
	}
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	after := readTasks(t, f.s, cid)
	if len(after) != 6 {
		t.Fatalf("tasks = %d, want 4 old + 2 new", len(after))
	}
	for _, tk := range after {
		switch {
		case tk.PlanVersion == 1 && tk.ID == old[0].ID:
			if tk.Status != TaskStatusDone {
				t.Errorf("done task became %s (AC-101)", tk.Status)
			}
		case tk.PlanVersion == 1:
			if tk.Status != TaskStatusCancelled {
				t.Errorf("old task %s (was %s) = %s, want cancelled (AC-100)", tk.Key, snapshot[tk.ID], tk.Status)
			}
		default:
			if tk.PlanVersion != 2 || tk.Status != TaskStatusPending {
				t.Errorf("new task %+v, want plan version 2 and pending (AC-102)", tk)
			}
		}
	}
}

// AC-103: 構造化した出力を持たない計画（plan --file）の承認は成功し、task の行は増えず、uses_integration は未決のまま。
func TestApprovePlan_PlanWithoutStructuredOutputSucceedsWithoutTasks(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch, err := s.CreateChallenge(ctx0, ChannelCLI, CreateInput{Title: "manual", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClassifyChallenge(ctx0, ChannelCLI, ch.ID, ClassifyInput{Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PlanChallenge(ctx0, ChannelCLI, ch.ID, PlanInput{Body: "手書きの計画"}); err != nil {
		t.Fatal(err)
	}
	if err := approvePlanForTest(t, s, ch.ID, ttyAtt(t, ch.ID)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	d, _ := s.GetChallenge(ctx0, ch.ID)
	if d.Status != StatusInProgress || countRowsForTest(t, s, "task") != 0 || countRowsForTest(t, s, "plan_answer") != 0 {
		t.Errorf("status=%s tasks=%d answers=%d, want in_progress and no rows", d.Status, countRowsForTest(t, s, "task"), countRowsForTest(t, s, "plan_answer"))
	}
}

// タスクを持たない過去の形の spec の計画の承認も、タスクの行を作らずに成功する。
func TestApprovePlan_LegacySpecWithoutTasksSucceeds(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch, _ := s.CreateChallenge(ctx0, ChannelCLI, CreateInput{Title: "legacy", Description: "d"})
	_, _ = s.ClassifyChallenge(ctx0, ChannelCLI, ch.ID, ClassifyInput{Priority: "P1"})
	if _, _, err := s.PlanChallenge(ctx0, ChannelCLI, ch.ID, PlanInput{Body: "旧形式"}); err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(ch.ID)
	if err := s.db.Write(ctx0, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_plan SET spec = ? WHERE challenge_id = ?`, `{"verdict":"plan","size":"S","done_criteria":"x"}`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := approvePlanForTest(t, s, ch.ID, ttyAtt(t, ch.ID)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if countRowsForTest(t, s, "task") != 0 {
		t.Error("a legacy plan must create no task rows")
	}
}
