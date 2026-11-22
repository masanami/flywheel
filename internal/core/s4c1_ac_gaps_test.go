package core

import (
	"context"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4c-1 の受入基準のうち、その場面を直接固定する試験が無かったもの（AC-19・20・71・73・102・104・106・108〜110・130・155・184）。
// 実ストアと偽の判断の IF で確かめる（既存の準備: newExpandFixture・expandDoneFixture・overLimitHeld・answerHoldFull）。

// 上限を書かなかった課題（上限の列が NULL）の、足せる着手中の課題。上限は見込み（10 USD）と同じ額として扱われる。
func expandNoLimitFixture(t *testing.T) *paFixture {
	t.Helper()
	f := expandDoneFixture(t)
	coretest.Exec(t, f.s.Workspace(), `UPDATE challenge SET budget_limit_micros = NULL, budget_limit_reason = NULL WHERE id = ?`, f.cid())
	if l, r := f.limitColumns(t); l.Valid || r.Valid {
		t.Fatalf("limit columns = %+v / %+v, want NULL", l, r)
	}
	return f
}

// AC-19・AC-20: 上限の列が NULL の課題で add（足すと見込みを超える）なら、タスクの行は増えず、課題は task_add_over_limit の保留になる。
func TestAC19_AC20_AddWithANullLimitMakesNoTaskRowAndHoldsAsTaskAddOverLimit(t *testing.T) {
	t.Parallel()
	f := expandNoLimitFixture(t)
	f.expand(t, &expandRecorder{out: []byte(expandAddOutput(nil))}) // 10 + 3 = 13 > 見込み 10（＝上限）

	// AC-19
	if n := len(taskRows(t, f.s, f.cid())); n != 2 {
		t.Errorf("AC-19: tasks = %d, want 2 (no task row is made)", n)
	}
	if f.taskByKey(t, "c") != nil {
		t.Errorf("AC-19: task c was made")
	}
	// AC-20
	if st := f.detail(t, f.id).Status; st != StatusAwaitingHuman {
		t.Fatalf("AC-20: status = %s, want awaiting_human", st)
	}
	h, reason := f.lastHold(t)
	if reason != string(holdReasonTaskAddOverLimit) || !h.Open() {
		t.Errorf("AC-20: hold = %+v reason %q, want an open task_add_over_limit hold", h, reason)
	}
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionAwaitingOwner {
		t.Errorf("additions = %+v, want one awaiting_owner", adds)
	}
}

// AC-71・AC-73: 足した後の見込みが上限と同じ額なら、タスクの行ができ、課題は保留にならない。
func TestAC71_AC73_AddExactlyAtTheLimitMakesTheTaskRowAndDoesNotHold(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(expandAddOutput(func(ts []map[string]any) {
		ts[0]["budget_impl_usd"], ts[0]["budget_review_usd"] = 3.0, 1.0 // 10 + 4 = 上限 14
	}))})

	// AC-71
	c := f.taskByKey(t, "c")
	if c == nil || c.Status != TaskStatusPending {
		t.Fatalf("AC-71: task c = %+v, want a pending task row", c)
	}
	if n := len(taskRows(t, f.s, f.cid())); n != 3 {
		t.Errorf("AC-71: tasks = %d, want 3", n)
	}
	// AC-73
	d := f.detail(t, f.id)
	if d.Status != StatusInProgress {
		t.Errorf("AC-73: status = %s, want in_progress (not held)", d.Status)
	}
	for _, h := range d.Holds {
		if h.Open() {
			t.Errorf("AC-73: open hold = %+v, want none", h)
		}
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ?`, f.cid()); n != 0 {
		t.Errorf("AC-73: holds = %d, want 0", n)
	}
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionApplied {
		t.Errorf("additions = %+v, want applied", adds)
	}
}

// budgetLimitActivity は課題の作業ログのうち、上限を書いたもの（action=budget_limit）の最後の 1 件。
func (f *paFixture) lastBudgetLimitActivity(t *testing.T) Activity {
	t.Helper()
	acts, err := f.s.ListActivities(context.Background(), &f.id)
	if err != nil {
		t.Fatal(err)
	}
	var out []Activity
	for _, a := range acts {
		if a.Action == ActivityActionBudgetLimit {
			out = append(out, a)
		}
	}
	if len(out) != 1 {
		t.Fatalf("budget_limit activities = %d, want 1", len(out))
	}
	return out[0]
}

// AC-102・AC-104: 足す依頼の保留に add で答えて上限が上がると、作業ログに上限の前の額と本人確認の方式が残る。
func TestAC102_AC104_AddAnswerLogsTheLimitBeforeAndTheVerification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		att        func(t *testing.T, id string) Attestation
		wantMethod Verification
	}{
		{"relayed", func(t *testing.T, id string) Attestation { return relayedAttestation(t, id, "sess", "quote") }, VerificationRelayed},
		{"tty_confirm", ttyAtt, VerificationTTYConfirm},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := overLimitHeld(t) // 上限 14・足した後の見込み 15
			prev, err := f.s.PrepareAnswerOption(context.Background(), f.id, "", taskAddOptionAdd, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: prev.Version, Option: taskAddOptionAdd, Agent: f.agent, Conn: f.conn}, tc.att(t, f.id)); err != nil {
				t.Fatal(err)
			}
			if got := f.limitMicros(t); got != 15_000_000 {
				t.Fatalf("limit = %d, want 15000000 (raised)", got)
			}
			a := f.lastBudgetLimitActivity(t)
			// AC-102: 上限の前の額
			if string(a.Before) != `{"limit_usd":14}` {
				t.Errorf("AC-102: before = %s, want {\"limit_usd\":14}", a.Before)
			}
			// AC-104: 本人確認の方式
			if a.Verification != string(tc.wantMethod) {
				t.Errorf("AC-104: verification = %q, want %q", a.Verification, tc.wantMethod)
			}
			if want := `{"limit_usd":15,"verification":"` + string(tc.wantMethod) + `"}`; string(a.After) != want {
				t.Errorf("AC-104: after = %s, want %s", a.After, want)
			}
		})
	}
}

// AC-106: 足す依頼の保留に add で答えて作られたタスクの前提は、足す依頼に保存した判断点の出力の前提である（前提が空でない出力）。
func TestAC106_AddAnswerCreatesTheTaskWithTheDependenciesOfTheSavedOutput(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(expandAddOutput(func(ts []map[string]any) {
		ts[0]["budget_impl_usd"], ts[0]["budget_review_usd"] = 3.0, 2.0 // 15 > 上限 14
		ts[0]["depends_on"] = []string{"b"}                             // 全タスクでも起点のタスク（a）でもない部分集合
	}))})
	if st := f.detail(t, f.id).Status; st != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", st)
	}
	if _, err := f.answerHold(t, taskAddOptionAdd); err != nil {
		t.Fatal(err)
	}
	a, b, c := f.taskByKey(t, "a"), f.taskByKey(t, "b"), f.taskByKey(t, "c")
	if c == nil {
		t.Fatal("task c was not made")
	}
	if len(c.DependsOn) != 1 || c.DependsOn[0] != b.ID {
		t.Errorf("c.DependsOn = %v, want [%s] (the saved output's dependencies; not %s)", c.DependsOn, b.ID, a.ID)
	}
}

// AC-108・AC-109・AC-110: 保留の間に --limit の経路（PrepareBudgetLimit → ExecuteBudget）で上限だけを足した後の見込み以上へ上げても、
// タスクの行は増えず、保留は開いたままで、足す依頼は awaiting_owner のまま。
func TestAC108_AC109_AC110_RaisingOnlyTheLimitDuringTheHoldChangesNeitherTasksHoldNorAddition(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t) // 上限 14・足した後の見込み 15
	before := f.detail(t, f.id)

	prev, err := f.s.PrepareBudgetLimit(context.Background(), f.id, f.agent, 15)
	if err != nil {
		t.Fatalf("PrepareBudgetLimit: %v", err)
	}
	limit := 15.0
	if _, err := f.s.ExecuteBudget(context.Background(), BudgetRequest{
		ChallengeID: f.id, ExpectedVersion: prev.Version, PlanVersion: prev.PlanVersion, LimitUSD: &limit, Agent: f.agent,
	}, relayedAttestation(t, f.id, "sess", "上限を上げる")); err != nil {
		t.Fatalf("ExecuteBudget: %v", err)
	}
	if got := f.limitMicros(t); got != 15_000_000 {
		t.Fatalf("limit = %d, want 15000000 (the --limit path raised it)", got)
	}

	// AC-108
	if f.taskByKey(t, "c") != nil || len(taskRows(t, f.s, f.cid())) != 2 {
		t.Errorf("AC-108: the number of task rows changed")
	}
	// AC-109
	if h, reason := f.lastHold(t); !h.Open() || reason != string(holdReasonTaskAddOverLimit) {
		t.Errorf("AC-109: hold = %+v reason %q, want it still open", h, reason)
	}
	if st := f.detail(t, f.id).Status; st != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human", st)
	}
	// AC-110
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionAwaitingOwner {
		t.Errorf("AC-110: additions = %+v, want awaiting_owner", adds)
	}
	if after := f.detail(t, f.id); after.Version != before.Version {
		t.Errorf("version = %d, want %d (--limit does not change the version)", after.Version, before.Version)
	}
}

// AC-130: 足す依頼の保留に replan に文を添えて答えると、計画を作り直す判断点の入力にその文が入っている。
func TestAC130_ReplanAnswerWithASentenceIsInTheNextPlanCallInput(t *testing.T) {
	t.Parallel()
	const sentence = "SENTENCE-FOR-REPLAN 方針を見直してください"
	f := overLimitHeld(t)
	c, _, err := f.answerHoldFull(t, f.detail(t, f.id).Version, AnswerRequest{Option: taskAddOptionReplan, Answer: sentence})
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusClassified {
		t.Fatalf("status = %s, want classified (awaiting plan)", c.Status)
	}
	inv := &expandRecorder{out: j2OutputFor(false, nil)}
	res, err := f.run(t, inv, idPtr(f.id))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if inv.count() != 1 || len(res.Items) != 1 {
		t.Fatalf("J2 calls = %d items = %+v, want one plan call", inv.count(), res.Items)
	}
	for _, s := range inv.calls[0].Sections {
		if s.Label == callShapeSectionLabel {
			t.Fatalf("the call is the add shape, want the plan shape: %+v", s)
		}
	}
	if text := sectionsText(inv.calls[0].Sections); !strings.Contains(text, sentence) {
		t.Errorf("plan input lacks the sentence %q:\n%s", sentence, text)
	}
}

// AC-155: 選択肢を持たない task_add_question の保留には、文で回答できる（回答の処理 ExecuteAnswer を通す）。
func TestAC155_TaskAddQuestionWithoutOptionsCanBeAnsweredWithASentence(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"どちらの方針で足しますか"}`)})
	h, reason := f.lastHold(t)
	if reason != string(holdReasonTaskAddQuestion) || !h.Open() || len(h.Options) != 0 || h.OptionRequired {
		t.Fatalf("hold = %+v reason %q, want an open task_add_question without options", h, reason)
	}
	const answer = "B 方針で足してください"
	prev, err := f.s.PrepareAnswer(context.Background(), f.id, answer)
	if err != nil {
		t.Fatalf("PrepareAnswer: %v", err)
	}
	c, hold, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: prev.Version, Answer: answer, Agent: f.agent, Conn: f.conn},
		relayedAttestation(t, f.id, "sess", "quote"))
	if err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
	if c.Status != StatusInProgress || hold.Answer == nil || *hold.Answer != answer {
		t.Errorf("status = %s hold = %+v, want in_progress and the answer recorded", c.Status, hold)
	}
	if h, _ := f.lastHold(t); h.Open() || h.Answer == nil || *h.Answer != answer {
		t.Errorf("stored hold = %+v, want closed with the answer", h)
	}
}

