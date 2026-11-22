package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// 区切り S4c-2 の 2: 出どころが followup の足す依頼を、計画の段が判断点の「足す」の形で呼ぶ（AC-52〜57・61〜65・80）。
// 実ストアと偽の判断の IF で確かめる。見込み 10 USD・上限 14 USD の課題（newExpandFixture）に、枠 1+1 USD の足す依頼を上げる。

// followupAddFixture は、フォローアップ（題名・説明つき）をタスクとして上げた、仕様待ちの足す依頼を持つ課題。
func followupAddFixture(t *testing.T) (*paFixture, string) {
	t.Helper()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	n, _ := parseFollowupID(fid)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE followup SET title = '検索の遅さを直す', detail = '一覧の検索が 3 秒かかる。索引を足したい。' WHERE id = ?`, n)
		return err
	})
	if _, res, err := destPromote(t, f.s, fid, destSmall); err != nil || res.Addition == nil {
		t.Fatalf("promote: %v %+v", err, res)
	}
	return f, fid
}

func (f *paFixture) followupRow(t *testing.T, fid string) (task, challenge sql.NullInt64) {
	t.Helper()
	n, _ := parseFollowupID(fid)
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT promoted_task_id, promoted_challenge_id FROM followup WHERE id = ?`, n).Scan(&task, &challenge)
	})
	return task, challenge
}

// 出力の枠（9+9 USD）は、足す依頼の額（1+1 USD）と違う。
func followupAddOutput() string {
	return expandAddOutput(func(ts []map[string]any) {
		ts[0]["budget_impl_usd"], ts[0]["budget_review_usd"] = 9.0, 9.0
	})
}

// AC-52・AC-53
func TestFollowupExpand_InputCarriesTheFollowupTitleAndDetailAfterTheCallShape(t *testing.T) {
	t.Parallel()
	f, _ := followupAddFixture(t)
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	if inv.count() != 1 {
		t.Fatalf("J2 calls = %d, want 1", inv.count())
	}
	secs := inv.calls[0].Sections
	if secs[0].Label != callShapeSectionLabel || secs[0].Content != callShapeExpand {
		t.Fatalf("first section = %+v, want the add call shape", secs[0])
	}
	got := map[string]string{}
	for _, s := range secs {
		got[s.Label] = s.Content
	}
	if got[labelExpandFollowupTitle] != "検索の遅さを直す" {
		t.Errorf("title section = %q (AC-52)", got[labelExpandFollowupTitle])
	}
	if !strings.Contains(got[labelExpandFollowupDetail], "索引を足したい") {
		t.Errorf("detail section = %q (AC-53)", got[labelExpandFollowupDetail])
	}
}

func TestFollowupExpand_ExpandOriginInputHasNoFollowupSections(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	for _, s := range inv.calls[0].Sections {
		if s.Label == labelExpandFollowupTitle || s.Label == labelExpandFollowupDetail {
			t.Errorf("expand-origin input has section %q", s.Label)
		}
	}
}

