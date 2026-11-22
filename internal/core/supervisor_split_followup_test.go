package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// このファイルは区切り S4b-2 の手 split_followup（AC-79・80・198〜210）を、実ストアで検証する。
// 回せる条件は、保留の原因の、タスクの検証の判断点 J5 の実行の、記録済みの出力の区分つきの条件（deferred_conditions）から引く。

const (
	splitCondA = "公開の後に、利用者へ案内が届くこと"
	splitCondB = "本番反映の後に、設定が有効になること"
)

// splitFixture は、止まり方 verify_uncertain の、タスクを指す保留（スーパーバイザー待ち）と、その原因の J5 の実行を持つ。
type splitFixture struct {
	*offeredFixture
	tg supervisorTarget
}

// newSplitFixture は、原因の J5 の実行の出力に deferred（{条件の文, 区分} の並び）を持つ保留を作る。
func newSplitFixture(t *testing.T, deferred ...[2]string) *splitFixture {
	t.Helper()
	f := &splitFixture{offeredFixture: newOfferedFixture(t)}
	f.tg = f.hold(t, StatusInProgress, StopKindVerifyUncertain, true)
	var conds []map[string]string
	for _, d := range deferred {
		conds = append(conds, map[string]string{"condition": d[0], "status": d[1]})
	}
	out := map[string]any{"verdict": "uncertain", "reason": "r", "feedback": nil, "question": "どうする？"}
	if conds != nil {
		out["deferred_conditions"] = conds
	}
	b, _ := json.Marshal(out)
	f.exec2(t, `INSERT INTO run (challenge_id, kind, judgment, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, task_id, output)
		VALUES (?, 'judgment', 'J5', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', 'succeeded', 1000000, 'impl', ?, ?)`,
		f.cidInt, f.taskID, string(b))
	f.exec2(t, `UPDATE hold SET run_id = (SELECT MAX(id) FROM run) WHERE id = ?`, f.tg.HoldID)
	return f
}

