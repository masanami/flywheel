package view

import (
	"encoding/json"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// supervise の段は、処理した件数と、手ごとの件数（手の閉集合の 7 つを常に出す）を、キーの辞書順で出す。
func TestFromCyclePhase_Supervise(t *testing.T) {
	t.Parallel()
	p := core.CyclePhaseResult{
		Phase: core.CyclePhaseSupervise, SuperviseProcessed: 3,
		SuperviseHands: map[core.SupervisorHand]int{core.HandDefer: 2, core.HandEscalate: 1},
	}
	b, err := json.Marshal(FromCyclePhase(p))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"hands":{"defer":2,"escalate":1,"replan":0,"retry":0,"reverify":0,"send_back":0,"split_followup":0},"not_started":[],"phase":"supervise","processed":3,"skipped":false}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}
