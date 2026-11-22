package core

// 区切り S4b-3 のうち、契約の判断待ち（接続ツールの run が回答を待つ）をスーパーバイザーが先に受ける入口を、
// 実ストア・偽の ContractClient・偽の判断の呼び出しで確かめる（AC-20〜28・AC-31〜36）。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const resumeWithIssueConnectorsJSON = `{
  "version": 1,
  "human_question_kinds": [],
  "connectors": [
    {"id": "tool", "form": "cli", "permission_mode": "default",
     "commands": {
       "start": ["tool", "start", "--challenge", "{challenge_id}"],
       "status": ["tool", "status", "{run_id}"],
       "resume": ["tool", "resume", "{run_id}", "--issue", "{issue_number}", "--answer", "{answer}"],
       "cancel": ["tool", "cancel", "{run_id}"]
     },
     "contract_version": 1,
     "operations": [{"id": "impl", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]}
  ],
  "repos": [
    {"name": "main-repo", "remote": "o/r", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot1"]}}
  ]
}`

// contractSVFixture は、根拠のファイル docs/a.md を置いた契約の試験の部品。
func contractSVFixture(t *testing.T, connectorsJSON string) *contractFixture {
	t.Helper()
	f := newContractFixtureWith(t, connectorsJSON)
	p := filepath.Join(f.s.Workspace(), "docs", "a.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *contractFixture) waitFor(t *testing.T, ra ContractRequestedAction) {
	t.Helper()
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(&ra, nil)}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
}

func (f *contractFixture) pollSV(t *testing.T, r *svRouter) []ContractRunReport {
	t.Helper()
	reps, err := f.s.PollContractRuns(context.Background(), ContractPollInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
		CycleID: f.cycle(t, 300), Reconcile: f.branches, Judgment: r})
	if err != nil {
		t.Fatalf("PollContractRuns: %v", err)
	}
	return reps
}

func (f *contractFixture) hold(t *testing.T, col string) string {
	t.Helper()
	return f.queryString(t, `SELECT `+col+` FROM hold ORDER BY id DESC LIMIT 1`).String
}

// AC-20〜22: 答える者が親・人・どちらでもの回答の待ちは、スーパーバイザーの判断点が呼ばれる（止まり方は child_question）。
func TestContractSV_AnswerWaitGoesToTheSupervisorWhoeverAnswers(t *testing.T) {
	t.Parallel()
	for _, decider := range []string{"parent", "human", "any"} {
		t.Run(decider, func(t *testing.T) {
			f := contractSVFixture(t, contractConnectorsJSON)
			f.waitFor(t, ContractRequestedAction{Kind: "answer", Decider: decider, Text: "SV-Q"})
			r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
			f.pollSV(t, r)
			if len(r.svSeen) != 1 || !strings.Contains(sectionsText(r.svSeen[0].Sections), "child_question") || !strings.Contains(sectionsText(r.svSeen[0].Sections), "SV-Q") {
				t.Fatalf("SV calls = %d, want 1 with the child_question stop kind and the question", len(r.svSeen))
			}
			if r.j4Seen != 0 {
				t.Errorf("J4 was called")
			}
		})
	}
}

// AC-23〜25: ターミナルで人が解く待ち・承認の待ち・成果物の無いレビューの待ちは、判断点を呼ばず、オーナーの保留にする。
func TestContractSV_OtherWaitsStayWithTheOwner(t *testing.T) {
	t.Parallel()
	cases := map[string]ContractRequestedAction{
		"terminal":              {Kind: "answer", Decider: "parent", Text: "Q", ResolvableBy: "terminal"},
		"approve":               {Kind: "approve", Decider: "parent", Text: "Q"},
		"review without output": {Kind: "review", Decider: "parent", Text: "Q"},
	}
	for name, ra := range cases {
		t.Run(name, func(t *testing.T) {
			f := contractSVFixture(t, contractConnectorsJSON)
			f.waitFor(t, ra)
			r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
			reps := f.pollSV(t, r)
			if len(r.svSeen) != 0 || len(f.client.resumes) != 0 {
				t.Errorf("SV calls = %d resumes = %v, want none", len(r.svSeen), f.client.resumes)
			}
			if len(reps) != 1 || !reps[0].Held || f.status(t, f.cid) != StatusAwaitingHuman {
				t.Fatalf("reports = %+v status = %s, want an owner hold", reps, f.status(t, f.cid))
			}
		})
	}
}

// AC-26・AC-27・AC-28: 答えると、宣言の resume で、選択肢があれば選んだ選択肢・無ければ文を返し、保留は作らない。対処の記録に問いと答えと根拠が残る。
func TestContractSV_AnswerIsReturnedByResumeWithoutAHold(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		ra   ContractRequestedAction
		ans  string
		want string
	}{
		"option": {ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "どちらか", Options: []string{"OPT-A", "OPT-B"}}, "OPT-B|docs/a.md", "OPT-B"},
		"text":   {ContractRequestedAction{Kind: "answer", Decider: "human", Text: "名前は?"}, "TEXT-ANSWER|docs/a.md", "TEXT-ANSWER"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := contractSVFixture(t, contractConnectorsJSON)
			f.waitFor(t, c.ra)
			r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON(c.ans))}}
			reps := f.pollSV(t, r)

			if len(reps) != 1 || reps[0].Held || reps[0].State != ContractStateRunning {
				t.Fatalf("reports = %+v, want the resume's running state and no hold", reps)
			}
			if len(f.client.resumes) != 1 || f.client.resumes[0][len(f.client.resumes[0])-1] != c.want {
				t.Fatalf("resumes = %v, want the answer %q", f.client.resumes, c.want)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 0 || f.status(t, f.cid) != StatusInProgress {
				t.Errorf("holds = %d status = %s, want none and in_progress", n, f.status(t, f.cid))
			}
			if n := f.count(t, `SELECT COUNT(*) FROM supervisor_action WHERE hand = 'answer' AND result = 'executed' AND stop_kind = 'child_question' AND hold_id IS NULL AND questions LIKE '%docs/a.md%' AND questions LIKE '%`+c.want+`%'`); n != 1 {
				t.Errorf("answer actions = %d, want 1 with the question, the answer and the basis", n)
			}
		})
	}
}

// AC-31: escalate は、止まり方 child_question・印 escalated の保留を作り、契約の実行をその保留に結び付ける。選択肢は委譲先が返したものを使う。
func TestContractSV_EscalateHoldsTiedToTheRunWithTheChildsOptions(t *testing.T) {
	t.Parallel()
	f := contractSVFixture(t, contractConnectorsJSON)
	f.waitFor(t, ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q", Options: []string{"OPT-A", "OPT-B"}})
	// スーパーバイザーが付けた options は使わない。
	esc := strings.Replace(svEscalateJSON(), `"options":[]`, `"options":[{"label":"SV-X","description":"d"},{"label":"SV-Y","description":"d"}]`, 1)
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(esc)}}
	reps := f.pollSV(t, r)

	if len(reps) != 1 || !reps[0].Held || f.status(t, f.cid) != StatusAwaitingHuman || f.count(t, `SELECT COUNT(*) FROM hold`) != 1 {
		t.Fatalf("reports = %+v status = %s, want one owner hold", reps, f.status(t, f.cid))
	}
	for col, want := range map[string]string{"stop_kind": "child_question", "supervisor_state": "escalated", "supervisor_findings": "FINDINGS-MARKER"} {
		if got := f.hold(t, col); got != want {
			t.Errorf("%s = %q, want %q", col, got, want)
		}
	}
	if opts := f.hold(t, "options"); !strings.Contains(opts, "OPT-A") || !strings.Contains(opts, "OPT-B") || strings.Contains(opts, "SV-X") {
		t.Errorf("options = %s, want the child's options only", opts)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run c JOIN hold h ON h.id = c.wait_hold_id`); n != 1 {
		t.Errorf("the contract run is not tied to the hold")
	}
	// オーナーの回答が resume で返る。
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.answerLatestHold(t, f.cid, "OPT-A")
	f.pollSV(t, r)
	if len(f.client.resumes) != 1 || f.client.resumes[0][len(f.client.resumes[0])-1] != "OPT-A" {
		t.Errorf("resumes = %v, want the owner's answer", f.client.resumes)
	}
}