// AC-54・AC-55・AC-56・AC-57
func TestFollowupExpand_AddCreatesOneTaskWithTheAdditionsFramesAndRecordsThePromotedTask(t *testing.T) {
	t.Parallel()
	f, fid := followupAddFixture(t)
	// 上げ直しの前に上げた先の課題が記録されている形（promoted_challenge_id は、足しても変わらない）。
	n, _ := parseFollowupID(fid)
	old := mustCreateChallengeInternalID(t, f.s, "前に上げた先")
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE followup SET promoted_challenge_id = ? WHERE id = ?`, old, n)
		return err
	})
	res := f.expand(t, &expandRecorder{out: []byte(followupAddOutput())})
	if len(res.Items) != 1 || res.Items[0].Result != RunResultSucceeded || res.Items[0].Outcome != "add" {
		t.Fatalf("items = %+v", res.Items)
	}
	c := f.taskByKey(t, "c")
	if c == nil || c.Status != TaskStatusPending {
		t.Fatalf("task c = %+v, want a pending task (AC-54)", c)
	}
	var impl, review int64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		tid, _ := parseTaskID(c.ID)
		return tx.QueryRow(`SELECT budget_impl_micros, budget_review_micros FROM task WHERE id = ?`, tid).Scan(&impl, &review)
	})
	if impl != 1_000_000 || review != 1_000_000 {
		t.Errorf("task frames = %d/%d micros, want the addition's 1000000/1000000 (AC-55)", impl, review)
	}
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].State != TaskAdditionApplied || len(adds[0].TaskIDs) != 1 || adds[0].TaskIDs[0] != c.ID {
		t.Fatalf("additions = %+v, want applied with the added task", adds)
	}
	if adds[0].TotalAfterUSD == nil || *adds[0].TotalAfterUSD != 12 {
		t.Errorf("total after = %v, want 12", adds[0].TotalAfterUSD)
	}
	pt, pc := f.followupRow(t, fid)
	tid, _ := parseTaskID(c.ID)
	if !pt.Valid || pt.Int64 != tid {
		t.Errorf("promoted_task_id = %+v, want %s (AC-56)", pt, c.ID)
	}
	if !pc.Valid || pc.Int64 != old {
		t.Errorf("promoted_challenge_id = %+v, want unchanged (AC-57)", pc)
	}
}

// AC-61・AC-62: 上げた後に見込みが増えて、足した後の見込みが上限を超える。
func TestFollowupExpand_AddOverTheLimitAfterTheEstimateGrewMakesNoTaskAndHolds(t *testing.T) {
	t.Parallel()
	f, fid := followupAddFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error { // 見込みを 10 → 13 USD にする（足すと 15 USD で上限 14 USD を超える）
		_, err := tx.Exec(`UPDATE task SET budget_impl_micros = 6000000 WHERE challenge_id = ? AND key = 'b'`, f.cid())
		return err
	})
	res := f.expand(t, &expandRecorder{out: []byte(followupAddOutput())})
	if len(res.Items) != 1 || res.Items[0].Outcome != "add" {
		t.Fatalf("items = %+v", res.Items)
	}
	if f.taskByKey(t, "c") != nil || len(taskRows(t, f.s, f.cid())) != 2 {
		t.Errorf("tasks = %+v, want no new task (AC-61)", taskRows(t, f.s, f.cid()))
	}
	h, reason := f.lastHold(t)
	if reason != string(holdReasonTaskAddOverLimit) || !h.Open() || f.detail(t, f.id).Status != StatusAwaitingHuman {
		t.Errorf("hold = %+v reason %q, want an open task_add_over_limit hold (AC-62)", h, reason)
	}
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].State != TaskAdditionAwaitingOwner || adds[0].Spec == nil {
		t.Fatalf("additions = %+v, want awaiting_owner with the spec", adds)
	}
	if pt, _ := f.followupRow(t, fid); pt.Valid {
		t.Errorf("promoted_task_id = %+v, want unset before the task is added", pt)
	}
}

// 保留への回答 add で足したときも、上げた先のタスクを記録し、枠は足す依頼の額のまま。
func TestFollowupExpand_AnswerAddAfterOverLimitRecordsThePromotedTaskWithTheAdditionsFrames(t *testing.T) {
	t.Parallel()
	f, fid := followupAddFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET budget_impl_micros = 6000000 WHERE challenge_id = ? AND key = 'b'`, f.cid())
		return err
	})
	f.expand(t, &expandRecorder{out: []byte(followupAddOutput())})
	if _, err := f.answerHold(t, taskAddOptionAdd); err != nil {
		t.Fatalf("answer add: %v", err)
	}
	c := f.taskByKey(t, "c")
	if c == nil {
		t.Fatal("task c was not added")
	}
	if pt, _ := f.followupRow(t, fid); !pt.Valid {
		t.Errorf("promoted_task_id = %+v, want the added task", pt)
	}
	if got := f.limitMicros(t); got != 15_000_000 {
		t.Errorf("limit = %d, want raised to 15000000", got)
	}
}

// AC-63
func TestFollowupExpand_AskHoldsTheChallengeWithATaskAddQuestion(t *testing.T) {
	t.Parallel()
	f, _ := followupAddFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"範囲を確認したい"}`)})
	h, reason := f.lastHold(t)
	if reason != string(holdReasonTaskAddQuestion) || h.Question != "範囲を確認したい" || !h.Open() {
		t.Errorf("hold = %+v reason %q, want an open task_add_question hold", h, reason)
	}
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionPending || adds[0].HoldID == nil {
		t.Errorf("additions = %+v, want pending with the hold", adds)
	}
}

// AC-64・AC-65
func TestFollowupExpand_NoneAndTwoTasksAreInvalidOutputs(t *testing.T) {
	t.Parallel()
	two := expandAddOutput(nil)
	var m map[string]any
	_ = json.Unmarshal([]byte(two), &m)
	tasks := m["tasks"].([]any)
	second := map[string]any{}
	for k, v := range tasks[0].(map[string]any) {
		second[k] = v
	}
	second["key"] = "d"
	m["tasks"] = []any{tasks[0], second}
	twoOut, _ := json.Marshal(m)
	for name, out := range map[string]string{"none (AC-64)": `{"verdict":"none"}`, "two tasks (AC-65)": string(twoOut)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, fid := followupAddFixture(t)
			res := f.expand(t, &expandRecorder{out: []byte(out)})
			if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
				t.Fatalf("items = %+v, want invalid_output", res.Items)
			}
			if n := len(taskRows(t, f.s, f.cid())); n != 2 {
				t.Errorf("tasks = %d, want 2", n)
			}
			if adds := f.additions(t); adds[0].State != TaskAdditionPending {
				t.Errorf("addition = %+v, want it left pending", adds[0])
			}
			if pt, _ := f.followupRow(t, fid); pt.Valid {
				t.Errorf("promoted_task_id = %+v, want unset", pt)
			}
		})
	}
}

// 出どころが expand の足す依頼では none も従来どおり通り、followup では通らない（S4c-1 を変えない）。
func TestFollowupExpand_ExpandOriginStillAcceptsNone(t *testing.T) {
	t.Parallel()
	if _, ok := validateJ2ExpandOutput([]byte(`{"verdict":"none"}`), j2ExpandValidationContext{Origin: TaskAdditionOriginExpand}); !ok {
		t.Error("expand origin must accept none")
	}
	if _, ok := validateJ2ExpandOutput([]byte(`{"verdict":"none"}`), j2ExpandValidationContext{Origin: TaskAdditionOriginFollowup}); ok {
		t.Error("followup origin must reject none")
	}
}

func TestFollowupExpand_SchemaForFollowupExcludesNoneAndLimitsTasksToOne(t *testing.T) {
	t.Parallel()
	var sc struct {
		Properties struct {
			Verdict struct {
				Enum []string `json:"enum"`
			} `json:"verdict"`
			Tasks struct {
				MaxItems int `json:"maxItems"`
			} `json:"tasks"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(j2ExpandOutputSchemaFor(TaskAdditionOriginFollowup), &sc); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sc.Properties.Verdict.Enum, ",") != "add,ask" || sc.Properties.Tasks.MaxItems != 1 {
		t.Errorf("schema = %+v, want add|ask and at most 1 task", sc.Properties)
	}
}

