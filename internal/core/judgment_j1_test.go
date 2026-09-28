package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// --- テスト用のヘルパー ---

// newAgentDeclForJ1Test はワークスペース s にポジション定義ファイルを置き、
// それを指す全既定値の AgentDeclaration を返す。
func newAgentDeclForJ1Test(t *testing.T, s *Store, positionContent string) *AgentDeclaration {
	t.Helper()
	const posRelPath = "position.md"
	if err := os.WriteFile(filepath.Join(s.Workspace(), posRelPath), []byte(positionContent), 0o644); err != nil {
		t.Fatalf("write position file: %v", err)
	}
	decl := defaultAgentDeclaration()
	decl.PositionFile = posRelPath
	return decl
}

// beginJ1Cycle は classify --auto 相当の周を開始し、Y-<n> の ID を返す。
func beginJ1Cycle(t *testing.T, s *Store, budgetUSD float64) string {
	t.Helper()
	c, err := s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "classify --auto", BudgetUSD: budgetUSD, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	return c.ID
}

// j1Output は j1RawOutput を JSON バイト列にする。
func j1Output(t *testing.T, out j1RawOutput) []byte {
	t.Helper()
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal j1RawOutput: %v", err)
	}
	return b
}

// succeededInvoker は常に succeeded・output を返す偽の判断の IF。
func succeededInvoker(output []byte) *fakeJudgmentInvoker {
	return &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: output}}
}

// --- j1OutputSchema・validateJ1Output ---

func TestJ1OutputSchema_ContainsClosedValues(t *testing.T) {
	schema := j1OutputSchema()
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatalf("j1OutputSchema is not valid JSON: %v", err)
	}
	if decoded["type"] != "object" {
		t.Errorf("type = %v, want object", decoded["type"])
	}
	props, ok := decoded["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing or wrong type: %#v", decoded["properties"])
	}
	for _, key := range []string{"verdict", "priority", "size", "reason", "question"} {
		if _, ok := props[key]; !ok {
			t.Errorf("properties is missing %q", key)
		}
	}
}

func TestValidateJ1Output_MineRequiresPriority(t *testing.T) {
	// AC-77: mine で優先度が null は invalid。
	if _, ok := validateJ1Output(j1Output(t, j1RawOutput{Verdict: "mine", Reason: "r"})); ok {
		t.Error("want ok=false when mine has no priority")
	}
	if _, ok := validateJ1Output(j1Output(t, j1RawOutput{Verdict: "mine", Priority: strPtr("P1"), Reason: "r"})); !ok {
		t.Error("want ok=true when mine has a valid priority")
	}
}

func TestValidateJ1Output_UncertainRequiresQuestion(t *testing.T) {
	// AC-80: uncertain で問いが null は invalid。
	if _, ok := validateJ1Output(j1Output(t, j1RawOutput{Verdict: "uncertain", Reason: "r"})); ok {
		t.Error("want ok=false when uncertain has no question")
	}
	if _, ok := validateJ1Output(j1Output(t, j1RawOutput{Verdict: "uncertain", Question: strPtr("q?"), Reason: "r"})); !ok {
		t.Error("want ok=true when uncertain has a question")
	}
}

func TestValidateJ1Output_UnknownVerdictIsInvalid(t *testing.T) {
	// AC-86: 判定が閉集合の外の値は invalid_output。
	if _, ok := validateJ1Output(j1Output(t, j1RawOutput{Verdict: "maybe", Reason: "r"})); ok {
		t.Error("want ok=false for an unknown verdict")
	}
}

func TestValidateJ1Output_NotMineDoesNotRequireExtras(t *testing.T) {
	if _, ok := validateJ1Output(j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "r"})); !ok {
		t.Error("want ok=true for not_mine without priority/question")
	}
}

// --- buildJ1Sections（入力の組み立て） ---