// AC-32: 宣言の supervisor が偽のとき、判断点を呼ばず、保留にする（印は NULL）。
func TestContractSV_SupervisorOffHoldsWithoutCalling(t *testing.T) {
	t.Parallel()
	f := contractSVFixture(t, contractConnectorsJSON)
	f.agent.Supervisor = false
	f.waitFor(t, ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"})
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("A|docs/a.md"))}}
	reps := f.pollSV(t, r)
	if len(r.svSeen) != 0 || len(f.client.resumes) != 0 || len(reps) != 1 || !reps[0].Held {
		t.Fatalf("SV calls = %d resumes = %v reports = %+v, want a hold without a call", len(r.svSeen), f.client.resumes, reps)
	}
	if f.queryString(t, `SELECT supervisor_state FROM hold`).Valid {
		t.Errorf("supervisor_state is set, want NULL")
	}
}

// 検査を通らない答え（選択肢にない値・根拠が無い・根拠のファイルが無い）は、委譲先へ返さず、failed の記録を残して保留にする。
func TestContractSV_BadAnswersAreNotReturned(t *testing.T) {
	t.Parallel()
	opts := ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q", Options: []string{"OPT-A", "OPT-B"}}
	cases := map[string]struct {
		ra  ContractRequestedAction
		ans string
	}{
		"not an option":     {opts, "OPT-Z|docs/a.md"},
		"no basis":          {ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"}, "A"},
		"basis not on disk": {ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"}, "A|docs/none.md"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := contractSVFixture(t, contractConnectorsJSON)
			f.waitFor(t, c.ra)
			r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON(c.ans))}}
			f.pollSV(t, r)
			if len(f.client.resumes) != 0 || f.count(t, `SELECT COUNT(*) FROM hold`) != 1 {
				t.Fatalf("resumes = %v holds = %d, want no resume and one hold", f.client.resumes, f.count(t, `SELECT COUNT(*) FROM hold`))
			}
			if n := f.count(t, `SELECT COUNT(*) FROM supervisor_action WHERE result = 'failed' AND stop_kind = 'child_question'`); n != 1 {
				t.Errorf("failed actions = %d, want 1", n)
			}
		})
	}
}

