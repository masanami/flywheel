package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// AC-207: cycle の出力（テキストと --json）に、その回に足したタスク（課題・題名・出どころ）が入る。
func TestCycleOutputCarriesTheAddedTasks_AC207(t *testing.T) {
	t.Parallel()
	res := &core.CycleRunResult{
		Cycle: &core.Cycle{ID: "Y-1", Trigger: "manual", Result: core.CycleResultCompleted, StartedAt: time.Unix(0, 0)},
		AddedTasks: []core.AddedTask{
			{ChallengeID: "C-7", TaskID: "T-12", Title: "続きの調査", Origin: core.TaskAdditionOriginExpand},
		},
	}
	text := cycleResultText(res)
	if !strings.Contains(text, "足したタスク: 1 件\n    C-7 T-12 続きの調査（出どころ: expand）\n") {
		t.Errorf("text = %q, want the added task with its challenge, title and origin", text)
	}
	b, err := json.Marshal(view.FromCycleRunResult(res))
	if err != nil {
		t.Fatal(err)
	}
	want := `"added_tasks":[{"challenge_id":"C-7","origin":"expand","task_id":"T-12","title":"続きの調査"}]`
	if !strings.Contains(string(b), want) {
		t.Errorf("json = %s, want %s", b, want)
	}
	res.AddedTasks = nil
	if text := cycleResultText(res); strings.Contains(text, "足したタスク") {
		t.Errorf("text = %q, want no added-task line without added tasks", text)
	}
	b, _ = json.Marshal(view.FromCycleRunResult(res))
	if !strings.Contains(string(b), `"added_tasks":[]`) {
		t.Errorf("json = %s, want an empty added_tasks array", b)
	}
}