func TestBuildJ1Sections_IncludesChallengeFieldsAndPosition(t *testing.T) {
	// AC-75。
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "UNIQUE-POSITION-MARKER このエージェントの担当範囲")
	urgency := "高"
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{
		Title: "UNIQUE-TITLE-1", Description: "UNIQUE-DESC-1", DoneCriteria: "UNIQUE-DONE-1", Urgency: &urgency,
	})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}

	sections, err := s.buildJ1Sections(context.Background(), *ch, decl)
	if err != nil {
		t.Fatalf("buildJ1Sections: %v", err)
	}
	var all string
	for _, sec := range sections {
		all += sec.Content
	}
	for _, want := range []string{"UNIQUE-TITLE-1", "UNIQUE-DESC-1", "UNIQUE-DONE-1", "高", ch.Reporter, "UNIQUE-POSITION-MARKER"} {
		if !containsSubstring(all, want) {
			t.Errorf("sections do not contain %q; got sections=%+v", want, sections)
		}
	}
}

// AC-74: 保留の記録（問いと回答）を J1 の入力に含める。
func TestBuildJ1Sections_IncludesHoldQuestionsAndAnswers(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, ch.ID, HoldInput{Question: "UNIQUE-QUESTION-1"}); err != nil {
		t.Fatalf("HoldChallenge: %v", err)
	}
	att, err := Verify(ChannelCLI, &fakeVerifier{method: VerificationTTYConfirm, actor: "tester"}, "s", ch.ID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	preview, err := s.PrepareAnswer(context.Background(), ch.ID, "UNIQUE-ANSWER-1")
	if err != nil {
		t.Fatalf("PrepareAnswer: %v", err)
	}
	if _, _, err := s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: ch.ID, Answer: "UNIQUE-ANSWER-1", ExpectedVersion: preview.Version}, att); err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}

	current, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	sections, err := s.buildJ1Sections(context.Background(), current.Challenge, decl)
	if err != nil {
		t.Fatalf("buildJ1Sections: %v", err)
	}
	var all string
	for _, sec := range sections {
		all += sec.Content
	}
	if !containsSubstring(all, "UNIQUE-QUESTION-1") || !containsSubstring(all, "UNIQUE-ANSWER-1") {
		t.Errorf("sections do not contain the hold question/answer: %+v", sections)
	}
}

func containsSubstring(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOfString(haystack, needle) >= 0)
}

func indexOfString(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// --- 経路 invoker の作業ログ（AC-67）・本人確認の拒否（AC-68） ---

func TestApplyJ1Mine_RecordsActivityWithInvokerChannelAndRunID(t *testing.T) {
	s := newStoreForTest(t)
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	runID := insertFakeRunForTest(t, s, ch.ID, judgmentJ1)

	mapped, err := s.applyJ1Mine(context.Background(), formatRunID(runID), ch.ID, PriorityP1)
	if err != nil {
		t.Fatalf("applyJ1Mine: %v", err)
	}
	if !mapped {
		t.Fatal("mapped = false, want true")
	}

	row := queryLastActivityForTest(t, s, ch.ID)
	if row.channel != string(ChannelInvoker) {
		t.Errorf("channel = %q, want %q", row.channel, ChannelInvoker)
	}
	if row.verification != string(VerificationNone) {
		t.Errorf("verification = %q, want %q", row.verification, VerificationNone)
	}
	if row.runID == nil || *row.runID != runID {
		t.Errorf("run_id = %v, want %d", row.runID, runID)
	}
}

func TestApplyJ1Uncertain_RecordsActivityWithInvokerChannelAndRunID(t *testing.T) {
	s := newStoreForTest(t)
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	runID := insertFakeRunForTest(t, s, ch.ID, judgmentJ1)

	mapped, err := s.applyJ1Uncertain(context.Background(), formatRunID(runID), ch.ID, "q?")
	if err != nil {
		t.Fatalf("applyJ1Uncertain: %v", err)
	}
	if !mapped {
		t.Fatal("mapped = false, want true")
	}

	row := queryLastActivityForTest(t, s, ch.ID)
	if row.channel != string(ChannelInvoker) || row.verification != string(VerificationNone) {
		t.Errorf("channel/verification = %q/%q, want invoker/none", row.channel, row.verification)
	}
	if row.runID == nil || *row.runID != runID {
		t.Errorf("run_id = %v, want %d", row.runID, runID)
	}

	// AC-79: 保留の記録は原因の run の ID を持つ。
	detail, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	if len(detail.Holds) != 1 {
		t.Fatalf("Holds = %+v, want 1 hold", detail.Holds)
	}
	holdRunID := queryHoldRunIDForTest(t, s, ch.ID)
	if holdRunID == nil || *holdRunID != runID {
		t.Errorf("hold.run_id = %v, want %d", holdRunID, runID)
	}
}

func TestVerify_RejectsChannelInvoker_ForApprove(t *testing.T) {
	v := &fakeVerifier{method: VerificationTTYConfirm, actor: "tester"}
	_, err := Verify(ChannelInvoker, v, "approve summary", "C-1")
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("Verify(invoker) error = %v, want ErrVerificationRejected", err)
	}
	if v.confirmCalls != 0 {
		t.Errorf("Confirm was called %d times, want 0 (fail-closed: reject before touching Confirm)", v.confirmCalls)
	}
}

