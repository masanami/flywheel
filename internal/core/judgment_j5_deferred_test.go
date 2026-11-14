package core

// #451: J5 の条件ごとの判定の区分（本番反映で満たされる見込み・人の操作の後でしか満たせない）の
// 検査と、区分による振り分け（完了確認待ち・人間待ちの保留・着手中）を固定する。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// addOperation は課題 C-1 に不可逆操作を未承認で登録する（本番反映が待っている状態を作る）。
func (f *j5Fixture) addOperation(t *testing.T, kind string) {
	t.Helper()
	if _, err := f.s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: "C-1", Kind: kind, Summary: "OP-" + kind}); err != nil {
		t.Fatal(err)
	}
}

func (f *j5Fixture) holdCount(t *testing.T) string {
	t.Helper()
	return f.queryString(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = 1`)
}

// (b) 区分の無い未達・不確かが無く、残りが「本番反映で満たされる見込み」だけで、待っている本番反映
// （未承認の release）があれば、完了確認待ちへ進み、承認の要約にその条件が出る（#466 の (d)）。
func TestVerifyAutoJ5_OnlyAfterReleaseLeftWithAPendingRelease_AwaitsCompletionApprovalAndTheSummaryListsThem(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.addOperation(t, "release")
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
	f.addOperation(t, "release") // 待っている本番反映があれば、本番反映が無い旨は保留に足さない
	launches := len(f.deleg.launched())
	f.j5.result = j5OutDeferred("met", nil, nil,
		[2]string{condAfterRelease, "after_release"}, [2]string{condAfterHuman, "after_human_action"})
	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(res, err)
	}
	if it := res.Items[0]; it.Result != RunResultSucceeded || it.Outcome != "uncertain" || it.Status == nil || *it.Status != string(StatusAwaitingHuman) {
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
	if got := f.queryString(t, `SELECT COUNT(*) FROM occurrence WHERE challenge_id = 1 AND kind LIKE 'verification%'`); got != "0" {
		t.Errorf("verification occurrences = %s, want none (waiting for a human action is not a failure)", got)
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
	// 検証し直して残りが満たされれば、完了確認待ちへ進む。
	f.j5.result = j5Out("met", nil, nil)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Errorf("status after the re-verification = %s, want awaiting_completion_approval", got)
	}
}

// 人の操作の後でしか満たせない条件と、予想と実物の個数の食い違いが重なると、食い違いの保留に条件も書き、
// 実物は課題へ書かない。
func TestVerifyAutoJ5_AfterHumanActionWithActualMismatch_OneHoldListsBoth(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.setExpected(t, "C-1", 2)
	b, _ := json.Marshal(map[string]any{
		"verdict": "met", "reason": "R", "feedback": nil, "question": nil,
		"actual":              []map[string]string{{"scene": "s", "after": "a"}},
		"deferred_conditions": []map[string]string{{"condition": condAfterHuman, "status": "after_human_action"}},
	})
	f.j5.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: b, ReportedTotalCostUSD: float64Ptr(0.5)}
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s", got)
	}
	if got := f.holdCount(t); got != "1" {
		t.Errorf("holds = %s, want 1", got)
	}
	q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`)
	if !strings.HasPrefix(q, ActualMismatchHoldPreamble) || !strings.Contains(q, "- "+condAfterHuman) {
		t.Errorf("hold question = %q, want the mismatch and the human-action condition", q)
	}
	if got := f.detail(t, "C-1").Actual; len(got) != 0 {
		t.Errorf("actual = %+v, want unchanged (empty)", got)
	}
}

// 人の操作の保留（T10）では、他の T10 と同じく実物を課題へ書かない。
func TestVerifyAutoJ5_AfterHumanAction_DoesNotWriteTheActual(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	b, _ := json.Marshal(map[string]any{
		"verdict": "met", "reason": "R", "feedback": nil, "question": nil,
		"actual":              []map[string]string{{"scene": "s", "after": "a"}},
		"deferred_conditions": []map[string]string{{"condition": condAfterHuman, "status": "after_human_action"}},
	})
	f.j5.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: b, ReportedTotalCostUSD: float64Ptr(0.5)}
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Actual; len(got) != 0 {
		t.Errorf("actual = %+v, want unchanged (empty)", got)
	}
}

