package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// J4（子の問いへの応答。docs/features/m3-invoker-delegation.md §J4）の検証。実ストア・偽の委譲の IF・偽の契約の口を使う。

// j4Router は判断点ごとに出力を変える偽の JudgmentInvoker。J4 以外は J3 のブリーフを返す。
type j4Router struct {
	mu     sync.Mutex
	j4Out  []JudgmentLaunchOutput // 呼ぶたびに先頭から返す。尽きたら最後の値を返す
	j4Seen []JudgmentLaunchInput
}

func (r *j4Router) Available(context.Context) error { return nil }

func (r *j4Router) InvokeJudgment(_ context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if in.Judgment != JudgmentJ4 {
		return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: briefJSON("BRIEF-MARKER")}, nil
	}
	r.j4Seen = append(r.j4Seen, in)
	out := r.j4Out[len(r.j4Out)-1]
	if len(r.j4Out) > 1 {
		r.j4Out = r.j4Out[1:]
	}
	return out, nil
}

func j4Out(decisions ...map[string]any) JudgmentLaunchOutput {
	b, _ := json.Marshal(map[string]any{"decisions": decisions})
	return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: b, ReportedTotalCostUSD: float64Ptr(0.5)}
}

func dec(i int, decision string, option, answer any) map[string]any {
	return map[string]any{"question_index": i, "decision": decision, "option": option, "answer": answer}
}

// minorQuestionsOut は、人へ上げる種類でない 2 つの問い（1: 選択肢あり・2: 選択肢なし）で止まる報告。
func minorQuestionsOut() JudgmentLaunchOutput {
	return JudgmentLaunchOutput{Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(1), StructuredOutput: reportJSON(func(m map[string]any) {
		m["outcome"] = "questions"
		m["branch"] = "feat/x"
		m["questions"] = []any{
			map[string]any{"kind": "minor", "text": "QUESTION-ONE", "options": []string{"A", "B"}, "recommendation": "A"},
			map[string]any{"kind": "requirements", "text": "QUESTION-TWO", "options": []string{}, "recommendation": nil},
		}
	})}
}

func answerAll() JudgmentLaunchOutput {
	return j4Out(dec(1, "answer", "B", nil), dec(2, "answer", nil, "ANSWER-TWO"))
}

func (f *delegateFixture) runWith(t *testing.T, r *j4Router, id string) *DelegateResult {
	t.Helper()
	in := f.input(f.cycle(t, 300), &id)
	in.Judgment = r
	res, err := f.s.RunDelegation(context.Background(), in)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	return res
}