func TestVerify_RejectsChannelInvoker_ForReject(t *testing.T) {
	v := &fakeVerifier{method: VerificationTTYConfirm, actor: "tester"}
	_, err := Verify(ChannelInvoker, v, "reject summary", "C-1")
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("Verify(invoker) error = %v, want ErrVerificationRejected", err)
	}
}

func TestVerify_RejectsChannelInvoker_ForAnswer(t *testing.T) {
	v := &fakeVerifier{method: VerificationTTYConfirm, actor: "tester"}
	_, err := Verify(ChannelInvoker, v, "answer summary", "C-1")
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("Verify(invoker) error = %v, want ErrVerificationRejected", err)
	}
}

// --- 対象の選び方（AC-69・71・72・73） ---

func TestSelectJ1AutoTargets_ExcludesChallengeWithActiveRun(t *testing.T) {
	s := newStoreForTest(t)
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	insertActiveFakeRunForTest(t, s, ch.ID, judgmentJ1)

	ids := listJ1AutoTargetsForTest(t, s)
	if containsInt64(ids, mustChallengeInternalID(t, ch.ID)) {
		t.Errorf("targets = %v, want %s excluded (has an active run)", ids, ch.ID)
	}
}

func TestSelectJ1AutoTargets_ExcludesUpstreamClosedOrMissing(t *testing.T) {
	for _, upstream := range []string{"closed", "missing"} {
		t.Run(upstream, func(t *testing.T) {
			s := newStoreForTest(t)
			ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
			if err != nil {
				t.Fatalf("CreateChallenge: %v", err)
			}
			bindSourceForTest(t, s, ch.ID, upstream, "in_policy")

			ids := listJ1AutoTargetsForTest(t, s)
			if containsInt64(ids, mustChallengeInternalID(t, ch.ID)) {
				t.Errorf("targets = %v, want %s excluded (upstream=%s)", ids, ch.ID, upstream)
			}
		})
	}
}

func TestSelectJ1AutoTargets_ExcludesOutOfPolicy(t *testing.T) {
	s := newStoreForTest(t)
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	bindSourceForTest(t, s, ch.ID, "open", "out_of_policy")

	ids := listJ1AutoTargetsForTest(t, s)
	if containsInt64(ids, mustChallengeInternalID(t, ch.ID)) {
		t.Errorf("targets = %v, want %s excluded (out_of_policy)", ids, ch.ID)
	}
}

// AC-73: 上流の状態が closed の未分類の課題も、ID を指定すれば J1 の対象になる。
func TestClassifyAutoJ1_ExplicitIDIgnoresUpstreamExclusion(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	bindSourceForTest(t, s, ch.ID, "closed", "in_policy")

	cycleID := beginJ1Cycle(t, s, 300)
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "r"}))
	id := ch.ID
	res, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != ch.ID {
		t.Fatalf("Items = %+v, want 1 item for %s", res.Items, ch.ID)
	}
}

// --- 写像（AC-76〜86） ---

