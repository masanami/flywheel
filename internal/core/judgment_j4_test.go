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

func (f *delegateFixture) countRows(t *testing.T, q string) int {
	t.Helper()
	n := 0
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestValidateJ4Output(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	var m map[string]any
	if err := json.Unmarshal(j4OutputSchema(), &m); err != nil {
		t.Fatal(err)
	}
	enum := m["properties"].(map[string]any)["decisions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["decision"].(map[string]any)["enum"]
	if !reflect.DeepEqual(enum, []any{"answer", "escalate"}) {
		t.Errorf("enum = %v", enum)
	}
}

// 同じ周で再開できなくても、回答は run に残り、次の起動が同じ回答を渡して再開する（新しいセッションで始めない）。
func TestJ4_PendingAnswerIsResumedByTheNextLaunchDecision(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	qs := []DelegationQuestion{{Kind: "minor", Text: "x"}, {Kind: "my_kind", Text: "y"}}
	if !j4RuleEscalates(qs, []HumanQuestionKind{{ID: "my_kind", Label: "L"}}) {
		t.Error("a declared human question kind must escalate")
	}
	if j4RuleEscalates(qs, nil) {
		t.Error("undeclared minor kinds must not escalate by rule")
	}
}

func TestJ4_ContractStartKeepsAParentAnswerWaitForTheNextPollWhenJ4IsAvailable(t *testing.T) {
	t.Parallel()
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

// J4 の答えを返す resume が失敗したら、保留にして、オーナーが答えて着手中へ戻っても同じ答えを返し直さない。
func TestJ4_ContractFailedResumeOfTheAnswerHoldsAndIsNotRetried(t *testing.T) {
	t.Parallel()
	for _, res := range []RunResult{RunResultTimedOut, RunResultErrored} {
		t.Run(string(res), func(t *testing.T) {
			f := newContractFixture(t)
			if _, err := f.start(t); err != nil {
				t.Fatal(err)
			}
			f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"}, nil)}
			f.client.resumeOut = ContractLaunchOutput{Result: res, ErrorSummary: "RESUME-BOOM"}
			r := &j4Router{j4Out: []JudgmentLaunchOutput{j4Out(dec(1, "answer", nil, "J4-ANSWER"))}}
			reps := f.pollWith(t, r)

			if len(r.j4Seen) != 1 || len(f.client.resumes) != 1 {
				t.Fatalf("J4 calls = %d resumes = %v, want 1 and 1", len(r.j4Seen), f.client.resumes)
			}
			if len(reps) != 1 || !reps[0].Held || f.status(t, f.cid) != StatusAwaitingHuman {
				t.Fatalf("reports = %+v status = %s, want a hold", reps, f.status(t, f.cid))
			}
			qs := f.holdQuestions(t)
			if len(qs) != 1 || !strings.Contains(qs[0], "resume") || !strings.Contains(qs[0], "RESUME-BOOM") || !strings.Contains(qs[0], "返し直さない") {
				t.Fatalf("questions = %q, want the resume failure and no automatic retry", qs)
			}
			// 保留の間の周は、失敗した resume をやり直さず、J4 も呼び直さない。
			f.pollWith(t, r)
			f.pollWith(t, r)
			if len(f.client.resumes) != 1 || len(r.j4Seen) != 1 {
				t.Fatalf("resumes = %d J4 calls = %d, want 1 and 1 (the failed resume is not retried)", len(f.client.resumes), len(r.j4Seen))
			}
			// オーナーが答えて着手中へ戻った後も run がまだ同じ waiting なら、前の答えを返し直すのではなく改めて問う
			// （仕様 §答えの返し「次の周の状態の確認でまだ waiting なら改めて問う」。J4 の新しい呼び出しで、再開の上限に数える）。
			f.answerLatestHold(t, f.cid, "確かめた")
			f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
			f.pollWith(t, r)
			if len(r.j4Seen) != 2 || len(f.client.resumes) != 2 {
				t.Fatalf("J4 calls = %d resumes = %d, want a fresh J4 call and one resume", len(r.j4Seen), len(f.client.resumes))
			}
		})
	}
}