func (f *delegateFixture) countRows(t *testing.T, q string) int {
	t.Helper()
	n := 0
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestValidateJ4Output(t *testing.T) {
	qs := []j4Question{{Kind: "minor", Text: "q1", Options: []string{"A", "B"}}, {Text: "q2"}}
	ok := func(s string) bool { _, v := validateJ4Output([]byte(s), qs); return v }
	if !ok(`{"decisions":[{"question_index":1,"decision":"answer","option":"A","answer":null},{"question_index":2,"decision":"answer","option":null,"answer":"x"}]}`) {
		t.Error("a valid all-answer output must pass")
	}
	if !ok(`{"decisions":[{"question_index":2,"decision":"escalate","option":null,"answer":null},{"question_index":1,"decision":"escalate","option":null,"answer":"reason"}]}`) {
		t.Error("escalate needs no option or answer, and the order is free")
	}
	bad := map[string]string{
		"option not among the options": `{"decisions":[{"question_index":1,"decision":"answer","option":"C","answer":null},{"question_index":2,"decision":"escalate","option":null,"answer":null}]}`,
		"option missing":               `{"decisions":[{"question_index":1,"decision":"answer","option":null,"answer":"A"},{"question_index":2,"decision":"escalate","option":null,"answer":null}]}`,
		"no options and blank answer":  `{"decisions":[{"question_index":1,"decision":"escalate","option":null,"answer":null},{"question_index":2,"decision":"answer","option":null,"answer":"  "}]}`,
		"duplicate index":              `{"decisions":[{"question_index":1,"decision":"escalate","option":null,"answer":null},{"question_index":1,"decision":"escalate","option":null,"answer":null}]}`,
		"wrong count":                  `{"decisions":[{"question_index":1,"decision":"escalate","option":null,"answer":null}]}`,
		"index out of range":           `{"decisions":[{"question_index":1,"decision":"escalate","option":null,"answer":null},{"question_index":3,"decision":"escalate","option":null,"answer":null}]}`,
		"unknown decision":             `{"decisions":[{"question_index":1,"decision":"maybe","option":null,"answer":null},{"question_index":2,"decision":"escalate","option":null,"answer":null}]}`,
		"not json":                     `x`,
	}
	for name, s := range bad {
		if ok(s) {
			t.Errorf("%s: must be rejected", name)
		}
	}
}

func TestJ4Schema_EnumComesFromTheDecisionValues(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(j4OutputSchema(), &m); err != nil {
		t.Fatal(err)
	}
	enum := m["properties"].(map[string]any)["decisions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["decision"].(map[string]any)["enum"]
	if !reflect.DeepEqual(enum, []any{"answer", "escalate"}) {
		t.Errorf("enum = %v", enum)
	}
}

func TestJ4_AllAnswerResumesTheChildInTheSameCycleWithoutAHold(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{minorQuestionsOut(), {Result: RunResultSucceeded, StructuredOutput: reportJSON(nil), ReportedTotalCostUSD: float64Ptr(1)}}
	r := &j4Router{j4Out: []JudgmentLaunchOutput{answerAll()}}
	f.runWith(t, r, id)

	launches := f.deleg.launched()
	if len(launches) != 2 {
		t.Fatalf("launches = %d, want 2 (the child is resumed in the same cycle)", len(launches))
	}
	first, second := launches[0], launches[1]
	if !second.IsResume || second.SessionID != first.SessionID || second.ResumeKind != ResumeKindAnswer {
		t.Errorf("second launch = %+v, want a resume of %s with the answer kind", second, first.SessionID)
	}
	for _, want := range []string{"問い 1 の回答:\nB", "問い 2 の回答:\nANSWER-TWO"} {
		if !strings.Contains(second.ResumeAnswer, want) {
			t.Errorf("ResumeAnswer lacks %q:\n%s", want, second.ResumeAnswer)
		}
	}
	if !strings.Contains(second.ResumeQuestion, "QUESTION-ONE") || second.ResumeBranch != "feat/x" {
		t.Errorf("question = %q branch = %q", second.ResumeQuestion, second.ResumeBranch)
	}
	// 2 回目の起動の結果の照合は、この試験の偽の gh が成果物を返さないので保留になりうる。問いと J4 の回答の保留は無い。
	if n := f.countRows(t, `SELECT COUNT(*) FROM hold WHERE question LIKE '%QUESTION-%' OR question LIKE '%ANSWER-TWO%'`); n != 0 {
		t.Errorf("holds about the questions = %d, want 0 (J4's answers are not put in a hold)", n)
	}
	if len(r.j4Seen) != 1 || !strings.Contains(sectionsText(r.j4Seen[0].Sections), "QUESTION-TWO") {
		t.Errorf("J4 calls = %d", len(r.j4Seen))
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM run WHERE kind = 'judgment' AND judgment = 'J4' AND result = 'succeeded' AND output LIKE '%ANSWER-TWO%'`); n != 1 {
		t.Errorf("the J4 output is not kept as the run's output")
	}
}

func TestJ4_AnyEscalateHoldsWithTheReasonAndTheDelegationRun(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = minorQuestionsOut()
	r := &j4Router{j4Out: []JudgmentLaunchOutput{j4Out(dec(1, "answer", "A", nil), dec(2, "escalate", nil, "REASON-MARKER"))}}
	f.runWith(t, r, id)

	if len(f.deleg.launched()) != 1 || f.detail(t, id).Status != StatusAwaitingHuman {
		t.Fatalf("launches = %d status = %s, want 1 and awaiting_human", len(f.deleg.launched()), f.detail(t, id).Status)
	}
	q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`)
	for _, want := range []string{"REASON-MARKER", "QUESTION-ONE", "QUESTION-TWO"} {
		if !strings.Contains(q, want) {
			t.Errorf("hold question lacks %q:\n%s", want, q)
		}
	}
	runs := f.delegateRuns(t)
	if got := f.queryString(t, `SELECT run_id FROM hold WHERE challenge_id = 1`); got != strings.TrimPrefix(runs[0].ID, "R-") {
		t.Errorf("hold.run_id = %q, want the delegation run", got)
	}
}

func TestJ4_RuleKindsAndInvalidOrFailedJ4GoToTheHuman(t *testing.T) {
	t.Run("scope is never given to J4", func(t *testing.T) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.deleg.result = questionsOut("feat/x") // 1 つが scope
		r := &j4Router{j4Out: []JudgmentLaunchOutput{answerAll()}}
		f.runWith(t, r, id)
		if len(r.j4Seen) != 0 || f.detail(t, id).Status != StatusAwaitingHuman {
			t.Errorf("J4 calls = %d status = %s", len(r.j4Seen), f.detail(t, id).Status)
		}
	})
	t.Run("invalid output", func(t *testing.T) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.deleg.result = minorQuestionsOut()
		r := &j4Router{j4Out: []JudgmentLaunchOutput{j4Out(dec(1, "answer", "Z", nil), dec(2, "answer", nil, "x"))}}
		f.runWith(t, r, id)
		if f.detail(t, id).Status != StatusAwaitingHuman || len(f.deleg.launched()) != 1 {
			t.Errorf("status = %s launches = %d", f.detail(t, id).Status, len(f.deleg.launched()))
		}
		if n := f.countRows(t, `SELECT COUNT(*) FROM run WHERE judgment = 'J4' AND result = 'invalid_output'`); n != 1 {
			t.Errorf("invalid J4 runs = %d, want 1", n)
		}
	})
	t.Run("J4 failed", func(t *testing.T) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.deleg.result = minorQuestionsOut()
		r := &j4Router{j4Out: []JudgmentLaunchOutput{{Result: RunResultErrored, ErrorSummary: "boom"}}}
		f.runWith(t, r, id)
		if f.detail(t, id).Status != StatusAwaitingHuman {
			t.Errorf("status = %s, want awaiting_human", f.detail(t, id).Status)
		}
	})
}

