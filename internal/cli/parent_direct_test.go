package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは、親が直接行った作業を完了として記録する道（課題 #452）の CLI を確かめる。
// hold --kind・complete-direct・status の「親のエージェントが動く番」・show・承認の要約。

// classifiedChallenge は分類済の課題を作って ID を返す。
func classifiedChallenge(t *testing.T, ws, doneCriteria string) string {
	t.Helper()
	args := []string{"create", "--title", "親が直接行う課題"}
	if doneCriteria != "" {
		args = append(args, "--done-criteria", doneCriteria)
	}
	created := runJSON(t, ws, args...)
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P1")
	return id
}

func parentDirectHeld(t *testing.T, ws, doneCriteria string) string {
	t.Helper()
	id := classifiedChallenge(t, ws, doneCriteria)
	doc := runJSON(t, ws, "hold", id, "--question", "親が直接行う", "--kind", "parent_direct")
	if got := doc["challenge"].(map[string]any)["status"]; got != "awaiting_human" {
		t.Fatalf("status after hold = %v, want awaiting_human", got)
	}
	return id
}

func idsOf(t *testing.T, v any) []string {
	t.Helper()
	out := []string{}
	for _, c := range v.([]any) {
		out = append(out, c.(map[string]any)["id"].(string))
	}
	return out
}

func TestRunHold_KindFlag(t *testing.T) {
	ws := initializedWorkspace(t)
	id := parentDirectHeld(t, ws, "")
	show := runJSON(t, ws, "show", id)
	holds := show["holds"].([]any)
	if len(holds) != 1 || holds[0].(map[string]any)["kind"] != "parent_direct" {
		t.Errorf("holds = %v, want one parent_direct hold", holds)
	}

	// 指定なしは従来の種類（人の対応を待つ）。
	id2 := classifiedChallenge(t, ws, "")
	runJSON(t, ws, "hold", id2, "--question", "聞く")
	holds2 := runJSON(t, ws, "show", id2)["holds"].([]any)
	if len(holds2) != 1 || holds2[0].(map[string]any)["kind"] != "human" {
		t.Errorf("default hold = %v, want kind human", holds2)
	}

	// 閉集合の外は validation_failed で、何も書かない。
	id3 := classifiedChallenge(t, ws, "")
	requireErrorCode(t, []string{"hold", id3, "--question", "q", "--kind", "bogus", "--workspace", ws}, 1, CodeValidationFailed)
	if got := workStatus(t, ws, id3); got != "classified" {
		t.Errorf("status after rejected hold = %q, want classified", got)
	}
}

func TestRunCompleteDirect_RecordsArtifactsAndNoteAndMovesToAwaitingCompletionApproval(t *testing.T) {
	ws := initializedWorkspace(t)
	id := parentDirectHeld(t, ws, "結果が Issue に書かれている")

	doc := runJSON(t, ws, "complete-direct", id,
		"--artifact", "https://github.com/o/r/issues/337#issuecomment-1", "--artifact", "o/r#12", "--note", "案 B を動かして確かめた")
	c := doc["challenge"].(map[string]any)
	if c["status"] != "awaiting_completion_approval" || c["status_label"] != "完了確認待ち" {
		t.Errorf("status/label = %v/%v, want awaiting_completion_approval/完了確認待ち", c["status"], c["status_label"])
	}
	dc := doc["direct_completion"].(map[string]any)
	arts := dc["artifacts"].([]any)
	if len(arts) != 2 || arts[0] != "https://github.com/o/r/issues/337#issuecomment-1" || arts[1] != "o/r#12" {
		t.Errorf("artifacts = %v", arts)
	}
	if dc["note"] != "案 B を動かして確かめた" {
		t.Errorf("note = %v", dc["note"])
	}

	// 完了の検証（J5）は起動していない: run が 1 件も無い（番兵の claude も呼ばれていない）。
	if runs := runJSON(t, ws, "runs")["runs"].([]any); len(runs) != 0 {
		t.Errorf("runs = %v, want none", runs)
	}
	// show に記録が出る。
	show := runJSON(t, ws, "show", id)
	dcs := show["direct_completions"].([]any)
	if len(dcs) != 1 || len(dcs[0].(map[string]any)["artifacts"].([]any)) != 2 {
		t.Errorf("show direct_completions = %v", dcs)
	}
	// 保留は回答なしで閉じている。
	h := show["holds"].([]any)[0].(map[string]any)
	if h["answer"] != nil || h["closed_at"] == nil {
		t.Errorf("hold = %v, want closed without an answer", h)
	}
}