// 作業ログの値が読めなくても、表示だけの値なので承認の①を止めない。
func TestPrepareApproval_UnreadableConditionsAfterReleaseAreIgnored(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	for _, after := range []string{`[]`, `{"conditions_after_release":"x"}`, `not json`} {
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE activity SET after = ? WHERE id = (SELECT MAX(id) FROM activity WHERE action = 'verify_met')`, after)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if p := f.approvalPreview(t, "C-1"); p.ConditionsAfterRelease != nil {
			t.Errorf("after=%s: ConditionsAfterRelease = %v", after, p.ConditionsAfterRelease)
		}
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
		f.addOperation(t, "release")
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

// --- #466: 待っている本番反映が無いのに、残りが「本番反映で満たされる見込み」だけのとき ---

// (c) 残りが after_release だけで待っている本番反映（未承認の release）が無いと、条件は満たされないまま
// 完了確認待ちへ進んでしまう。進めず、「本番反映が無いので条件を満たせない」と条件を問いに書いた人間待ちの
// 保留へ置く。着手中へ戻さず（子を再起動しない）、失敗ではないので発生も作らず、実物は課題へ書かない。
func TestVerifyAutoJ5_OnlyAfterReleaseLeftWithNoPendingRelease_HoldsAndDoesNotRelaunchTheChild(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	launches := len(f.deleg.launched())
	b, _ := json.Marshal(map[string]any{
		"verdict": "met", "reason": "R", "feedback": nil, "question": nil,
		"actual": []map[string]string{{"scene": "s", "after": "a"}},
		"deferred_conditions": []map[string]string{
			{"condition": condAfterRelease, "status": "after_release"},
			{"condition": "2 つめの条件", "status": "after_release"},
		},
	})
	f.j5.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: b, ReportedTotalCostUSD: float64Ptr(0.5)}
	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(res, err)
	}
	if it := res.Items[0]; it.Result != RunResultSucceeded || it.Outcome != "uncertain" || it.Status == nil || *it.Status != string(StatusAwaitingHuman) {
		t.Errorf("item = %+v", it)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human (not awaiting_completion_approval, not in_progress)", got)
	}
	q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`)
	if !strings.HasPrefix(q, J5NoReleaseHoldPreamble) || !strings.Contains(q, "本番反映が無いので条件を満たせない") ||
		!strings.Contains(q, "- "+condAfterRelease) || !strings.Contains(q, "- 2 つめの条件") {
		t.Errorf("hold question = %q, want the no-release preamble and the conditions", q)
	}
	if got := f.queryString(t, `SELECT from_status FROM hold WHERE challenge_id = 1`); got != "verifying" {
		t.Errorf("hold from_status = %q", got)
	}
	if action, channel, _ := f.lastActivity(t, "C-1"); action != "verify_uncertain" || channel != "invoker" {
		t.Errorf("activity = %s/%s", action, channel)
	}
	if got := f.queryString(t, `SELECT COUNT(*) FROM occurrence WHERE challenge_id = 1 AND kind LIKE 'verification%'`); got != "0" {
		t.Errorf("verification occurrences = %s, want none (waiting for an answer is not a failure)", got)
	}
	if got := f.detail(t, "C-1").Actual; len(got) != 0 {
		t.Errorf("actual = %+v, want unchanged (empty)", got)
	}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.deleg.launched()); got != launches {
		t.Errorf("delegations launched = %d, want %d (the child must not be relaunched)", got, launches)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Errorf("status after the next cycle = %s", got)
	}
}

// 待っている本番反映と数えるのは、その課題の未承認（pending）か承認済みで未実行（approved）の release だけ。
// release 以外の未承認の操作や、終わった release（実行済み・却下・取り消し）は数えない。
func TestVerifyAutoJ5_OnlyAfterReleaseLeft_OnlyAPendingOrApprovedReleaseCounts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, f *j5Fixture)
		wantEnd Status
	}{
		{"a pending delete is not a release", func(t *testing.T, f *j5Fixture) { f.addOperation(t, "delete") }, StatusAwaitingHuman},
		{"an approved unexecuted release is waiting", func(t *testing.T, f *j5Fixture) {
			f.addOperation(t, "release")
			f.setReleaseState(t, 1, "approved")
		}, StatusAwaitingCompletionApproval},
		{"an executed release is not waiting", func(t *testing.T, f *j5Fixture) {
			f.addOperation(t, "release")
			f.setReleaseState(t, 1, "executed")
		}, StatusAwaitingHuman},
		{"a rejected release is not waiting", func(t *testing.T, f *j5Fixture) {
			f.addOperation(t, "release")
			f.setReleaseState(t, 1, "rejected")
		}, StatusAwaitingHuman},
		{"a closed release is not waiting", func(t *testing.T, f *j5Fixture) {
			f.addOperation(t, "release")
			f.setReleaseState(t, 1, "closed")
		}, StatusAwaitingHuman},
		{"a pending release of another challenge is not this one's", func(t *testing.T, f *j5Fixture) {
			other, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "other"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: other.ID, Kind: "release", Summary: "other release"}); err != nil {
				t.Fatal(err)
			}
		}, StatusAwaitingHuman},
		{"a pending release of this challenge", func(t *testing.T, f *j5Fixture) { f.addOperation(t, "release") }, StatusAwaitingCompletionApproval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJ5Fixture(t, "impl")
			f.delegateToVerifying(t, "C-1")
			tc.setup(t, f)
			f.j5.result = j5OutDeferred("met", nil, nil, [2]string{condAfterRelease, "after_release"})
			if _, err := f.verify(t, nil); err != nil {
				t.Fatal(err)
			}
			if got := f.detail(t, "C-1").Status; got != tc.wantEnd {
				t.Errorf("status = %s, want %s", got, tc.wantEnd)
			}
		})
	}
}

