package core

// #451: J5 の条件ごとの判定の区分（本番反映で満たされる見込み・人の操作の後でしか満たせない）の
// 検査と、区分による振り分け（完了確認待ち・人間待ちの保留・着手中）を固定する。

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// j5OutDeferred は deferred_conditions を持つ J5 の出力。deferred は {condition, status} の組の並び。
func j5OutDeferred(verdict string, feedback, question any, deferred ...[2]string) JudgmentLaunchOutput {
	conds := make([]map[string]string, 0, len(deferred))
	for _, d := range deferred {
		conds = append(conds, map[string]string{"condition": d[0], "status": d[1]})
	}
	b, _ := json.Marshal(map[string]any{
		"verdict": verdict, "reason": "REASON-" + verdict, "feedback": feedback, "question": question,
		"deferred_conditions": conds,
	})
	return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: b, ReportedTotalCostUSD: float64Ptr(0.5)}
}

const (
	condAfterRelease = "成果物が既定の系統へ取り込まれている"
	condAfterHuman   = "人が配布物を公開している"
)

func TestValidateJ5Output_DeferredConditions(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		ok   bool
		want []J5DeferredCondition
	}{
		{"absent (old output)", `{"verdict":"met","reason":"r"}`, true, nil},
		{"null", `{"verdict":"met","reason":"r","deferred_conditions":null}`, true, nil},
		{"empty", `{"verdict":"met","reason":"r","deferred_conditions":[]}`, true, nil},
		{"both statuses, trimmed", `{"verdict":"met","reason":"r","deferred_conditions":[{"condition":" A ","status":"after_release"},{"condition":"B","status":"after_human_action"}]}`, true,
			[]J5DeferredCondition{{Condition: "A", Status: J5ConditionAfterRelease}, {Condition: "B", Status: J5ConditionAfterHumanAction}}},
		{"unknown status", `{"verdict":"met","reason":"r","deferred_conditions":[{"condition":"A","status":"later"}]}`, false, nil},
		{"blank condition", `{"verdict":"met","reason":"r","deferred_conditions":[{"condition":"  ","status":"after_release"}]}`, false, nil},
		{"too long condition", `{"verdict":"met","reason":"r","deferred_conditions":[{"condition":"` + strings.Repeat("あ", maxJ5DeferredConditionRunes+1) + `","status":"after_release"}]}`, false, nil},
		{"not an array", `{"verdict":"met","reason":"r","deferred_conditions":"A"}`, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := validateJ5Output([]byte(tc.in))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && !reflect.DeepEqual(v.Deferred, tc.want) {
				t.Errorf("Deferred = %+v, want %+v", v.Deferred, tc.want)
			}
		})
	}
	var many []string
	for i := 0; i <= MaxJ5DeferredConditions; i++ {
		many = append(many, `{"condition":"A","status":"after_release"}`)
	}
	if _, ok := validateJ5Output([]byte(`{"verdict":"met","reason":"r","deferred_conditions":[` + strings.Join(many, ",") + `]}`)); ok {
		t.Errorf("%d deferred conditions must be rejected", len(many))
	}
}

func TestJ5OutputSchema_HasTheDeferredConditionsWithTheClosedStatuses(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Type     json.RawMessage `json:"type"`
			MaxItems int             `json:"maxItems"`
			Items    struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
				Required []string `json:"required"`
			} `json:"items"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(j5OutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	dc, ok := schema.Properties["deferred_conditions"]
	if !ok {
		t.Fatal("schema lacks deferred_conditions")
	}
	if dc.MaxItems != MaxJ5DeferredConditions || !reflect.DeepEqual(dc.Items.Required, []string{"condition", "status"}) {
		t.Errorf("deferred_conditions = %+v", dc)
	}
	if got := dc.Items.Properties["status"].Enum; !reflect.DeepEqual(got, []string{"after_release", "after_human_action"}) {
		t.Errorf("status enum = %v", got)
	}
	for _, r := range schema.Required {
		if r == "deferred_conditions" {
			t.Error("deferred_conditions must stay optional (old outputs remain valid)")
		}
	}
}

func TestAllJudgmentOutputClosedValues_IncludesTheJ5ConditionStatuses(t *testing.T) {
	all := strings.Join(AllJudgmentOutputClosedValues(), "\n")
	for _, v := range J5ConditionStatusValues {
		if !strings.Contains(all, string(v)) {
			t.Errorf("AllJudgmentOutputClosedValues lacks %q", v)
		}
	}
}

func (f *j5Fixture) approvalPreview(t *testing.T, id string) *ApprovalPreview {
	t.Helper()
	p, err := f.s.PrepareApproval(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *j5Fixture) holdCount(t *testing.T) string {
	t.Helper()
	return f.queryString(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = 1`)
}

