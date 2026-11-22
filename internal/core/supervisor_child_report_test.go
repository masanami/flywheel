package core

// 区切り S4b-3 のうち、結末が「問い」の委譲の報告（子の問い）をスーパーバイザーが受ける入口を、実ストアで確かめる
// （AC-4〜AC-17・AC-53・AC-54・AC-56）。契約の判断待ちはこのファイルの範囲外。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// svRouter は判断点 SV に決めた出力を返す偽の JudgmentInvoker。SV 以外は J3 のブリーフを返す。J4 が呼ばれたら記録する。
type svRouter struct {
	mu     sync.Mutex
	svOut  []JudgmentLaunchOutput // 呼ぶたびに先頭から返す。尽きたら最後の値を返す
	svSeen []JudgmentLaunchInput
	j4Seen int
}

func (r *svRouter) Available(context.Context) error { return nil }

func (r *svRouter) InvokeJudgment(_ context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch in.Judgment {
	case JudgmentSV:
		r.svSeen = append(r.svSeen, in)
		out := r.svOut[len(r.svOut)-1]
		if len(r.svOut) > 1 {
			r.svOut = r.svOut[1:]
		}
		return out, nil
	case JudgmentJ4:
		r.j4Seen++
	}
	return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: briefJSON("BRIEF-MARKER")}, nil
}

func svJSON(out string) JudgmentLaunchOutput {
	return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(out), ReportedTotalCostUSD: float64Ptr(0.5)}
}

// svEscalateJSON は escalate の出力（調べたこと FINDINGS-MARKER・推奨 RECOMMEND-MARKER）。
func svEscalateJSON() string {
	b, _ := json.Marshal(map[string]any{
		"hand": "escalate", "findings": "FINDINGS-MARKER", "note": "", "basis": []any{}, "recommendation": "RECOMMEND-MARKER",
		"options": []any{}, "conditions": []any{}, "answers": []any{},
	})
	return string(b)
}

// childReportFixture は、根拠のファイル docs/a.md を置いた委譲の試験の部品。
func childReportFixture(t *testing.T) *delegateFixture {
	t.Helper()
	f := newResumeFixture(t)
	p := filepath.Join(f.s.Workspace(), "docs", "a.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *delegateFixture) runWithSV(t *testing.T, r *svRouter, id string) {
	t.Helper()
	in := f.input(f.cycle(t, 300), &id)
	in.Judgment = r
	if _, err := f.s.RunDelegation(context.Background(), in); err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
}

// humanKindsOut は、問いの種類がどちらも「人に聞く種類」（組み込みの scope）に当たる報告。
func humanKindsOut() JudgmentLaunchOutput {
	return JudgmentLaunchOutput{Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(1), StructuredOutput: reportJSON(func(m map[string]any) {
		m["outcome"] = "questions"
		m["branch"] = "feat/x"
		m["questions"] = []any{
			map[string]any{"kind": "scope", "text": "QUESTION-ONE", "options": []string{}, "recommendation": nil},
			map[string]any{"kind": "scope", "text": "QUESTION-TWO", "options": []string{}, "recommendation": nil},
		}
	})}
}

func (f *delegateFixture) holdCount(t *testing.T) int {
	return f.countRows(t, `SELECT COUNT(*) FROM hold`)
}

// AC-4・AC-5: 「人に聞く種類」に当たらない問いは、止まり方 child_question でスーパーバイザーの判断点が呼ばれ、J4 の記録は作られない。
func TestChildReport_AC4_AC5_SupervisorIsCalledAndJ4IsNot(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = minorQuestionsOut()
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
	f.runWithSV(t, r, id)

	if len(r.svSeen) != 1 || !strings.Contains(sectionsText(r.svSeen[0].Sections), "child_question") || !strings.Contains(sectionsText(r.svSeen[0].Sections), "QUESTION-TWO") {
		t.Fatalf("SV calls = %d, want 1 with the child_question stop kind and the questions", len(r.svSeen))
	}
	if r.j4Seen != 0 || f.countRows(t, `SELECT COUNT(*) FROM run WHERE judgment = 'J4'`) != 0 {
		t.Errorf("J4 was called (%d) or recorded", r.j4Seen)
	}
}

// AC-6・AC-7・AC-8・AC-9: 「人に聞く種類」に当たる問いを 1 件でも含む報告は、判断点を呼ばず、全部の問いを 1 つの保留にする（印は NULL）。
func TestChildReport_AC6_AC7_AC8_AC9_HumanKindsSkipTheSupervisor(t *testing.T) {
	t.Parallel()
	for name, out := range map[string]JudgmentLaunchOutput{"all human kinds": humanKindsOut(), "one human kind": questionsOut("feat/x")} {
		t.Run(name, func(t *testing.T) {
			f := childReportFixture(t)
			id := f.newInProgress(t, "t", "P1", planSpec(nil))
			f.deleg.result = out
			r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
			f.runWithSV(t, r, id)

			if len(r.svSeen) != 0 {
				t.Errorf("SV calls = %d, want 0", len(r.svSeen))
			}
			if n := f.holdCount(t); n != 1 {
				t.Fatalf("holds = %d, want 1", n)
			}
			q := f.queryString(t, `SELECT question FROM hold`)
			for _, want := range []string{"QUESTION-ONE", "QUESTION-TWO"} {
				if !strings.Contains(q, want) {
					t.Errorf("hold question lacks %q:\n%s", want, q)
				}
			}
			if got := f.queryString(t, `SELECT stop_kind FROM hold`); got != "child_question" {
				t.Errorf("stop_kind = %q, want child_question", got)
			}
			if n := f.countRows(t, `SELECT COUNT(*) FROM hold WHERE supervisor_state IS NULL`); n != 1 {
				t.Errorf("holds with NULL supervisor_state = %d, want 1", n)
			}
		})
	}
}

// AC-10: 委譲の意思決定者が人でも、問いの種類が「人に聞く種類」に当たらなければ、スーパーバイザーの判断点が呼ばれる。
func TestChildReport_AC10_HumanDeciderStillGoesToTheSupervisor(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(func(m map[string]any) { m["operation"] = "define-human" }))
	f.deleg.result = minorQuestionsOut()
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
	f.runWithSV(t, r, id)

	if runs := f.delegateRuns(t); len(runs) != 1 || runs[0].Decider != DeciderHuman {
		t.Fatalf("delegate runs = %+v, want one run decided by human", runs)
	}
	if len(r.svSeen) != 1 {
		t.Errorf("SV calls = %d, want 1", len(r.svSeen))
	}
}