func TestJ4_ResumeLimitIsThreePerChainAndTheFourthGoesToTheHuman(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = minorQuestionsOut() // 毎回また問いで止まる
	r := &j4Router{j4Out: []JudgmentLaunchOutput{answerAll()}}
	f.runWith(t, r, id)

	if got := len(f.deleg.launched()); got != 4 {
		t.Errorf("launches = %d, want 4 (1 + 3 resumes by J4's answers)", got)
	}
	if len(r.j4Seen) != 3 {
		t.Errorf("J4 calls = %d, want 3 (the 4th is not called)", len(r.j4Seen))
	}
	if f.detail(t, id).Status != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", f.detail(t, id).Status)
	}
	if q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`); !strings.Contains(q, "上限（3 回）") {
		t.Errorf("hold question lacks the limit reason:\n%s", q)
	}
}

// 同じ周で再開できなくても、回答は run に残り、次の起動が同じ回答を渡して再開する（新しいセッションで始めない）。
func TestJ4_PendingAnswerIsResumedByTheNextLaunchDecision(t *testing.T) {
	pv := int64(1)
	deleg := runRow{ID: "R-1", Kind: runKindDelegate, SessionID: "sess", Result: RunResultSucceeded, PlanVersion: &pv,
		Output: string(minorQuestionsOut().StructuredOutput)}
	j4 := runRow{ID: "R-2", Kind: runKindJudgment, Judgment: judgmentJ4, Result: RunResultSucceeded, Output: string(answerAll().StructuredOutput)}
	h := &launchHistory{Runs: []runRow{deleg}, J4Runs: []runRow{j4}}
	resume, limit := h.decideLaunch(1, 2)
	if limit != nil || resume == nil || resume.Kind != ResumeKindAnswer || resume.Target.ID != "R-1" ||
		!strings.Contains(resume.Answer, "ANSWER-TWO") || !strings.Contains(resume.Question, "QUESTION-ONE") {
		t.Fatalf("resume = %+v limit = %+v", resume, limit)
	}
	// 回答を渡す再開が済んだ（後の委譲の run がある）なら、同じ回答を渡し直さない。
	later := runRow{ID: "R-3", Kind: runKindDelegate, SessionID: "sess", Result: RunResultSucceeded, PlanVersion: &pv, Output: `{"outcome":"completed"}`}
	h.Runs = append(h.Runs, later)
	if resume, _ := h.decideLaunch(1, 2); resume != nil {
		t.Errorf("resume = %+v, want none after the answered resume ran", resume)
	}
	// 人へ上げた J4（全問 answer でない）は再開に使わない。
	h = &launchHistory{Runs: []runRow{deleg}, J4Runs: []runRow{{ID: "R-2", Kind: runKindJudgment, Judgment: judgmentJ4, Result: RunResultSucceeded,
		Output: string(j4Out(dec(1, "answer", "A", nil), dec(2, "escalate", nil, nil)).StructuredOutput)}}}
	if resume, _ := h.decideLaunch(1, 2); resume != nil {
		t.Errorf("resume = %+v, want none for an escalating J4", resume)
	}
}

// --- 契約の判断待ち（decider が parent・kind が answer） ---

func (f *contractFixture) pollWith(t *testing.T, r *j4Router) []ContractRunReport {
	t.Helper()
	reps, err := f.s.PollContractRuns(context.Background(), ContractPollInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
		CycleID: f.cycle(t, 300), Reconcile: f.branches, Judgment: r})
	if err != nil {
		t.Fatalf("PollContractRuns: %v", err)
	}
	return reps
}

func TestJ4_ContractParentAnswerWaitIsAnsweredByResumeWithTheChosenOption(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "どちらにするか", Options: []string{"OPT-A", "OPT-B"}}, nil)}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	r := &j4Router{j4Out: []JudgmentLaunchOutput{j4Out(dec(1, "answer", "OPT-B", "NOTE-MARKER"))}}
	reps := f.pollWith(t, r)

	if len(reps) != 1 || reps[0].Held || reps[0].State != ContractStateRunning {
		t.Fatalf("reports = %+v, want the resume's running state and no hold", reps)
	}
	if len(f.client.resumes) != 1 || f.client.resumes[0][len(f.client.resumes[0])-1] != "OPT-B\nNOTE-MARKER" {
		t.Fatalf("resumes = %v, want the chosen option (with the note) carried by resume", f.client.resumes)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 0 || f.status(t, f.cid) != StatusInProgress {
		t.Errorf("holds = %d status = %s, want none and in_progress", n, f.status(t, f.cid))
	}
}

func TestJ4_ContractWithoutOptionsIsAnsweredWithText(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "名前は?"}, nil)}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	r := &j4Router{j4Out: []JudgmentLaunchOutput{j4Out(dec(1, "answer", nil, "TEXT-ANSWER"))}}
	f.pollWith(t, r)
	if len(f.client.resumes) != 1 || f.client.resumes[0][len(f.client.resumes[0])-1] != "TEXT-ANSWER" {
		t.Fatalf("resumes = %v", f.client.resumes)
	}
}

func TestJ4_ContractEscalateHoldsAndOtherWaitsDoNotCallJ4(t *testing.T) {
	cases := []struct {
		name   string
		action ContractRequestedAction
		out    []JudgmentLaunchOutput
		j4Call int
	}{
		{"escalate", ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"}, []JudgmentLaunchOutput{j4Out(dec(1, "escalate", nil, "WHY"))}, 1},
		{"approve by parent stays with the owner", ContractRequestedAction{Kind: "approve", Decider: "parent", Text: "Q"}, nil, 0},
		{"answer by human stays with the owner", ContractRequestedAction{Kind: "answer", Decider: "human", Text: "Q"}, nil, 0},
		{"answer by any stays with the owner", ContractRequestedAction{Kind: "answer", Decider: "any", Text: "Q"}, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newContractFixture(t)
			if _, err := f.start(t); err != nil {
				t.Fatal(err)
			}
			a := c.action
			f.client.queue = []ContractLaunchOutput{waitingOut(&a, nil)}
			r := &j4Router{j4Out: c.out}
			reps := f.pollWith(t, r)
			if len(reps) != 1 || !reps[0].Held || f.status(t, f.cid) != StatusAwaitingHuman {
				t.Fatalf("reports = %+v status = %s, want an owner hold", reps, f.status(t, f.cid))
			}
			if c.j4Call == 1 {
				var q string
				if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT question FROM hold`).Scan(&q) }); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(q, "WHY") {
					t.Errorf("hold question lacks the escalation reason: %s", q)
				}
			}
			if len(r.j4Seen) != c.j4Call || len(f.client.resumes) != 0 {
				t.Errorf("J4 calls = %d resumes = %v", len(r.j4Seen), f.client.resumes)
			}
		})
	}
}