// AC-80
func TestFollowupExpand_ReapprovingThePlanSupersedesAPendingFollowupAddition(t *testing.T) {
	t.Parallel()
	f := newLimitFixture(t, nil)
	fid := destFixtureFollowup(t, f)
	n, _ := parseFollowupID(fid)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task_addition (challenge_id, origin, state, created_at, followup_id, budget_impl_micros, budget_review_micros)
			VALUES (?, 'followup', 'pending', '2026-11-01T00:00:00.000Z', ?, 1000000, 1000000)`, f.cid(), n)
		return err
	})
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].Origin != TaskAdditionOriginFollowup || adds[0].State != TaskAdditionSuperseded || adds[0].DecidedAt == nil {
		t.Errorf("additions = %+v, want the followup addition superseded", adds)
	}
}

// 足す依頼の枠が 0 USD でも、出力は不正にならず、足す依頼の額で足せる。
func TestFollowupExpand_ZeroFrameOfTheAdditionStillAdds(t *testing.T) {
	t.Parallel()
	f, _ := followupAddFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_addition SET budget_review_micros = 0`)
		return err
	})
	res := f.expand(t, &expandRecorder{out: []byte(followupAddOutput())})
	if len(res.Items) != 1 || res.Items[0].Result != RunResultSucceeded || f.taskByKey(t, "c") == nil {
		t.Fatalf("items = %+v, want the task added", res.Items)
	}
	var impl, review int64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT budget_impl_micros, budget_review_micros FROM task WHERE challenge_id = ? AND key = 'c'`, f.cid()).Scan(&impl, &review)
	})
	if impl != 1_000_000 || review != 0 {
		t.Errorf("frames = %d/%d, want the addition's 1000000/0 (no placeholder leaks)", impl, review)
	}
}

// 実装の枠が 1 回の上限を超える足す依頼の額でも、出力は不正にならず、足す依頼の額で足せる。
func TestFollowupExpand_ImplFrameAboveTheRunCapStillAdds(t *testing.T) {
	t.Parallel()
	f, _ := followupAddFixture(t)
	over := int64(f.agent.MaxRunBudgetUSD*1_000_000) + 1_000_000
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_addition SET budget_impl_micros = ?`, over)
		return err
	})
	res := f.expand(t, &expandRecorder{out: []byte(followupAddOutput())})
	// 上限（14 USD）を超えるときは保留になるが、不正な出力にはならない。
	if len(res.Items) != 1 || res.Items[0].Result != RunResultSucceeded {
		t.Fatalf("items = %+v, want a valid output", res.Items)
	}
}

// フォローアップを捨てると、その足す依頼は見送りで閉じ、判断点を呼ばず、足さない。
func TestFollowupExpand_DroppingTheFollowupDeclinesItsAdditionAndNothingIsAdded(t *testing.T) {
	t.Parallel()
	f, fid := followupAddFixture(t)
	if _, err := f.s.DropFollowups(context.Background(), []string{fid}, "不要", fuAtt(t, fid)); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionDeclined {
		t.Fatalf("additions = %+v, want declined", adds)
	}
	inv := &expandRecorder{out: []byte(followupAddOutput())}
	f.expand(t, inv)
	if inv.count() != 0 || f.taskByKey(t, "c") != nil {
		t.Errorf("calls = %d, task c = %v, want no call and no task", inv.count(), f.taskByKey(t, "c"))
	}
}
