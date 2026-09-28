package invoker

import (
	"errors"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// AC: §結果の判別の表を上から評価する（AC-41〜48相当）。

func TestClassifyOutcome_LaunchFailed(t *testing.T) {
	result, resp := classifyOutcome(errors.New("boom"), false, nil)
	if result != core.RunResultLaunchFailed {
		t.Errorf("result = %v, want launch_failed", result)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil", resp)
	}
}

func TestClassifyOutcome_TimedOut(t *testing.T) {
	result, resp := classifyOutcome(nil, true, []byte(`{"is_error":false}`))
	if result != core.RunResultTimedOut {
		t.Errorf("result = %v, want timed_out", result)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil (timed_out ignores stdout)", resp)
	}
}

func TestClassifyOutcome_MalformedNotJSON(t *testing.T) {
	result, _ := classifyOutcome(nil, false, []byte("not json at all"))
	if result != core.RunResultMalformed {
		t.Errorf("result = %v, want malformed", result)
	}
}

func TestClassifyOutcome_MalformedEmptyStdout(t *testing.T) {
	result, _ := classifyOutcome(nil, false, nil)
	if result != core.RunResultMalformed {
		t.Errorf("result = %v, want malformed", result)
	}
}

func TestClassifyOutcome_MalformedTrailingGarbage(t *testing.T) {
	result, _ := classifyOutcome(nil, false, []byte(`{"is_error":false}garbage`))
	if result != core.RunResultMalformed {
		t.Errorf("result = %v, want malformed", result)
	}
}

func TestClassifyOutcome_BudgetExhausted(t *testing.T) {
	result, resp := classifyOutcome(nil, false, []byte(`{"subtype":"error_max_budget_usd","is_error":true}`))
	if result != core.RunResultBudgetExhausted {
		t.Errorf("result = %v, want budget_exhausted", result)
	}
	if resp == nil {
		t.Fatal("resp should be non-nil")
	}
}

func TestClassifyOutcome_Errored(t *testing.T) {
	result, _ := classifyOutcome(nil, false, []byte(`{"subtype":"error_other","is_error":true}`))
	if result != core.RunResultErrored {
		t.Errorf("result = %v, want errored", result)
	}
}

func TestClassifyOutcome_InvalidOutputMissingStructuredOutput(t *testing.T) {
	result, _ := classifyOutcome(nil, false, []byte(`{"is_error":false}`))
	if result != core.RunResultInvalidOutput {
		t.Errorf("result = %v, want invalid_output", result)
	}
}

// AC「result の本文に "verdict": "mine" を含むが structured_output の無い
// 結果も、run の結果が invalid_output になる（判別に子の自由記述を使わない
// ことの検証）」
func TestClassifyOutcome_InvalidOutputIgnoresFreeTextResultField(t *testing.T) {
	result, _ := classifyOutcome(nil, false, []byte(`{"is_error":false,"result":"{\"verdict\": \"mine\"}"}`))
	if result != core.RunResultInvalidOutput {
		t.Errorf("result = %v, want invalid_output (free-text result must not drive classification)", result)
	}
}

func TestClassifyOutcome_Succeeded(t *testing.T) {
	result, resp := classifyOutcome(nil, false, []byte(`{"is_error":false,"structured_output":{"verdict":"mine"}}`))
	if result != core.RunResultSucceeded {
		t.Errorf("result = %v, want succeeded", result)
	}
	if resp == nil || string(resp.StructuredOutput) != `{"verdict":"mine"}` {
		t.Errorf("resp.StructuredOutput = %s, want the structured_output value", resp.StructuredOutput)
	}
}

func TestClassifyOutcome_NullStructuredOutputIsInvalid(t *testing.T) {
	result, _ := classifyOutcome(nil, false, []byte(`{"is_error":false,"structured_output":null}`))
	if result != core.RunResultInvalidOutput {
		t.Errorf("result = %v, want invalid_output", result)
	}
}

func TestClassifyOutcome_NullTopLevelIsMalformed(t *testing.T) {
	result, _ := classifyOutcome(nil, false, []byte("null"))
	if result != core.RunResultMalformed {
		t.Errorf("result = %v, want malformed for a top-level null", result)
	}
}

func TestClassifyOutcome_TopLevelArrayIsMalformed(t *testing.T) {
	result, _ := classifyOutcome(nil, false, []byte(`[{"is_error":false}]`))
	if result != core.RunResultMalformed {
		t.Errorf("result = %v, want malformed for a top-level array", result)
	}
}

// 順序: subtype=error_max_budget_usd が is_error=true より優先される
// （budget_exhausted は表の4行目、errored は5行目）。
func TestClassifyOutcome_BudgetExhaustedTakesPriorityOverErrored(t *testing.T) {
	result, _ := classifyOutcome(nil, false, []byte(`{"subtype":"error_max_budget_usd","is_error":true,"structured_output":null}`))
	if result != core.RunResultBudgetExhausted {
		t.Errorf("result = %v, want budget_exhausted (must be evaluated before errored/invalid_output rows)", result)
	}
}