func (f *splitFixture) exec2(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// do は手 split_followup を、conditions を付けた出力で実行する（対処の記録・保留・フォローアップは実ストアに残る）。
func (f *splitFixture) do(t *testing.T, conds ...int) supervisorHandOutcome {
	t.Helper()
	req := supervisorHandRequest{
		CycleID: f.cycle(t, 300), Target: f.tg, Decl: f.agent,
		Output: SVOutput{Hand: HandSplitFollowup, Findings: "回す", Note: "別に追う", Conditions: conds},
	}
	o, err := f.s.applySupervisorHand(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func (f *splitFixture) followups(t *testing.T) []Followup {
	t.Helper()
	fs, err := f.s.ListFollowups(context.Background(), FollowupFilter{ChallengeID: f.cid})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func (f *splitFixture) actions(t *testing.T) []SupervisorAction {
	t.Helper()
	as, err := f.s.ListSupervisorActionsByChallenge(context.Background(), f.cid)
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func (f *splitFixture) holdState(t *testing.T) (state string, answered bool) {
	t.Helper()
	var st sql.NullString
	var ans sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT supervisor_state, answer FROM hold WHERE id = ?`, f.tg.HoldID).Scan(&st, &ans)
	}); err != nil {
		t.Fatal(err)
	}
	return st.String, ans.Valid
}

// AC-79: 区分つきの条件が 1 件も無い verify_uncertain の保留では、選べる手に split_followup が無い。
func TestSplitFollowup_AC79_NotOfferedWithoutSplittableConditions(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t)
	wantOffered(t, "区分つきの条件なし", f.offered(t, f.tg), HandReverify, HandSendBack, HandReplan, HandDefer, HandEscalate)
	g := newSplitFixture(t, [2]string{splitCondA, "after_release"})
	wantOffered(t, "区分つきの条件あり", g.offered(t, g.tg), HandReverify, HandSendBack, HandSplitFollowup, HandReplan, HandDefer, HandEscalate)
}

// AC-80: 区分つきの条件が 1 件も無い保留に split_followup を返すと、対処の記録の結果は failed（保留はオーナーへ上がる）。
func TestSplitFollowup_AC80_FailedWithoutSplittableConditions(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t)
	if o := f.do(t, 1); o.Result != ActionFailed {
		t.Fatalf("result = %s, want failed", o.Result)
	}
	as := f.actions(t)
	if len(as) != 1 || as[0].Result != ActionFailed {
		t.Fatalf("actions = %+v, want one failed", as)
	}
	if n := len(f.followups(t)); n != 0 {
		t.Errorf("followups = %d, want 0", n)
	}
}

// AC-198・199: 入力の「回せる条件」は、区分つきの条件を出力の順に 1 から番号を振ったもの。区分の無いタスクの完了条件は入らない。
func TestSplitFollowup_AC198_AC199_SectionNumbersTheDeferredConditionsInOrder(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t, [2]string{splitCondA, "after_release"}, [2]string{splitCondB, "after_human_action"})
	secs, err := f.s.buildSupervisorSections(context.Background(), f.agent, f.tg, f.offered(t, f.tg))
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, s := range secs {
		if s.Label == "回せる条件" {
			got = s.Content
		}
	}
	if want := "1. " + splitCondA + "\n2. " + splitCondB; got != want {
		t.Errorf("section = %q, want %q", got, want)
	}
	// 完了条件（区分の無い条件）は、区画「課題とタスク」にだけ現れ、回せる条件には入らない。
	g := newSplitFixture(t)
	secs, err = g.s.buildSupervisorSections(context.Background(), g.agent, g.tg, g.offered(t, g.tg))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secs {
		if s.Label == "回せる条件" {
			t.Errorf("section 回せる条件 must be absent without deferred conditions, got %q", s.Content)
		}
	}
}

// AC-200・201・202・210・保留を閉じる: 番号 1 つで、そのタスクにぶら下がるフォローアップが 1 件でき、出どころは supervisor・題名は条件の文。
func TestSplitFollowup_AC200_AC201_AC202_AC210_OneNumberMakesOneFollowup(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t, [2]string{splitCondA, "after_release"}, [2]string{splitCondB, "after_human_action"})
	if o := f.do(t, 2); o.Result != ActionExecuted {
		t.Fatalf("result = %s, want executed", o.Result)
	}
	fs := f.followups(t)
	if len(fs) != 1 || fs[0].Title != splitCondB || fs[0].Source != FollowupSourceSupervisor || fs[0].Status != FollowupStatusOpen {
		t.Fatalf("followups = %+v", fs)
	}
	if got := f.queryString(t, `SELECT task_id FROM followup`); got.String != formatTaskIDDigits(f.taskID) {
		t.Errorf("followup.task_id = %q, want %d", got.String, f.taskID)
	}
	as := f.actions(t)
	if len(as) != 1 || as[0].Result != ActionExecuted || len(as[0].Conditions) != 1 || as[0].Conditions[0] != (SupervisorCondition{Number: 2, Text: splitCondB}) {
		t.Fatalf("actions = %+v, want one executed with condition 2", as)
	}
	if st, answered := f.holdState(t); st != "handled" || !answered {
		t.Errorf("hold = %s answered=%v, want handled and answered", st, answered)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Errorf("challenge status = %s, want in_progress (the status before the hold)", got)
	}
}

// AC-203: 番号 2 つなら、フォローアップが 2 件できる。
func TestSplitFollowup_AC203_TwoNumbersMakeTwoFollowups(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t, [2]string{splitCondA, "after_release"}, [2]string{splitCondB, "after_human_action"})
	if o := f.do(t, 1, 2); o.Result != ActionExecuted {
		t.Fatalf("result = %s", o.Result)
	}
	fs := f.followups(t)
	if len(fs) != 2 || fs[0].Title != splitCondA || fs[1].Title != splitCondB {
		t.Fatalf("followups = %+v", fs)
	}
	if as := f.actions(t); len(as) != 1 || len(as[0].Conditions) != 2 {
		t.Errorf("actions = %+v, want the two conditions recorded", as)
	}
}

// AC-205・206・207: 回せる条件に無い番号があれば、フォローアップは作られず、対処の記録は failed で、保留は閉じない（escalated）。
func TestSplitFollowup_AC205_AC206_AC207_UnknownNumberFailsAndEscalates(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t, [2]string{splitCondA, "after_release"}, [2]string{splitCondB, "after_human_action"})
	if o := f.do(t, 1, 3); o.Result != ActionFailed {
		t.Fatalf("result = %s, want failed", o.Result)
	}
	if n := len(f.followups(t)); n != 0 {
		t.Errorf("followups = %d, want 0 (nothing is created, not even for the valid number)", n)
	}
	as := f.actions(t)
	if len(as) != 1 || as[0].Result != ActionFailed {
		t.Fatalf("actions = %+v", as)
	}
	if st, answered := f.holdState(t); st != "escalated" || answered {
		t.Errorf("hold = %s answered=%v, want escalated and not answered", st, answered)
	}
}

// AC-208: conditions が空なら、対処の記録は failed。
func TestSplitFollowup_AC208_EmptyConditionsFail(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t, [2]string{splitCondA, "after_release"})
	if o := f.do(t); o.Result != ActionFailed {
		t.Fatalf("result = %s, want failed", o.Result)
	}
	if n := len(f.followups(t)); n != 0 {
		t.Errorf("followups = %d, want 0", n)
	}
}

// 待ちでなくなっていた保留には、フォローアップを作らず断る（state_changed）。
func TestSplitFollowup_NotPendingHoldIsRejectedAndNothingIsCreated(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t, [2]string{splitCondA, "after_release"})
	f.exec2(t, `UPDATE hold SET supervisor_state = 'escalated' WHERE id = ?`, f.tg.HoldID)
	o := f.do(t, 1)
	if o.Result != ActionRejected || o.Reject != RejectStateChanged {
		t.Fatalf("outcome = %+v, want rejected state_changed", o)
	}
	if n := len(f.followups(t)); n != 0 {
		t.Errorf("followups = %d, want 0", n)
	}
}

// 改行を含む条件の文は、1 行の題名にまとめ、元の文を説明へ残す。
func TestSplitFollowup_MultilineConditionBecomesASingleLineTitle(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t, [2]string{"一行目\n二行目", "after_release"})
	if o := f.do(t, 1); o.Result != ActionExecuted {
		t.Fatalf("result = %s", o.Result)
	}
	fs := f.followups(t)
	if len(fs) != 1 || fs[0].Title != "一行目 二行目" || fs[0].Detail != "一行目\n二行目" {
		t.Errorf("followups = %+v", fs)
	}
}

// 原因が verify_uncertain でない・タスクを指さない保留は、回せる条件を持たない。
func TestSplitFollowup_NoSplittableConditionsForOtherStopKindsOrWithoutATask(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t, [2]string{splitCondA, "after_release"})
	for _, tg := range []supervisorTarget{{HoldID: f.tg.HoldID, ChallengeID: f.cidInt, StopKind: StopKindVerifyUncertain}, {HoldID: f.tg.HoldID, ChallengeID: f.cidInt, TaskID: f.tg.TaskID, StopKind: StopKindFactsUnmet}} {
		var got []SupervisorCondition
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var err error
			got, err = splittableConditions(context.Background(), tx, tg)
			return err
		}); err != nil || len(got) != 0 {
			t.Errorf("splittable(%+v) = %v, %v, want none", tg, got, err)
		}
	}
}

// AC-204: split_followup の後のタスクの検証の入力に、回した条件が「判定から外す」区画として渡る。最後のオーナーの回答より前の記録・
// failed の記録は渡さない。
func TestSplitFollowup_AC204_NextTaskVerificationGetsTheSplitConditionsSection(t *testing.T) {
	t.Parallel()
	f := newSplitFixture(t, [2]string{splitCondA, "after_release"}, [2]string{splitCondB, "after_human_action"})
	read := func() []string {
		var got []string
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var err error
			got, err = splitConditionsForTask(context.Background(), tx, f.cidInt, f.taskID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("before the hand: %v, want none", got)
	}
	f.do(t, 2)
	if got := read(); len(got) != 1 || got[0] != splitCondB {
		t.Fatalf("after the hand: %v, want [%s]", got, splitCondB)
	}
	if body := formatSplitConditionsSection(read()); body != "- "+splitCondB {
		t.Errorf("section body = %q", body)
	}
	// 最後のオーナーの回答より後に限る。
	f.exec2(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, kind)
		VALUES (?, 'q', 'in_progress', '2999-01-01T00:00:00.000Z', 'a', '2999-01-01T00:00:01.000Z', 'owner', 'human')`, f.cidInt)
	if got := read(); len(got) != 0 {
		t.Errorf("after an owner answer: %v, want none", got)
	}
}

// AC-204: 区画は、タスクの検証の入力（prepareJ5）に載る。
func TestSplitFollowup_AC204_SectionIsPartOfTheTaskVerificationInput(t *testing.T) {
	t.Parallel()
	vc := &j5Context{SplitConditions: []string{splitCondA}}
	secs, ns := prepareJ5(context.Background(), nil, vc, nil, nil, nil)
	if ns != nil {
		t.Fatalf("not started: %+v", ns)
	}
	var got string
	for _, s := range secs {
		if s.Label == "フォローアップへ回した条件（判定から外す）" {
			got = s.Content
		}
	}
	if got != "- "+splitCondA {
		t.Errorf("section = %q, want the split condition", got)
	}
}

func formatTaskIDDigits(id int64) string { return strings.TrimPrefix(formatTaskID(id), "T-") }