func TestRunCompleteDirect_NoteIsOptional(t *testing.T) {
	ws := initializedWorkspace(t)
	id := parentDirectHeld(t, ws, "")
	doc := runJSON(t, ws, "complete-direct", id, "--artifact", "https://example.com/1")
	if doc["direct_completion"].(map[string]any)["note"] != nil {
		t.Errorf("note = %v, want null", doc["direct_completion"].(map[string]any)["note"])
	}
}

func TestRunCompleteDirect_Rejections(t *testing.T) {
	ws := initializedWorkspace(t)
	direct := parentDirectHeld(t, ws, "")

	// 成果物の参照が 0 件は usage_error（終了コード 2）。
	requireErrorCode(t, []string{"complete-direct", direct, "--workspace", ws}, 2, CodeUsageError)
	// 空の参照は validation_failed。
	requireErrorCode(t, []string{"complete-direct", direct, "--artifact", "  ", "--workspace", ws}, 1, CodeValidationFailed)
	if got := workStatus(t, ws, direct); got != "awaiting_human" {
		t.Errorf("status after rejected calls = %q, want awaiting_human", got)
	}

	// 別の種類（人の対応を待つ）の保留からは使えない。
	human := classifiedChallenge(t, ws, "")
	runJSON(t, ws, "hold", human, "--question", "聞く")
	requireErrorCode(t, []string{"complete-direct", human, "--artifact", "https://example.com/1", "--workspace", ws}, 1, CodeInvalidTransition)

	// 分類済・計画済の課題からは使えない。
	classified := classifiedChallenge(t, ws, "")
	requireErrorCode(t, []string{"complete-direct", classified, "--artifact", "https://example.com/1", "--workspace", ws}, 1, CodeInvalidTransition)

	// 存在しない課題。
	requireErrorCode(t, []string{"complete-direct", "C-999", "--artifact", "https://example.com/1", "--workspace", ws}, 1, CodeNotFound)

	// 位置引数が無ければ usage_error。
	requireErrorCode(t, []string{"complete-direct", "--artifact", "https://example.com/1", "--workspace", ws}, 2, CodeUsageError)
}

func TestRunCompleteDirect_WithdrawnAndDoneAreTerminal(t *testing.T) {
	ws := initializedWorkspace(t)
	id := parentDirectHeld(t, ws, "")
	runJSON(t, ws, "complete-direct", id, "--artifact", "https://example.com/1")
	runJSON(t, ws, "approve", id, "--hold-release", "--relayed", "--session", "s", "--quote", "OK")
	if got := workStatus(t, ws, id); got != "done" {
		t.Fatalf("status after approval = %q, want done", got)
	}
	requireErrorCode(t, []string{"complete-direct", id, "--artifact", "https://example.com/1", "--workspace", ws}, 1, CodeTerminalState)
}