func TestJ4_ContractResumeIsLimitedToThreeAnswersAndTheFourthGoesToTheOwner(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	// 答えるたびに、別の問いでまた待つ。
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"}, nil)}
	f.client.resumeOut = waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q-NEXT"}, nil)
	r := &j4Router{j4Out: []JudgmentLaunchOutput{j4Out(dec(1, "answer", nil, "A"))}}
	for i := 0; i < 4; i++ {
		f.pollWith(t, r)
	}
	if len(r.j4Seen) != 3 || len(f.client.resumes) != 3 {
		t.Fatalf("J4 calls = %d resumes = %d, want 3 and 3", len(r.j4Seen), len(f.client.resumes))
	}
	if f.status(t, f.cid) != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human after the limit", f.status(t, f.cid))
	}
}

func TestJ4_DeclaredHumanKindIsNeverGivenToJ4(t *testing.T) {
	qs := []DelegationQuestion{{Kind: "minor", Text: "x"}, {Kind: "my_kind", Text: "y"}}
	if !j4RuleEscalates(qs, []HumanQuestionKind{{ID: "my_kind", Label: "L"}}) {
		t.Error("a declared human question kind must escalate")
	}
	if j4RuleEscalates(qs, nil) {
		t.Error("undeclared minor kinds must not escalate by rule")
	}
}

func TestJ4_ContractStartKeepsAParentAnswerWaitForTheNextPollWhenJ4IsAvailable(t *testing.T) {
	f := newContractFixture(t)
	f.client.startOut = waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"}, nil)
	_, err := f.s.StartContractDelegation(context.Background(), ContractStartInput{
		ChallengeID: f.cid, ConnectorID: "tool", AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
		CycleID: f.cycle(t, 300), MaxBudgetUSD: 5, Reconcile: f.branches, Judgment: &j4Router{j4Out: []JudgmentLaunchOutput{answerAll()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.status(t, f.cid) != StatusInProgress || f.count(t, `SELECT COUNT(*) FROM hold`) != 0 {
		t.Errorf("status = %s, want in_progress without a hold (J4 handles it at the next poll)", f.status(t, f.cid))
	}
}