// AC-11・AC-12: 全部の問いに根拠つきで答えると、保留は作られず、同じ周の次の起動が答えを渡して委譲先を再開する。
func TestChildReport_AC11_AC12_AnswerResumesTheChildWithoutAHold(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{minorQuestionsOut(), {Result: RunResultSucceeded, StructuredOutput: reportJSON(nil), ReportedTotalCostUSD: float64Ptr(1)}}
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("B|docs/a.md", "ANSWER-TWO|docs/a.md"))}}
	f.runWithSV(t, r, id)

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
	if n := f.countRows(t, `SELECT COUNT(*) FROM hold WHERE question LIKE '%QUESTION-%'`); n != 0 {
		t.Errorf("holds about the questions = %d, want 0", n)
	}
	// 答えと根拠が、対処の記録に残る。
	if n := f.countRows(t, `SELECT COUNT(*) FROM supervisor_action WHERE hand = 'answer' AND result = 'executed' AND stop_kind = 'child_question' AND hold_id IS NULL AND questions LIKE '%docs/a.md%'`); n != 1 {
		t.Errorf("answer actions = %d, want 1", n)
	}
}

// 答えを渡す再開が同じ周で行えなくても、答えは記録に残り、次の起動の決定が同じ答えを渡して再開する（新しいセッションで始めない）。
func TestChildReport_PendingSupervisorAnswerIsResumedByTheNextLaunchDecision(t *testing.T) {
	t.Parallel()
	pv := int64(1)
	deleg := runRow{ID: "R-1", Kind: runKindDelegate, SessionID: "sess", Result: RunResultSucceeded, PlanVersion: &pv,
		Output: string(minorQuestionsOut().StructuredOutput)}
	sv := runRow{ID: "R-2", Kind: runKindJudgment, Judgment: JudgmentSV, Result: RunResultSucceeded, Output: answerJSON("B|docs/a.md", "ANSWER-TWO|docs/a.md")}
	h := &launchHistory{Runs: []runRow{deleg}, J4Runs: []runRow{sv}}
	resume, limit := h.decideLaunch(1, 2)
	if limit != nil || resume == nil || resume.Kind != ResumeKindAnswer || resume.Target.ID != "R-1" ||
		!strings.Contains(resume.Answer, "ANSWER-TWO") || !strings.Contains(resume.Question, "QUESTION-ONE") {
		t.Fatalf("resume = %+v limit = %+v", resume, limit)
	}
	// 上げた（escalate）スーパーバイザーの出力は、再開に使わない。
	h = &launchHistory{Runs: []runRow{deleg}, J4Runs: []runRow{{ID: "R-2", Kind: runKindJudgment, Judgment: JudgmentSV, Result: RunResultSucceeded, Output: svEscalateJSON()}}}
	if resume, _ := h.decideLaunch(1, 2); resume != nil {
		t.Errorf("resume = %+v, want none for an escalating supervisor", resume)
	}
}

