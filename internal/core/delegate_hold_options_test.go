package core

import (
	"reflect"
	"strings"
	"testing"
)

// 契約の判断待ちの保留は、委譲先が返した選択肢を（件数によらず）そのまま持つ。識別は core が o1・o2… と振り、
// 問いの文は選択肢を読まない表示でも読める（番号つきの文のまま）。
func TestContractWaitHold_CarriesTheDelegatesOptions(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	ra := &ContractRequestedAction{Kind: "answer", Decider: "human", Text: "どちらにするか", Options: []string{"OPT-A", "OPT-B", "OPT-C", "OPT-D", "OPT-E", "OPT-F", "OPT-G"}}
	f.client.queue = []ContractLaunchOutput{waitingOut(ra, nil)}
	f.poll(t)

	h := f.detail(t, f.cid).Holds[0]
	if len(h.Options) != 7 || h.Options[0] != (HoldOption{ID: "o1", Label: "OPT-A"}) || h.Options[6] != (HoldOption{ID: "o7", Label: "OPT-G"}) {
		t.Fatalf("options = %+v", h.Options)
	}
	if !strings.Contains(h.Question, "選択肢:") || !strings.Contains(h.Question, "1. OPT-A") || !strings.Contains(h.Question, "7. OPT-G") {
		t.Fatalf("question = %q", h.Question)
	}
}

// 選択肢の無い求めは、選択肢を持たない（いまと同じ）。
func TestContractWaitHold_WithoutOptionsHasNone(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "approve", Decider: "human", Text: "承認して"}, nil)}
	f.poll(t)
	if h := f.detail(t, f.cid).Holds[0]; h.Options != nil {
		t.Fatalf("options = %+v, want nil", h.Options)
	}
}

// 選択肢で答えると、次の回りで委譲先へ返す値は選んだ見出し。添え書きは次の行に続く。
func TestContractWaitHold_OptionAnswerIsReturnedAsTheLabel(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ answer, want string }{{"", "OPT-B"}, {"理由は X", "OPT-B\n理由は X"}} {
		f := newContractFixture(t)
		if _, err := f.start(t); err != nil {
			t.Fatal(err)
		}
		f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "human", Text: "t", Options: []string{"OPT-A", "OPT-B"}}, nil)}
		f.poll(t)
		if _, _, err := answerWith(t, f.s, f.cid, c.answer, "o2", false); err != nil {
			t.Fatal(err)
		}
		f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
		f.poll(t)
		if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], []string{"tool", "resume", "ext-1", "--answer", c.want}) {
			t.Fatalf("answer %q: resumes = %v, want the answer %q", c.answer, f.client.resumes, c.want)
		}
	}
}

// 報告の問いが 1 件で選択肢があれば、その選択肢をそのまま持つ（7 件でも）。問いの文は選択肢なしと同じ文（「選択肢:」の行を残す）。
func TestQuestionsHold_SingleQuestionCarriesItsOptions(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	opts := []string{"A", "B", "C", "D", "E", "F", "G"}
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: reportJSON(func(m map[string]any) {
		m["outcome"] = "questions"
		m["questions"] = []any{map[string]any{"kind": "scope", "text": "QUESTION-ONE", "options": opts, "recommendation": nil}}
	})}
	f.runDelegation(t, id)
	h := f.detail(t, id).Holds[0]
	if len(h.Options) != 7 || h.Options[0] != (HoldOption{ID: "o1", Label: "A"}) || h.Options[6] != (HoldOption{ID: "o7", Label: "G"}) {
		t.Fatalf("options = %+v", h.Options)
	}
	want := formatQuestionsHold(&DelegationReport{Summary: "done", Questions: []DelegationQuestion{{Kind: "scope", Text: "QUESTION-ONE", Options: opts}}})
	if h.Question != want || !strings.Contains(h.Question, "選択肢: A / B") {
		t.Fatalf("question = %q, want %q", h.Question, want)
	}
}

// 問いが 2 件以上の報告は、選択肢を持たない。1 件で選択肢の無い問いも持たない。
func TestQuestionsHold_MultipleOrOptionlessQuestionsHaveNoOptions(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = questionsOut("feat/x") // 2 件。1 件目に選択肢がある
	f.runDelegation(t, id)
	if h := f.detail(t, id).Holds[0]; h.Options != nil {
		t.Fatalf("options = %+v, want nil", h.Options)
	}
	if got := questionsHoldOptions(&DelegationReport{Questions: []DelegationQuestion{{Kind: "minor", Text: "x"}}}); got.Options != nil {
		t.Fatalf("options = %+v, want none", got.Options)
	}
}

// 見出しにできない選択肢（空・空白だけ・複数行）が 1 つでもあれば、選択肢を持たせず、保留は作る（問いの文には番号つきで残る）。
func TestHoldOptionsFromLabels_UnusableLabelDropsAllOptions(t *testing.T) {
	t.Parallel()
	for _, labels := range [][]string{{"A", ""}, {"A", "  "}, {"A", "B\nC"}, {"A", "B\rC"}} {
		if got := holdOptionsFromLabels(labels); got.Options != nil {
			t.Errorf("%q: options = %+v, want none", labels, got.Options)
		}
	}
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "human", Text: "t", Options: []string{"A", "B\nC"}}, nil)}
	f.poll(t)
	d := f.detail(t, f.cid)
	if len(d.Holds) != 1 || d.Holds[0].Options != nil || !strings.Contains(d.Holds[0].Question, "1. A") {
		t.Fatalf("holds = %+v", d.Holds)
	}
	g := newResumeFixture(t)
	id := g.newInProgress(t, "t", "P1", planSpec(nil))
	g.deleg.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: reportJSON(func(m map[string]any) {
		m["outcome"] = "questions"
		m["questions"] = []any{map[string]any{"kind": "scope", "text": "Q", "options": []string{"A", "B\nC"}, "recommendation": nil}}
	})}
	g.runDelegation(t, id)
	if hs := g.detail(t, id).Holds; len(hs) != 1 || hs[0].Options != nil {
		t.Fatalf("holds = %+v", hs)
	}
}