// AC-33・AC-34・AC-35: resume の引数を埋められなかったとき、委譲先へ返さず、固定の 1 行つきの保留にして、対処の記録の結果は failed。
func TestContractSV_UnfillableResumeIsEscalatedWithTheFixedLine(t *testing.T) {
	t.Parallel()
	f := contractSVFixture(t, contractConnectorsJSON)
	f.waitFor(t, ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"})
	// 起動の後で、resume が課題の取り込み元（この課題には無い）を要る宣言に替える。
	conn, err := parseConnectorsDeclaration([]byte(resumeWithIssueConnectorsJSON))
	if err != nil {
		t.Fatal(err)
	}
	f.conn = conn
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("A|docs/a.md"))}}
	f.pollSV(t, r)

	if len(f.client.resumes) != 0 || f.count(t, `SELECT COUNT(*) FROM hold`) != 1 {
		t.Fatalf("resumes = %v holds = %d, want no resume and one hold", f.client.resumes, f.count(t, `SELECT COUNT(*) FROM hold`))
	}
	if first := strings.SplitN(f.hold(t, "question"), "\n", 2)[0]; first != "スーパーバイザーの答えを、委譲先へ返せなかった。" {
		t.Errorf("first line = %q", first)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM supervisor_action WHERE hand = 'answer' AND result = 'failed'`); n != 1 {
		t.Errorf("failed answer actions = %d, want 1", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM supervisor_action WHERE result = 'executed'`); n != 0 {
		t.Errorf("executed actions = %d, want 0 (the answer is not recorded)", n)
	}
}