func TestClassifyAutoJ1_Mine_ClassifiesWithPriority(t *testing.T) {
	// AC-76。
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	cycleID := beginJ1Cycle(t, s, 300)
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "mine", Priority: strPtr("P1"), Reason: "r"}))
	id := ch.ID
	res, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}
	if res.Items[0].Outcome != "mine" || res.Items[0].Status == nil || *res.Items[0].Status != string(StatusClassified) {
		t.Fatalf("Items[0] = %+v, want outcome=mine status=classified", res.Items[0])
	}

	after, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	if after.Status != StatusClassified {
		t.Errorf("Status = %q, want classified", after.Status)
	}
	if after.Priority == nil || *after.Priority != PriorityP1 {
		t.Errorf("Priority = %v, want P1", after.Priority)
	}
}

func TestClassifyAutoJ1_MineWithoutPriority_InvalidOutputLeavesChallengeUnchanged(t *testing.T) {
	// AC-77。
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	before, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge before: %v", err)
	}
	cycleID := beginJ1Cycle(t, s, 300)
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "mine", Reason: "r"}))
	id := ch.ID
	res, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}
	if res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("Result = %q, want invalid_output", res.Items[0].Result)
	}
	after, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge after: %v", err)
	}
	if after.Status != before.Status || after.Version != before.Version {
		t.Errorf("challenge changed: before=%+v after=%+v", before.Challenge, after.Challenge)
	}
}

func TestClassifyAutoJ1_Uncertain_HoldsWithQuestion(t *testing.T) {
	// AC-78。
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	cycleID := beginJ1Cycle(t, s, 300)
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "uncertain", Question: strPtr("UNIQUE-Q"), Reason: "r"}))
	id := ch.ID
	res, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}
	if res.Items[0].Outcome != "uncertain" || res.Items[0].Status == nil || *res.Items[0].Status != string(StatusAwaitingHuman) {
		t.Fatalf("Items[0] = %+v, want outcome=uncertain status=awaiting_human", res.Items[0])
	}
	after, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	if len(after.Holds) != 1 || after.Holds[0].Question != "UNIQUE-Q" {
		t.Fatalf("Holds = %+v, want 1 hold with question UNIQUE-Q", after.Holds)
	}
}

func TestClassifyAutoJ1_UncertainWithoutQuestion_InvalidOutput(t *testing.T) {
	// AC-80。
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	cycleID := beginJ1Cycle(t, s, 300)
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "uncertain", Reason: "r"}))
	id := ch.ID
	res, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}
	if res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("Result = %q, want invalid_output", res.Items[0].Result)
	}
}

func TestClassifyAutoJ1_NotMine_LeavesChallengeUnchanged(t *testing.T) {
	// AC-81。
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	before, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge before: %v", err)
	}
	cycleID := beginJ1Cycle(t, s, 300)
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "not for me"}))
	id := ch.ID
	res, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}
	if res.Items[0].Outcome != "not_mine" || res.Items[0].Status != nil {
		t.Fatalf("Items[0] = %+v, want outcome=not_mine status=nil", res.Items[0])
	}
	after, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge after: %v", err)
	}
	if after.Status != before.Status || after.Version != before.Version {
		t.Errorf("challenge changed: before=%+v after=%+v", before.Challenge, after.Challenge)
	}
}

func TestClassifyAutoJ1_UnknownVerdict_InvalidOutput(t *testing.T) {
	// AC-86。
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	cycleID := beginJ1Cycle(t, s, 300)
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "maybe", Reason: "r"}))
	id := ch.ID
	res, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}
	if res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("Result = %q, want invalid_output", res.Items[0].Result)
	}
}

// --- not_mine の除外・triage（AC-82・83・84・85） ---

func TestJ1NotMine_ExcludedFromAutoTargetsUntilVersionChanges(t *testing.T) {
	// AC-82・83。
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	cycleID := beginJ1Cycle(t, s, 300)
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "r"}))
	id := ch.ID
	if _, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID}); err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}

	ids := listJ1AutoTargetsForTest(t, s)
	if containsInt64(ids, mustChallengeInternalID(t, ch.ID)) {
		t.Fatalf("targets = %v, want %s excluded right after a not_mine verdict", ids, ch.ID)
	}

	// AC-83: edit で版が変われば、自動の対象に戻る。
	desc := "changed"
	if _, err := s.EditChallenge(context.Background(), ChannelCLI, ch.ID, EditInput{Description: &desc}); err != nil {
		t.Fatalf("EditChallenge: %v", err)
	}
	ids = listJ1AutoTargetsForTest(t, s)
	if !containsInt64(ids, mustChallengeInternalID(t, ch.ID)) {
		t.Fatalf("targets = %v, want %s included again after edit", ids, ch.ID)
	}
}