// AC-184: 計画の承認待ちの課題の詳細の budget.limit_reason は、承認を待っている版の、上限の理由である
// （課題に記録した前の版の上限・理由があっても、それを返さない）。上限（14）が見込み（10）より大きい計画で確かめる。
func TestAC184_PlanApprovalPendingDetailShowsTheLimitReasonOfThePendingVersion(t *testing.T) {
	t.Parallel()
	f := newLimitFixture(t, func(m map[string]any) {
		m["budget_limit_usd"] = 14.0
		m["budget_limit_reason"] = "PENDING-REASON 続きを足す分"
	})
	// 課題に記録した、前の版の上限・理由（承認待ちの版のものではない）。
	coretest.SetChallengeBudgetLimit(t, f.s.Workspace(), int(f.cid()), 99_000_000, "STALE-REASON 前の版")

	d := f.detail(t, f.id)
	if d.Status != StatusAwaitingPlanApproval {
		t.Fatalf("status = %s, want awaiting_plan_approval", d.Status)
	}
	b := d.Budget
	if b.EstimateUSD == nil || *b.EstimateUSD != 10 || b.LimitUSD == nil || *b.LimitUSD != 14 {
		t.Fatalf("budget = %+v, want estimate 10 / limit 14 of the pending plan", b)
	}
	if b.LimitReason == nil || *b.LimitReason != "PENDING-REASON 続きを足す分" {
		t.Errorf("limit_reason = %v, want the pending version's reason", b.LimitReason)
	}
}