// AC-13〜AC-17: escalate は、全部の問いを 1 つの保留にして上げる（止まり方 child_question・印 escalated・調べたことを保留に残す）。
func TestChildReport_AC13_to_AC17_EscalateHoldsAllQuestionsOnce(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = minorQuestionsOut()
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
	f.runWithSV(t, r, id)

	if n := f.holdCount(t); n != 1 {
		t.Fatalf("holds = %d, want 1", n)
	}
	q := f.queryString(t, `SELECT question FROM hold`)
	for _, want := range []string{"QUESTION-ONE", "QUESTION-TWO"} {
		if !strings.Contains(q, want) {
			t.Errorf("hold question lacks %q:\n%s", want, q)
		}
	}
	for col, want := range map[string]string{"stop_kind": "child_question", "supervisor_state": "escalated", "supervisor_findings": "FINDINGS-MARKER", "supervisor_recommendation": "RECOMMEND-MARKER"} {
		if got := f.queryString(t, `SELECT `+col+` FROM hold`); got != want {
			t.Errorf("%s = %q, want %q", col, got, want)
		}
	}
	if f.detail(t, id).Status != StatusAwaitingHuman || len(f.deleg.launched()) != 1 {
		t.Errorf("status = %s launches = %d, want awaiting_human and 1", f.detail(t, id).Status, len(f.deleg.launched()))
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM supervisor_action a JOIN hold h ON h.supervisor_action_id = a.id WHERE a.hand = 'escalate' AND a.result = 'executed'`); n != 1 {
		t.Errorf("the hold is not tied to an executed escalate action")
	}
	// 対処の記録の側も、できた保留を指す（保留が無いのは、答えて保留を作らなかったときだけ）。
	if n := f.countRows(t, `SELECT COUNT(*) FROM supervisor_action a JOIN hold h ON a.hold_id = h.id WHERE a.hand = 'escalate'`); n != 1 {
		t.Errorf("the escalate action does not point at the hold it produced")
	}
}

// 答えの件数・根拠が検査を通らない出力は、委譲先へ答えを返さず、failed の対処の記録を残して全部の問いを上げる。
func TestChildReport_BadAnswerIsEscalatedAsFailed(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = minorQuestionsOut()
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("B|docs/a.md"))}} // 2 問に 1 件
	f.runWithSV(t, r, id)

	if len(f.deleg.launched()) != 1 || f.holdCount(t) != 1 {
		t.Fatalf("launches = %d holds = %d, want 1 and 1", len(f.deleg.launched()), f.holdCount(t))
	}
	if q := f.queryString(t, `SELECT question FROM hold`); !strings.HasPrefix(q, supervisorFailedQuestionHead) || !strings.Contains(q, "QUESTION-TWO") {
		t.Errorf("question = %q", q)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM supervisor_action WHERE result = 'failed' AND stop_kind = 'child_question'`); n != 1 {
		t.Errorf("failed actions = %d, want 1", n)
	}
}