func TestJ1NotMine_AppearsInTriageWithRunIDAndReason(t *testing.T) {
	// AC-84。
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	cycleID := beginJ1Cycle(t, s, 300)
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "UNIQUE-REASON"}))
	id := ch.ID
	res, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if len(ov.NeedsHumanTriage) != 1 {
		t.Fatalf("NeedsHumanTriage = %+v, want 1 item", ov.NeedsHumanTriage)
	}
	item := ov.NeedsHumanTriage[0]
	if item.ChallengeID != ch.ID || item.RunID != res.Items[0].RunID || item.Reason != "UNIQUE-REASON" {
		t.Errorf("triage item = %+v, want challenge=%s run=%s reason=UNIQUE-REASON", item, ch.ID, res.Items[0].RunID)
	}
}

func TestJ1NotMine_ClassifiedManuallyRemovesFromTriage(t *testing.T) {
	// AC-85。
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	cycleID := beginJ1Cycle(t, s, 300)
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "r"}))
	id := ch.ID
	if _, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID}); err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}

	if _, err := s.ClassifyChallenge(context.Background(), ChannelCLI, ch.ID, ClassifyInput{Priority: "P2"}); err != nil {
		t.Fatalf("ClassifyChallenge: %v", err)
	}

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if len(ov.NeedsHumanTriage) != 0 {
		t.Fatalf("NeedsHumanTriage = %+v, want empty after manual classify", ov.NeedsHumanTriage)
	}
}

func TestGetOverview_NeedsHumanTriage_EmptyIsNotNil(t *testing.T) {
	s := newStoreForTest(t)
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if ov.NeedsHumanTriage == nil {
		t.Error("NeedsHumanTriage = nil, want an empty (non-nil) slice")
	}
}

// --- ID を指定した個別の操作の状態条件・並行実行 ---

func TestClassifyAutoJ1_ExplicitID_NotUnclassified_IsInvalidTransition(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	if _, err := s.ClassifyChallenge(context.Background(), ChannelCLI, ch.ID, ClassifyInput{Priority: "P1"}); err != nil {
		t.Fatalf("ClassifyChallenge: %v", err)
	}
	cycleID := beginJ1Cycle(t, s, 300)
	id := ch.ID
	_, err = s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: &fakeJudgmentInvoker{}, CycleID: cycleID})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition", err)
	}
}

// AC-70: 同じ課題に対する2つの classify --auto <C-ID> を並行して実行すると、
// run は1つしか作られず、もう一方は run_in_progress で終わる。
func TestClassifyAutoJ1_ConcurrentExplicitID_OnlyOneRunStarts(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}

	invoked := make(chan struct{})
	block := make(chan struct{})
	inv := &fakeJudgmentInvoker{
		invoked: invoked, block: block,
		result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "r"})},
	}

	cycleID1 := beginJ1Cycle(t, s, 300)
	id := ch.ID
	done := make(chan error, 1)
	go func() {
		_, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: cycleID1})
		done <- err
	}()

	select {
	case <-invoked:
	case <-timeoutChan(t, 10):
		t.Fatal("timed out waiting for the first call to reach the invoker")
	}

	cycleID2 := beginJ1Cycle(t, s, 300)
	_, err2 := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: &fakeJudgmentInvoker{}, CycleID: cycleID2})
	if !errors.Is(err2, ErrRunInProgress) {
		t.Fatalf("second call error = %v, want ErrRunInProgress", err2)
	}

	close(block)
	select {
	case err1 := <-done:
		if err1 != nil {
			t.Fatalf("first call error = %v, want nil", err1)
		}
	case <-timeoutChan(t, 10):
		t.Fatal("timed out waiting for the first call to finish")
	}

	runs, err := s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns = %d runs, want exactly 1", len(runs))
	}
}
