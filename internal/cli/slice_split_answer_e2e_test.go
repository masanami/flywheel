package cli

import (
	"os"
	"path/filepath"
	"testing"
)

var sliceSplitRelayed = []string{"--relayed", "--session", "s", "--quote", "答える"}

// 取り込み → 分類 → 区切りを分ける判断点 → 回答 → 計画 → 片付けを、CLI の口だけで通して確かめる
// （GitHub と判断点は偽物。区切りの課題は取り下げで終端にする）。
func TestSliceSplitAnswer_SplitEndToEnd(t *testing.T) {
	parallelTools(t, false)
	ws, id, _ := setupClassifiedSliceSplit(t, j10SplitFixture)
	evidence := filepath.Join(ws, ".flywheel", "requirements", "split-"+id+".md")

	runJSON(t, ws, append([]string{"answer", id, "--option", "split", "--answer", "添え書き"}, sliceSplitRelayed...)...)

	original := runJSON(t, ws, "show", id)
	if got := original["challenge"].(map[string]any)["status"]; got != "withdrawn" {
		t.Fatalf("original status = %v, want withdrawn", got)
	}
	split := original["slice_split"].(map[string]any)
	if split["state"] != "split" || split["hold_id"] != nil {
		t.Errorf("slice_split = %v", split)
	}
	if got := original["holds"].([]any)[0].(map[string]any)["answer"]; got != "この案で分ける\n添え書き" {
		t.Errorf("hold answer = %v", got)
	}
	var children []string
	for _, s := range split["slices"].([]any) {
		children = append(children, s.(map[string]any)["challenge_id"].(string))
	}
	if len(children) != 2 {
		t.Fatalf("children = %v, want 2", children)
	}
	for i, child := range children {
		shown := runJSON(t, ws, "show", child)
		challenge := shown["challenge"].(map[string]any)
		req := shown["requirement"].(map[string]any)
		if challenge["status"] != "classified" || challenge["title"] != []string{"a", "b"}[i] ||
			req["origin"] != "issue" || req["external_key"] != "o/r#1" || req["origin_challenge_id"] != id {
			t.Errorf("child %s: challenge = %v requirement = %v", child, challenge, req)
		}
	}
	if _, err := os.Stat(evidence); err != nil {
		t.Errorf("the evidence must remain until the requirement is resolved: %v", err)
	}

	// 区切りの課題は、分類を通らずに計画へ進める。
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j2RoutePlan(t)}, "")
	phase := runJSON(t, ws, "plan", "--auto", children[0])["phase"].(map[string]any)
	if items := phase["items"].([]any); len(items) != 1 {
		t.Fatalf("plan --auto %s: phase = %v, want one planned item", children[0], phase)
	}

	// 区切りの課題がすべて終端になると、片付けの段が証跡を消して要求を解決する。
	for _, child := range children {
		runJSON(t, ws, withdrawRelayed(child)...)
	}
	cycleDoc(t, ws)
	if _, err := os.Lstat(evidence); !os.IsNotExist(err) {
		t.Errorf("the evidence remains after the requirement is resolved (err = %v)", err)
	}
	if req := runJSON(t, ws, "show", children[0])["requirement"].(map[string]any); req["resolved_at"] == nil {
		t.Errorf("requirement = %v, want resolved_at", req)
	}
}

// 「分けない」と答えると、同じ課題が分類済へ戻り、証跡が消えて、計画へ進める。
func TestSliceSplitAnswer_SingleEndToEnd(t *testing.T) {
	parallelTools(t, false)
	ws, id, _ := setupClassifiedSliceSplit(t, j10SplitFixture)

	runJSON(t, ws, append([]string{"answer", id, "--option", "single"}, sliceSplitRelayed...)...)

	shown := runJSON(t, ws, "show", id)
	split := shown["slice_split"].(map[string]any)
	if shown["challenge"].(map[string]any)["status"] != "classified" || split["state"] != "single" || split["hold_id"] != nil || shown["requirement"] != nil {
		t.Errorf("show = %v / %v", shown["challenge"], split)
	}
	if _, err := os.Lstat(filepath.Join(ws, ".flywheel", "requirements", "split-"+id+".md")); !os.IsNotExist(err) {
		t.Errorf("the evidence remains (err = %v)", err)
	}
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j2RoutePlan(t)}, "")
	phase := runJSON(t, ws, "plan", "--auto", id)["phase"].(map[string]any)
	if items := phase["items"].([]any); len(items) != 1 {
		t.Errorf("plan --auto: phase = %v, want one planned item", phase)
	}
}

// 分けるかの保留を開いたまま取り下げると、証跡が消え、記録は awaiting_owner のまま保留の識別が空になる。
func TestSliceSplitAnswer_WithdrawEndToEnd(t *testing.T) {
	parallelTools(t, false)
	ws, id, _ := setupClassifiedSliceSplit(t, j10SplitFixture)

	runJSON(t, ws, withdrawRelayed(id)...)
	split := runJSON(t, ws, "show", id)["slice_split"].(map[string]any)
	if split["state"] != "awaiting_owner" || split["hold_id"] != nil {
		t.Errorf("slice_split = %v", split)
	}
	if _, err := os.Lstat(filepath.Join(ws, ".flywheel", "requirements", "split-"+id+".md")); !os.IsNotExist(err) {
		t.Errorf("the evidence remains (err = %v)", err)
	}
}