// AC-53・AC-54: 宣言の supervisor が偽のとき、判断点を呼ばず、印が NULL の保留になる。
func TestChildReport_AC53_AC54_SupervisorOffHoldsWithoutCalling(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	f.agent.Supervisor = false
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = minorQuestionsOut()
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("B|docs/a.md", "x|docs/a.md"))}}
	f.runWithSV(t, r, id)

	if len(r.svSeen) != 0 || f.holdCount(t) != 1 {
		t.Fatalf("SV calls = %d holds = %d, want 0 and 1", len(r.svSeen), f.holdCount(t))
	}
	if got := f.queryString(t, `SELECT stop_kind FROM hold`); got != "child_question" {
		t.Errorf("stop_kind = %q", got)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM hold WHERE supervisor_state IS NULL`); n != 1 {
		t.Errorf("holds with NULL supervisor_state = %d, want 1", n)
	}
}

// 再開の上限（3 回）の後は、判断点を呼ばず、上限の固定の 1 行を付けた保留になる。
func TestChildReport_AnswerLimitStopsTheFourthQuestion(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = minorQuestionsOut() // 毎回また問いで止まる
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("B|docs/a.md", "x|docs/a.md"))}}
	f.runWithSV(t, r, id)

	if got := len(f.deleg.launched()); got != 4 {
		t.Errorf("launches = %d, want 4 (1 + 3 resumes)", got)
	}
	if len(r.svSeen) != 3 {
		t.Errorf("SV calls = %d, want 3 (the 4th is not called)", len(r.svSeen))
	}
	if q := f.queryString(t, `SELECT question FROM hold`); !strings.HasPrefix(q, supervisorAnswerLimitNotice+"\n") {
		t.Errorf("question lacks the limit line:\n%s", q)
	}
}

// AC-56: 判断点を呼ばずに上げた子の問いだけがあったサイクルの、スーパーバイザーの段の処理した件数は 0。
func TestChildReport_AC56_UncalledChildQuestionIsNotProcessedBySuperviseStage(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = humanKindsOut()
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
	f.runWithSV(t, r, id)

	res, err := f.s.superviseStage(context.Background(), CycleRunInput{AgentDecl: f.agent, Invoker: r}, f.cycle(t, 300), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.SuperviseProcessed != 0 || len(r.svSeen) != 0 {
		t.Errorf("processed = %d, SV calls = %d, want 0 and 0", res.SuperviseProcessed, len(r.svSeen))
	}
}

// 宣言の「人に聞く種類」（human_question_kinds）に当たる問いは、組み込みの種類でなくても、判断点を呼ばずに保留にする（AC-6 の宣言側）。
func TestChildReport_DeclaredHumanKindSkipsTheSupervisor(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	f.conn.HumanQuestionKinds = append(f.conn.HumanQuestionKinds, HumanQuestionKind{ID: "minor", Label: "L"})
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = minorQuestionsOut()
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
	f.runWithSV(t, r, id)
	if len(r.svSeen) != 0 || f.holdCount(t) != 1 {
		t.Errorf("SV calls = %d holds = %d, want 0 and 1", len(r.svSeen), f.holdCount(t))
	}
}

// スーパーバイザーの実行が失敗したときは、委譲先へ答えを返さず、failed の対処の記録と、「判断できなかった」の 1 行つきの escalated の保留を残す。
func TestChildReport_FailedSupervisorRunIsEscalatedAsFailed(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = minorQuestionsOut()
	r := &svRouter{svOut: []JudgmentLaunchOutput{{Result: RunResultErrored, ErrorSummary: "boom"}}}
	f.runWithSV(t, r, id)

	if len(f.deleg.launched()) != 1 || f.holdCount(t) != 1 {
		t.Fatalf("launches = %d holds = %d, want 1 and 1", len(f.deleg.launched()), f.holdCount(t))
	}
	if q := f.queryString(t, `SELECT question FROM hold`); !strings.HasPrefix(q, supervisorFailedQuestionHead) {
		t.Errorf("question = %q", q)
	}
	if got := f.queryString(t, `SELECT supervisor_state FROM hold`); got != "escalated" {
		t.Errorf("supervisor_state = %q, want escalated", got)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM supervisor_action WHERE result = 'failed' AND stop_kind = 'child_question'`); n != 1 {
		t.Errorf("failed actions = %d, want 1", n)
	}
}

// 記録された答え（executed の対処の記録）が指さないスーパーバイザーの run の答えでは、再開しない（読み込みは記録を条件にする）。
func TestChildReport_ResumeSourceRequiresAnExecutedAnswerRecord(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{minorQuestionsOut(), {Result: RunResultSucceeded, StructuredOutput: reportJSON(nil), ReportedTotalCostUSD: float64Ptr(1)}}
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("B|docs/a.md", "x|docs/a.md"))}}
	f.runWithSV(t, r, id)
	if n := f.countRows(t, `SELECT COUNT(*) FROM supervisor_action WHERE hand = 'answer' AND result = 'executed' AND run_id IS NOT NULL`); n != 1 {
		t.Fatalf("answer actions with a run = %d, want 1", n)
	}
}