// 答えによる再開は 3 回まで。4 回目は判断点を呼ばず、固定の 1 行つきで上げる（止まり方 child_question・印 NULL・対処の記録は増えない）。
func TestContractSV_FourthAnswerGoesToTheOwnerWithoutCalling(t *testing.T) {
	t.Parallel()
	f := contractSVFixture(t, contractConnectorsJSON)
	f.waitFor(t, ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"})
	f.client.resumeOut = waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q-NEXT"}, nil)
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("A|docs/a.md"))}}
	for i := 0; i < 4; i++ {
		f.pollSV(t, r)
	}
	if len(r.svSeen) != 3 || len(f.client.resumes) != 3 {
		t.Fatalf("SV calls = %d resumes = %d, want 3 and 3", len(r.svSeen), len(f.client.resumes))
	}
	if f.status(t, f.cid) != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", f.status(t, f.cid))
	}
	if first := strings.SplitN(f.hold(t, "question"), "\n", 2)[0]; first != "スーパーバイザーが答えて再開できる回数の上限（3 回）に達した。" {
		t.Errorf("first line = %q", first)
	}
	if f.hold(t, "stop_kind") != "child_question" || f.queryString(t, `SELECT supervisor_state FROM hold`).Valid {
		t.Errorf("want child_question with NULL supervisor_state")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM supervisor_action`); n != 3 {
		t.Errorf("actions = %d, want 3", n)
	}
}

// start の直後の waiting は、スーパーバイザーへ回せるなら保留にせず、次の周の読みで扱う。
func TestContractSV_StartKeepsAnAnswerWaitForTheNextPoll(t *testing.T) {
	t.Parallel()
	f := contractSVFixture(t, contractConnectorsJSON)
	f.client.startOut = waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "human", Text: "Q"}, nil)
	_, err := f.s.StartContractDelegation(context.Background(), ContractStartInput{
		ChallengeID: f.cid, ConnectorID: "tool", AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
		CycleID: f.cycle(t, 300), MaxBudgetUSD: 5, Reconcile: f.branches, Judgment: &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.status(t, f.cid) != StatusInProgress || f.count(t, `SELECT COUNT(*) FROM hold`) != 0 {
		t.Errorf("status = %s, want in_progress without a hold", f.status(t, f.cid))
	}
}

// 答えを返す resume が失敗したら保留にし、保留の間の周は resume も判断点もやり直さない。オーナーが答えて戻った後は、前の答えを返し直さず改めて問う。
func TestContractSV_FailedResumeHoldsAndIsNotRetried(t *testing.T) {
	t.Parallel()
	for _, res := range []RunResult{RunResultTimedOut, RunResultErrored} {
		t.Run(string(res), func(t *testing.T) {
			f := contractSVFixture(t, contractConnectorsJSON)
			f.waitFor(t, ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"})
			f.client.resumeOut = ContractLaunchOutput{Result: res, ErrorSummary: "RESUME-BOOM"}
			r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("A|docs/a.md"))}}
			reps := f.pollSV(t, r)
			if len(reps) != 1 || !reps[0].Held || len(f.client.resumes) != 1 || len(r.svSeen) != 1 {
				t.Fatalf("reports = %+v resumes = %d SV calls = %d", reps, len(f.client.resumes), len(r.svSeen))
			}
			if qs := f.holdQuestions(t); len(qs) != 1 || !strings.Contains(qs[0], "RESUME-BOOM") || !strings.Contains(qs[0], "返し直さない") {
				t.Fatalf("questions = %q", qs)
			}
			f.pollSV(t, r)
			if len(f.client.resumes) != 1 || len(r.svSeen) != 1 {
				t.Fatalf("resumes = %d SV calls = %d, want 1 and 1", len(f.client.resumes), len(r.svSeen))
			}
			f.answerLatestHold(t, f.cid, "確かめた")
			f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
			f.pollSV(t, r)
			if len(r.svSeen) != 2 || len(f.client.resumes) != 2 {
				t.Errorf("SV calls = %d resumes = %d, want a fresh SV call and one resume", len(r.svSeen), len(f.client.resumes))
			}
		})
	}
}

// 宣言の resume が答えの差し込みを含まないとき、答えは委譲先へ渡らないので、答えを記録せず失敗の記録にして上げる。
func TestContractSV_ResumeWithoutAnswerPlaceholderIsEscalated(t *testing.T) {
	t.Parallel()
	f := contractSVFixture(t, strings.Replace(contractConnectorsJSON, `, "--answer", "{answer}"`, "", 1))
	f.waitFor(t, ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "Q"})
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("A|docs/a.md"))}}
	f.pollSV(t, r)
	if len(f.client.resumes) != 0 || f.count(t, `SELECT COUNT(*) FROM hold`) != 1 ||
		f.count(t, `SELECT COUNT(*) FROM supervisor_action WHERE result = 'failed'`) != 1 || f.count(t, `SELECT COUNT(*) FROM supervisor_action WHERE result = 'executed'`) != 0 {
		t.Errorf("want no resume, one hold, one failed action and no executed action")
	}
}