func TestRunStatus_ParentDirectHoldIsParentAgentsTurn(t *testing.T) {
	ws := initializedWorkspace(t)
	direct := parentDirectHeld(t, ws, "")
	human := classifiedChallenge(t, ws, "")
	runJSON(t, ws, "hold", human, "--question", "聞く")

	doc := runJSON(t, ws, "status")
	if got := idsOf(t, doc["parent_agent"].(map[string]any)["challenges"]); len(got) != 1 || got[0] != direct {
		t.Errorf("parent_agent.challenges = %v, want [%s]", got, direct)
	}
	if got := idsOf(t, doc["needs_human"].(map[string]any)["challenges"]); len(got) != 1 || got[0] != human {
		t.Errorf("needs_human.challenges = %v, want [%s]", got, human)
	}
	// 状態は人間対応待ちのまま（新しい状態は作らない）。
	for _, c := range doc["parent_agent"].(map[string]any)["challenges"].([]any) {
		if c.(map[string]any)["status"] != "awaiting_human" {
			t.Errorf("status = %v, want awaiting_human", c.(map[string]any)["status"])
		}
	}

	text := runText(t, ws, "status")
	if !strings.Contains(text, "親のエージェントが動く番:") {
		t.Fatalf("status text lacks the parent agent section:\n%s", text)
	}
	section := text[strings.Index(text, "親のエージェントが動く番:"):]
	if i := strings.Index(section, "システムが次に進められるもの:"); i >= 0 {
		section = section[:i]
	}
	if !strings.Contains(section, direct) || strings.Contains(section, human) {
		t.Errorf("parent agent section = %q, want only %s", section, direct)
	}
	humanSection := text[:strings.Index(text, "親のエージェントが動く番:")]
	if strings.Contains(humanSection, direct) {
		t.Errorf("the human section lists the parent-direct challenge:\n%s", humanSection)
	}
}

func TestRunStatus_ParentDirectChallengeMovesToNeedsHumanAfterCompleteDirect(t *testing.T) {
	ws := initializedWorkspace(t)
	id := parentDirectHeld(t, ws, "")
	runJSON(t, ws, "complete-direct", id, "--artifact", "https://example.com/1")
	doc := runJSON(t, ws, "status")
	if got := idsOf(t, doc["parent_agent"].(map[string]any)["challenges"]); len(got) != 0 {
		t.Errorf("parent_agent.challenges = %v, want none", got)
	}
	if got := idsOf(t, doc["needs_human"].(map[string]any)["challenges"]); len(got) != 1 || got[0] != id {
		t.Errorf("needs_human.challenges = %v, want [%s]", got, id)
	}
}

func TestRunShow_TextListsDirectCompletionAndHoldKind(t *testing.T) {
	ws := initializedWorkspace(t)
	id := parentDirectHeld(t, ws, "")
	runJSON(t, ws, "complete-direct", id, "--artifact", "https://example.com/result", "--note", "確かめた")
	text := runText(t, ws, "show", id)
	for _, want := range []string{"親が直接行った記録", "https://example.com/result", "確かめた", "親が直接行う"} {
		if !strings.Contains(text, want) {
			t.Errorf("show text lacks %q:\n%s", want, text)
		}
	}
}

func TestApprovalSummaryText_ShowsDirectCompletionArtifactsAlongsideDoneCriteria(t *testing.T) {
	note := "親が確かめた"
	p := &core.ApprovalPreview{
		ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindCompletion, Version: 3,
		DoneCriteria: "結果が Issue に書かれている",
		DirectCompletion: &core.DirectCompletion{
			Artifacts: []string{"https://example.com/result", "o/r#12"}, Note: &note, CreatedAt: time.Now(),
		},
	}
	got := approvalSummaryText(p, core.ApprovalDecisionApproved, true, nil)
	iDone := strings.Index(got, "完了条件:")
	iArt := strings.Index(got, "https://example.com/result")
	if iDone < 0 || iArt < 0 || iArt < iDone {
		t.Fatalf("summary should list the artifacts after the done criteria:\n%s", got)
	}
	for _, want := range []string{"親が直接行った", "o/r#12", "親が確かめた"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}

	p.DirectCompletion = nil
	if strings.Contains(approvalSummaryText(p, core.ApprovalDecisionApproved, true, nil), "親が直接行った") {
		t.Error("an ordinary completion summary mentions a direct completion")
	}
}

func TestUsageText_MentionsCompleteDirectAndHoldKind(t *testing.T) {
	for _, want := range []string{"complete-direct", "--kind <human|parent_direct>"} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usageText lacks %q", want)
		}
	}
}
