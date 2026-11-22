package view

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// 操作の出力のキー cycle は、本体のキーの後ろに足され、started と reason を持つ。
func TestWithCycle_AppendsTheCycleKey(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(WithCycle{Body: BudgetResponse{ChallengeID: "C-1"}, Cycle: FromCycleStartReason(core.CycleStartStarted)})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.HasSuffix(got, `,"cycle":{"reason":"started","started":true}}`) || !strings.Contains(got, `"challenge_id":"C-1"`) {
		t.Errorf("got %s", got)
	}
	b, err = json.Marshal(WithCycle{Body: struct{}{}, Cycle: FromCycleStartReason(core.CycleStartNoWork)})
	if err != nil || string(b) != `{"cycle":{"reason":"no_work","started":false}}` {
		t.Errorf("empty body: %s err=%v", b, err)
	}
	if _, err := json.Marshal(WithCycle{Body: []int{1}, Cycle: CycleStart{}}); err == nil {
		t.Error("a non-object body must be rejected")
	}
}

// 理由 started のときだけ started が真。
func TestFromCycleStartReason_StartedOnlyForStarted(t *testing.T) {
	t.Parallel()
	for _, r := range core.CycleStartReasonValues() {
		if got := FromCycleStartReason(r); got.Started != (r == core.CycleStartStarted) || got.Reason != string(r) {
			t.Errorf("%s: %+v", r, got)
		}
	}
}