// (e) 本番反映が無い保留へ回答すると、課題は検証中へ戻り（T12）、J5 が回答に従って判定し直す。
func TestVerifyAutoJ5_NoPendingReleaseHold_AnswerReturnsToVerifyingAndTheNextJudgmentDecides(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.result = j5OutDeferred("met", nil, nil, [2]string{condAfterRelease, "after_release"})
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s", got)
	}
	att := f.budgetAttestation(t, "C-1")
	req := AnswerRequest{ChallengeID: "C-1", ExpectedVersion: f.version(t, "C-1"), Answer: "条件が不要"}
	if _, _, err := f.s.ExecuteAnswer(context.Background(), req, att); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusVerifying {
		t.Fatalf("status after the answer = %s, want verifying", got)
	}
	// 回答を踏まえた判定（その条件を除いて met）で、完了確認待ちへ進む。
	f.j5.result = j5Out("met", nil, nil)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Errorf("status after the re-verification = %s, want awaiting_completion_approval", got)
	}
}

// 人の操作の後でしか満たせない条件と after_release が混在し、待っている本番反映も無いとき、保留は 1 つで
// 両方を伝える（人の操作の後に回答しても、すぐ次の保留になる往復を避ける）。待っている本番反映があれば
// 人の操作の保留のまま（after_release の条件は書かない）。
func TestVerifyAutoJ5_AfterHumanActionAndAfterReleaseWithNoPendingRelease_OneHoldListsBoth(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.result = j5OutDeferred("met", nil, nil,
		[2]string{condAfterRelease, "after_release"}, [2]string{condAfterHuman, "after_human_action"})
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s", got)
	}
	if got := f.holdCount(t); got != "1" {
		t.Errorf("holds = %s, want 1", got)
	}
	q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`)
	if !strings.HasPrefix(q, J5HumanActionHoldPreamble) || !strings.Contains(q, "- "+condAfterHuman) ||
		!strings.Contains(q, J5NoReleaseHoldPreamble) || !strings.Contains(q, "- "+condAfterRelease) {
		t.Errorf("hold question = %q, want both the human-action and the no-release parts", q)
	}
}

// 予想と実物の個数の食い違いの保留にも、本番反映が無い旨と条件を書き、回答の往復を 1 回にする。
func TestVerifyAutoJ5_NoPendingReleaseWithActualMismatch_OneHoldListsBoth(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.setExpected(t, "C-1", 2)
	b, _ := json.Marshal(map[string]any{
		"verdict": "met", "reason": "R", "feedback": nil, "question": nil,
		"actual":              []map[string]string{{"scene": "s", "after": "a"}},
		"deferred_conditions": []map[string]string{{"condition": condAfterRelease, "status": "after_release"}},
	})
	f.j5.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: b, ReportedTotalCostUSD: float64Ptr(0.5)}
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.holdCount(t); got != "1" {
		t.Errorf("holds = %s, want 1", got)
	}
	q := f.queryString(t, `SELECT question FROM hold WHERE challenge_id = 1`)
	if !strings.HasPrefix(q, ActualMismatchHoldPreamble) || !strings.Contains(q, J5NoReleaseHoldPreamble) || !strings.Contains(q, "- "+condAfterRelease) {
		t.Errorf("hold question = %q, want the mismatch and the no-release parts", q)
	}
	if got := f.detail(t, "C-1").Actual; len(got) != 0 {
		t.Errorf("actual = %+v, want unchanged (empty)", got)
	}
}

// 承認済みで未実行の release しか無くても完了確認待ちへ進み、完了の承認の要約の PendingReleases には
// state=pending の release だけが入る（approved を二重に承認しない）。条件は要約に出る。
func TestVerifyAutoJ5_ApprovedUnexecutedReleaseOnly_AwaitsCompletionApprovalWithoutListingItAsPending(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.addOperation(t, "release")
	f.setReleaseState(t, 1, "approved")
	f.j5.result = j5OutDeferred("met", nil, nil, [2]string{condAfterRelease, "after_release"})
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s", got)
	}
	if got := f.holdCount(t); got != "0" {
		t.Errorf("holds = %s, want none", got)
	}
	p := f.approvalPreview(t, "C-1")
	if len(p.PendingReleases) != 0 {
		t.Errorf("PendingReleases = %+v, want none", p.PendingReleases)
	}
	if want := []string{condAfterRelease}; !reflect.DeepEqual(p.ConditionsAfterRelease, want) {
		t.Errorf("ConditionsAfterRelease = %v, want %v", p.ConditionsAfterRelease, want)
	}
}

// j5ReleaseRoute は「本番反映で満たされる見込み」の条件だけが残る met の出力を検証し、事前の読み取り
// （j5Operation）の根拠を返す。
func (f *j5Fixture) j5ReleaseRoute(t *testing.T) (*j5ValidatedOutput, j5Route) {
	t.Helper()
	v, ok := validateJ5Output(j5OutDeferred("met", nil, nil, [2]string{condAfterRelease, "after_release"}).StructuredOutput)
	if !ok {
		t.Fatal("invalid output")
	}
	rt, err := f.s.j5Operation(context.Background(), "C-1", v)
	if err != nil {
		t.Fatal(err)
	}
	return v, rt
}

// 事前の読み取りの後で release の state が変わると、書き込み内の読み直しが食い違いを検出して書かず
// （errJ5StaleExpected）、選び直した applyJ5 が新しい状態で写す。向きは 2 つ:
// (a) 待っている release があった→無くなった: 選び直すと人間待ちの保留に置く。
// (b) 無かった→現れた: 選び直すと保留を作らず完了確認待ちへ進む。
func TestApplyJ5_ReleaseStateChangesAfterTheFirstRead_IsRereadAndReselected(t *testing.T) {
	ctx := context.Background()
	t.Run("a waiting release disappears", func(t *testing.T) {
		f := newJ5Fixture(t, "impl")
		f.delegateToVerifying(t, "C-1")
		f.addOperation(t, "release")
		f.setReleaseState(t, 1, "approved")
		v, rt := f.j5ReleaseRoute(t)
		if rt.noRelease || rt.op != OpVerifyMet {
			t.Fatalf("route = %+v", rt)
		}
		f.setReleaseState(t, 1, "executed") // 最初の読み取りの後で待っている本番反映が無くなる
		if _, _, err := f.s.applyJ5Route(ctx, "R-1", "C-1", v, nil, rt); !errors.Is(err, errJ5StaleExpected) {
			t.Fatalf("err = %v, want errJ5StaleExpected", err)
		}
		if got := f.detail(t, "C-1").Status; got != StatusVerifying {
			t.Fatalf("status = %s, want unchanged verifying", got)
		}
		mapped, held, err := f.s.applyJ5(ctx, "R-1", "C-1", v, nil)
		if err != nil || !mapped || !held {
			t.Fatalf("applyJ5 = mapped %v, held %v, %v; want mapped and held", mapped, held, err)
		}
		if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
			t.Errorf("status = %s, want %s", got, StatusAwaitingHuman)
		}
		if got := f.holdCount(t); got != "1" {
			t.Errorf("holds = %s, want 1", got)
		}
	})
	t.Run("a waiting release appears", func(t *testing.T) {
		f := newJ5Fixture(t, "impl")
		f.delegateToVerifying(t, "C-1")
		v, rt := f.j5ReleaseRoute(t)
		if !rt.noRelease {
			t.Fatalf("route = %+v, want noRelease", rt)
		}
		f.addOperation(t, "release") // 最初の読み取りの後で未承認の release が現れる
		if _, _, err := f.s.applyJ5Route(ctx, "R-1", "C-1", v, nil, rt); !errors.Is(err, errJ5StaleExpected) {
			t.Fatalf("err = %v, want errJ5StaleExpected", err)
		}
		if got := f.detail(t, "C-1").Status; got != StatusVerifying {
			t.Fatalf("status = %s, want unchanged verifying", got)
		}
		mapped, held, err := f.s.applyJ5(ctx, "R-1", "C-1", v, nil)
		if err != nil || !mapped || held {
			t.Fatalf("applyJ5 = mapped %v, held %v, %v; want mapped and not held", mapped, held, err)
		}
		if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
			t.Errorf("status = %s, want %s", got, StatusAwaitingCompletionApproval)
		}
		if got := f.holdCount(t); got != "0" {
			t.Errorf("holds = %s, want none", got)
		}
	})
}
