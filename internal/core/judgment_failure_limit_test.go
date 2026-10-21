package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func autoJ1Once(t *testing.T, s *Store, decl *AgentDeclaration, inv JudgmentInvoker) *J1AutoResult {
	t.Helper()
	res, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{AgentDecl: decl, Invoker: inv, CycleID: beginJ1Cycle(t, s, 300)})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}
	return res
}

func TestClassifyAutoJ1_HoldsAfterConsecutiveFailuresReachLimit(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored}, onInvoke: func(JudgmentLaunchInput) { calls++ }}

	for i := 0; i < decl.FailureLimit; i++ {
		res := autoJ1Once(t, s, decl, inv)
		if len(res.NotStarted) != 0 {
			t.Fatalf("round %d: NotStarted = %+v, want none before the limit", i, res.NotStarted)
		}
	}
	if calls != decl.FailureLimit {
		t.Fatalf("calls = %d, want %d", calls, decl.FailureLimit)
	}

	res := autoJ1Once(t, s, decl, inv)
	if calls != decl.FailureLimit {
		t.Fatalf("invoker called after the limit: calls = %d", calls)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit || res.NotStarted[0].ChallengeID != ch.ID {
		t.Fatalf("NotStarted = %+v, want one failure_limit for %s", res.NotStarted, ch.ID)
	}
	after, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", after.Status)
	}
	if len(after.Holds) != 1 || !strings.Contains(after.Holds[0].Question, "J1") || !strings.Contains(after.Holds[0].Question, "連続失敗: 2 回") {
		t.Fatalf("Holds = %+v, want a question naming J1 and the count", after.Holds)
	}
}

func TestClassifyAutoJ1_RateLimitedRunsDoNotCountNorBreakStreak(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	if _, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	failing := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored}}
	limited := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}}
	autoJ1Once(t, s, decl, failing)
	autoJ1Once(t, s, decl, limited) // 失敗に数えず、連続も切らない
	calls := 0
	failing.onInvoke = func(JudgmentLaunchInput) { calls++ }
	res := autoJ1Once(t, s, decl, failing) // 2 回目の失敗（数えるのは 1 回目と合わせて 2 → 次の周で保留）
	if len(res.NotStarted) != 0 || calls != 1 {
		t.Fatalf("NotStarted=%+v calls=%d, want the 2nd counted failure to run", res.NotStarted, calls)
	}
	res = autoJ1Once(t, s, decl, failing)
	if calls != 1 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit {
		t.Fatalf("NotStarted=%+v calls=%d, want held without invoking", res.NotStarted, calls)
	}
}

func TestClassifyAutoJ1_FailureCountResetsWhenChallengeVersionChanges(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored}}
	for i := 0; i < decl.FailureLimit; i++ {
		autoJ1Once(t, s, decl, inv)
	}
	desc := "changed"
	if _, err := s.EditChallenge(context.Background(), ChannelCLI, ch.ID, EditInput{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	inv.onInvoke = func(JudgmentLaunchInput) { calls++ }
	res := autoJ1Once(t, s, decl, inv)
	if calls != 1 || len(res.NotStarted) != 0 {
		t.Fatalf("calls=%d NotStarted=%+v, want a fresh count after the version changed", calls, res.NotStarted)
	}
}

func TestClassifyAutoJ1_ExplicitIDIgnoresFailureLimit(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored}}
	for i := 0; i < decl.FailureLimit; i++ {
		autoJ1Once(t, s, decl, inv)
	}
	calls := 0
	inv.onInvoke = func(JudgmentLaunchInput) { calls++ }
	id := ch.ID
	if _, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: beginJ1Cycle(t, s, 300)}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want the explicit-ID run to proceed", calls)
	}
}

func TestLoadJudgmentLimitHit_CountsPerJudgmentPoint(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < decl.FailureLimit; i++ {
		autoJ1Once(t, s, decl, &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored}})
	}
	cid := mustChallengeInternalID(t, ch.ID)
	for _, tc := range []struct {
		j    JudgmentPoint
		want bool
	}{{JudgmentJ1, true}, {JudgmentJ2, false}, {JudgmentJ5, false}} {
		var hit *judgmentLimitHit
		err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var err error
			hit, err = loadJudgmentLimitHit(context.Background(), tx, cid, 1, tc.j, decl.FailureLimit)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if (hit != nil) != tc.want {
			t.Errorf("%s: hit = %+v, want hit=%v", tc.j, hit, tc.want)
		}
	}
}

func TestPlanAutoJ2_HoldsAfterConsecutiveFailuresReachLimit(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "t", "")
	calls := 0
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored}, onInvoke: func(JudgmentLaunchInput) { calls++ }}
	limit := f.agent.FailureLimit
	for i := 0; i < limit; i++ {
		if _, err := f.run(t, inv, nil); err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.run(t, inv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != limit || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit {
		t.Fatalf("calls=%d NotStarted=%+v, want held at the limit without invoking", calls, res.NotStarted)
	}
	after, err := f.s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusAwaitingHuman || len(after.Holds) != 1 || !strings.Contains(after.Holds[0].Question, "J2") {
		t.Fatalf("status=%s holds=%+v, want awaiting_human with a J2 question", after.Status, after.Holds)
	}
}

// answerHoldForTest は課題の未回答の保留に、本人確認済みの回答の経路（PrepareAnswer・ExecuteAnswer）で回答する。
func answerHoldForTest(t *testing.T, s *Store, id, answer string) {
	t.Helper()
	prev, err := s.PrepareAnswer(context.Background(), id, answer)
	if err != nil {
		t.Fatalf("PrepareAnswer: %v", err)
	}
	if _, _, err := s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: id, ExpectedVersion: prev.Version, Answer: answer,
	}, verifiedAttestationForTest(t, id)); err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
}