// (b) 区分の無い未達・不確かが無く、残りが「本番反映で満たされる見込み」だけなら、完了確認待ちへ進み、
// 承認の要約にその条件が出る。
func TestVerifyAutoJ5_OnlyAfterReleaseLeft_AwaitsCompletionApprovalAndTheSummaryListsThem(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.result = j5OutDeferred("met", nil, nil,
		[2]string{condAfterRelease, "after_release"}, [2]string{"2 つめの条件", "after_release"})
	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(res, err)
	}
	if it := res.Items[0]; it.Outcome != "met" || it.Status == nil || *it.Status != string(StatusAwaitingCompletionApproval) {
		t.Errorf("item = %+v", it)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s", got)
	}
	if got := f.holdCount(t); got != "0" {
		t.Errorf("holds = %s, want none", got)
	}
	p := f.approvalPreview(t, "C-1")
	if want := []string{condAfterRelease, "2 つめの条件"}; !reflect.DeepEqual(p.ConditionsAfterRelease, want) {
		t.Errorf("ConditionsAfterRelease = %v, want %v", p.ConditionsAfterRelease, want)
	}
}

// (c) 区分の無い未達・不確かが無く、「人の操作の後でしか満たせない」が 1 つでもあれば、着手中に戻さず
// 人間待ちの保留へ置き、理由に該当の条件を書く。次の周で子は起動されない。回答すると検証中へ戻る。
func TestVerifyAutoJ5_AfterHumanActionLeft_HoldsWithTheConditionsAndDoesNotRelaunchTheChild(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	launches := len(f.deleg.launched())
	f.j5.result = j5OutDeferred("met", nil, nil,
		[2]string{condAfterRelease, "after_release"}, [2]string{condAfterHuman, "after_human_action"})
	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(res, err)
	}
	if it := res.Items[0]; it.Result != RunResultSucceeded || it.Status == nil || *it.Status != string(StatusAwaitingHuman) {
		t.Errorf("item = %+v", it)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human (not in_progress)", got)
	}
	q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`)
	if !strings.HasPrefix(q, J5HumanActionHoldPreamble) || !strings.Contains(q, "- "+condAfterHuman) {
		t.Errorf("hold question = %q, want the preamble and the condition", q)
	}
	if strings.Contains(q, condAfterRelease) {
		t.Errorf("hold question = %q must not list the after-release condition", q)
	}
	runs := f.j5Runs(t)
	if got := f.queryString(t, `SELECT from_status || '/' || run_id FROM hold WHERE challenge_id = 1`); got != "verifying/"+strings.TrimPrefix(runs[0].ID, "R-") {
		t.Errorf("hold from_status/run_id = %q", got)
	}
	if action, channel, _ := f.lastActivity(t, "C-1"); action != "verify_uncertain" || channel != "invoker" {
		t.Errorf("activity = %s/%s", action, channel)
	}
	// 次の周: 委譲の対象（ID を省略）に入らず、子は起動されない。
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.deleg.launched()); got != launches {
		t.Errorf("delegations launched = %d, want %d (the child must not be relaunched)", got, launches)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Errorf("status after the next cycle = %s", got)
	}
	// 人が操作を終えて回答すると、検証中へ戻る（T12。保留を作った状態へ）。
	att := f.budgetAttestation(t, "C-1")
	req := AnswerRequest{ChallengeID: "C-1", ExpectedVersion: f.version(t, "C-1"), Answer: "公開した"}
	if _, _, err := f.s.ExecuteAnswer(context.Background(), req, att); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusVerifying {
		t.Errorf("status after the answer = %s, want verifying", got)
	}
}

// (d) 区分の無い未達は、従来どおり着手中へ戻る（区分の無い旧い出力も同じ）。
func TestVerifyAutoJ5_NotMetWithoutCategories_ReturnsToInProgressAsBefore(t *testing.T) {
	for name, out := range map[string]JudgmentLaunchOutput{
		"old output":  j5Out("not_met", "FEEDBACK-TEXT", nil),
		"empty array": j5OutDeferred("not_met", "FEEDBACK-TEXT", nil),
	} {
		t.Run(name, func(t *testing.T) {
			f := newJ5Fixture(t, "impl")
			f.delegateToVerifying(t, "C-1")
			f.j5.result = out
			if _, err := f.verify(t, nil); err != nil {
				t.Fatal(err)
			}
			if got := f.detail(t, "C-1").Status; got != StatusInProgress {
				t.Errorf("status = %s, want in_progress", got)
			}
			if got := f.holdCount(t); got != "0" {
				t.Errorf("holds = %s, want none", got)
			}
		})
	}
}

// 区分の無い met（旧い出力）は、従来どおり完了確認待ちへ進み、要約に区分の条件は出ない。
func TestVerifyAutoJ5_MetWithoutCategories_SummaryHasNoConditionsAfterRelease(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s", got)
	}
	if p := f.approvalPreview(t, "C-1"); p.ConditionsAfterRelease != nil {
		t.Errorf("ConditionsAfterRelease = %v, want nil", p.ConditionsAfterRelease)
	}
}

// (e) 区分の付いた条件と区分の無い未達・不確かが混在するとき: 区分の無い未達があれば着手中へ戻り、
// 区分の無い不確かがあればその問いで保留になる（人の操作の保留の理由にはしない）。
func TestVerifyAutoJ5_CategoriesMixedWithPlainVerdicts(t *testing.T) {
	both := [][2]string{{condAfterRelease, "after_release"}, {condAfterHuman, "after_human_action"}}
	t.Run("not_met with both categories returns to in_progress", func(t *testing.T) {
		f := newJ5Fixture(t, "impl")
		f.delegateToVerifying(t, "C-1")
		f.j5.result = j5OutDeferred("not_met", "FEEDBACK-TEXT", nil, both...)
		res, err := f.verify(t, nil)
		if err != nil || len(res.Items) != 1 {
			t.Fatal(res, err)
		}
		if it := res.Items[0]; it.Outcome != "not_met" || it.Status == nil || *it.Status != string(StatusInProgress) {
			t.Errorf("item = %+v", it)
		}
		if got := f.holdCount(t); got != "0" {
			t.Errorf("holds = %s, want none", got)
		}
	})
	t.Run("uncertain with both categories holds with the plain question", func(t *testing.T) {
		f := newJ5Fixture(t, "impl")
		f.delegateToVerifying(t, "C-1")
		f.j5.result = j5OutDeferred("uncertain", nil, "QUESTION-FOR-HUMAN", both...)
		if _, err := f.verify(t, nil); err != nil {
			t.Fatal(err)
		}
		if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
			t.Errorf("status = %s", got)
		}
		if got := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`); got != "QUESTION-FOR-HUMAN" {
			t.Errorf("hold question = %q", got)
		}
	})
	t.Run("after_release with a later plain met keeps only the latest conditions", func(t *testing.T) {
		f := newJ5Fixture(t, "impl")
		f.delegateToVerifying(t, "C-1")
		f.j5.result = j5OutDeferred("met", nil, nil, [2]string{condAfterRelease, "after_release"})
		if _, err := f.verify(t, nil); err != nil {
			t.Fatal(err)
		}
		// 差し戻して検証し直し、区分の無い met で完了確認待ちへ入ると、前の条件は要約に出ない。
		f.setStatus(t, "C-1", "verifying")
		f.j5.result = j5Out("met", nil, nil)
		if _, err := f.verify(t, nil); err != nil {
			t.Fatal(err)
		}
		if p := f.approvalPreview(t, "C-1"); p.ConditionsAfterRelease != nil {
			t.Errorf("ConditionsAfterRelease = %v, want nil (from the latest verification)", p.ConditionsAfterRelease)
		}
	})
}