func TestClassifyAutoJ1_AnswerAfterTheLimitStartsCountingAgain(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	decl := newAgentDeclForJ1Test(t, s, "pos")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored}}
	for i := 0; i < decl.FailureLimit; i++ {
		autoJ1Once(t, s, decl, inv)
	}
	if res := autoJ1Once(t, s, decl, inv); len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit {
		t.Fatalf("NotStarted = %+v, want failure_limit", res.NotStarted)
	}

	answerHoldForTest(t, s, ch.ID, "go on")
	if got, err := s.GetChallenge(context.Background(), ch.ID); err != nil || got.Status != StatusUnclassified {
		t.Fatalf("after the answer: challenge = %+v err = %v, want unclassified", got, err)
	}
	calls := 0
	inv.onInvoke = func(JudgmentLaunchInput) { calls++ }
	// 回答の前の失敗は数えない: 回答の後の 1 回目の失敗では上限（2）に達しない。
	if res := autoJ1Once(t, s, decl, inv); calls != 1 || len(res.NotStarted) != 0 {
		t.Fatalf("calls=%d NotStarted=%+v, want J1 invoked after the answer", calls, res.NotStarted)
	}
	if res := autoJ1Once(t, s, decl, inv); calls != 2 || len(res.NotStarted) != 0 {
		t.Fatalf("calls=%d NotStarted=%+v, want the 2nd failure after the answer to run", calls, res.NotStarted)
	}
	if res := autoJ1Once(t, s, decl, inv); calls != 2 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit {
		t.Fatalf("calls=%d NotStarted=%+v, want held again after 2 failures since the answer", calls, res.NotStarted)
	}
}

// j5FailUntilHeld は C-1（検証中）の J5 を失敗させ続け、連続失敗の上限で人間対応待ちになるまで回す。
func (f *j5Fixture) j5FailUntilHeld(t *testing.T) {
	t.Helper()
	f.j5.result = JudgmentLaunchOutput{Result: RunResultErrored}
	before := len(f.j5Inputs)
	for i := 0; i < f.agent.FailureLimit; i++ {
		res, err := f.verify(t, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.NotStarted) != 0 {
			t.Fatalf("round %d: NotStarted = %+v, want none before the limit", i, res.NotStarted)
		}
	}
	res, err := f.verify(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(f.j5Inputs) - before; got != f.agent.FailureLimit {
		t.Fatalf("J5 invoked %d times, want %d (not at the limit)", got, f.agent.FailureLimit)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit || res.NotStarted[0].ChallengeID != "C-1" {
		t.Fatalf("NotStarted = %+v, want one failure_limit for C-1", res.NotStarted)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
}

func TestVerifyAutoJ5_HoldsAfterConsecutiveFailuresReachLimit(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5FailUntilHeld(t)
	q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1 ORDER BY id DESC LIMIT 1`)
	if !strings.Contains(q, "J5") || !strings.Contains(q, "連続失敗: 2 回") {
		t.Errorf("hold question lacks the judgment point and count:\n%s", q)
	}
	j5 := f.j5Runs(t)
	if got := f.queryString(t, `SELECT run_id FROM hold WHERE challenge_id = 1 ORDER BY id DESC LIMIT 1`); got != strings.TrimPrefix(j5[len(j5)-1].ID, "R-") {
		t.Errorf("hold.run_id = %q, want the last J5 run", got)
	}
}

// オーナーの決定（2026-10-22 #279）: 人間が回答した保留は、種類を問わず数え直しの起点になる。
// J5 の連続失敗の上限による保留への回答で、差し戻し（rework_limit）の回数も 0 に戻る。
func TestVerifyAutoJ5_AnswerToFailureLimitHoldAlsoResetsReworkCount(t *testing.T) {
	fixedActor(t, "alice")
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.agent.ReworkLimit = 2
	f.notMetRound(t, "C-1")
	f.runDelegation(t, "C-1") // 1 回目の not_met の後の委譲（検証中へ）
	f.j5FailUntilHeld(t)

	answerHoldForTest(t, f.s, "C-1", "go on")
	if got := f.detail(t, "C-1").Status; got != StatusVerifying {
		t.Fatalf("after the answer: status = %s, want verifying", got)
	}
	// 回答の後の 1 回目の not_met: 回答の前の not_met を数えないため、上限（2）に達せず委譲が起動される。
	f.notMetRound(t, "C-1")
	launches := len(f.deleg.launched())
	res := f.runDelegation(t, "C-1")
	if len(res.NotStarted) != 0 || len(res.Items) != 1 || len(f.deleg.launched()) != launches+1 {
		t.Fatalf("result = %+v, want a launch (the not_met before the answer must not count)", res)
	}
}
